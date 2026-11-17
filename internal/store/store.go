package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"modernc.org/sqlite"

	"szorty.infinigy.pl/internal/shortcode"
	"szorty.infinigy.pl/internal/urlcheck"
)

//go:embed schema.sql
var schemaFS embed.FS

const (
	schemaVersion            = "2"
	daySeconds               = 86400
	hourSeconds              = 3600
	createTries              = 5
	defaultRetentionDays     = 90
	defaultMaxCreatesPerHour = 30

	// SQLITE_CONSTRAINT_PRIMARYKEY and SQLITE_CONSTRAINT_UNIQUE.
	sqliteConstraintPrimaryKey = 19 | (6 << 8)
	sqliteConstraintUnique     = 19 | (8 << 8)
)

type Link struct {
	Code        string
	TargetURL   string
	AuthorSub   string
	AuthorEmail string
	CreatedAt   time.Time // UTC
	Permanent   bool
}

type Settings struct {
	RetentionDays     int
	MaxCreatesPerHour int
	UpdatedAt         time.Time
}

// ErrRateLimited is returned when this author already has MaxCreatesPerHour
// rows with created_at in the last 3600 seconds.
var ErrRateLimited = errors.New("rate limited")

// ErrCodeTaken is returned when the caller supplied a code that is already stored.
var ErrCodeTaken = errors.New("code taken")

// ErrBadCode is returned when a supplied code is not a random code and not a custom slug.
// Empty code means "generate" and is not ErrBadCode.
var ErrBadCode = errors.New("bad code")

// ResolveResult is the outcome of looking up a code.
type ResolveResult int

const (
	// ResolveGone means the code is missing, or an ordinary row was expired and deleted.
	ResolveGone ResolveResult = iota
	// ResolveLive means the row is permanent or inside retention, and the target passed urlcheck.Validate.
	ResolveLive
	// ResolveBlocked means the row is permanent or inside retention, but the target failed urlcheck.Validate.
	// The row is kept.
	ResolveBlocked
)

type Store struct {
	db *sql.DB
	// rand supplies bytes to shortcode.New. Open sets it to crypto/rand.Reader.
	rand io.Reader
}

// Open opens the database at path, enables WAL and a 5 second busy timeout,
// applies the embedded schema once, and seeds settings when the row is missing.
func Open(path string) (*Store, error) {
	dsn := path + "?_busy_timeout=5000&_journal_mode=WAL&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	ctx := context.Background()
	if err := migrate(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	if err := seedSettings(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, rand: rand.Reader}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func migrate(ctx context.Context, db *sql.DB) error {
	var name string
	err := db.QueryRowContext(ctx, `
		SELECT name FROM sqlite_master
		WHERE type = 'table' AND name = 'schema_migrations'`).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return applyFresh(ctx, db)
	}
	if err != nil {
		return err
	}
	has2, err := hasSchemaVersion(ctx, db, "2")
	if err != nil {
		return err
	}
	if has2 {
		return nil
	}
	has1, err := hasSchemaVersion(ctx, db, "1")
	if err != nil {
		return err
	}
	if !has1 {
		return fmt.Errorf("schema version %s is not applied", schemaVersion)
	}
	return applyPermanentColumn(ctx, db)
}

func hasSchemaVersion(ctx context.Context, db *sql.DB, version string) (bool, error) {
	var n int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

func applyFresh(ctx context.Context, db *sql.DB) error {
	schema, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, string(schema)); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO schema_migrations (version) VALUES (?)`, schemaVersion); err != nil {
		return err
	}
	return tx.Commit()
}

func applyPermanentColumn(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		ALTER TABLE links ADD COLUMN permanent INTEGER NOT NULL DEFAULT 0`); err != nil {
		return fmt.Errorf("add permanent: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO schema_migrations (version) VALUES (?)`, schemaVersion); err != nil {
		return err
	}
	return tx.Commit()
}

func seedSettings(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO settings (id, retention_days, max_creates_per_hour, updated_at)
		VALUES (1, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING`, defaultRetentionDays, defaultMaxCreatesPerHour, time.Now().UTC().Unix())
	return err
}

// Create inserts one random, non-permanent row.
func (s *Store) Create(ctx context.Context, authorSub, authorEmail, target string, now time.Time) (string, error) {
	return s.CreateChosen(ctx, authorSub, authorEmail, target, "", false, now)
}

// CreateChosen inserts one row.
// Empty code generates a random code and retries a primary-key collision up to 5 times.
// A supplied code must already be Valid or Custom; this method does not lowercase it.
// ErrCodeTaken when that code is already stored. ErrRateLimited when this author's
// rows in the last 3600 seconds are already >= settings.MaxCreatesPerHour.
func (s *Store) CreateChosen(ctx context.Context, authorSub, authorEmail, target, code string, permanent bool, now time.Time) (string, error) {
	if code != "" && !shortcode.Valid(code) && !shortcode.Custom(code) {
		return "", ErrBadCode
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	var max int
	if err := tx.QueryRowContext(ctx, `
		SELECT max_creates_per_hour FROM settings WHERE id = 1`).Scan(&max); err != nil {
		return "", err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM links
		WHERE author_sub = ? AND created_at >= ?`, authorSub, now.Unix()-hourSeconds).Scan(&count); err != nil {
		return "", err
	}
	if count >= max {
		return "", ErrRateLimited
	}

	flag := 0
	if permanent {
		flag = 1
	}
	if code != "" {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO links (code, target_url, author_sub, author_email, created_at, permanent)
			VALUES (?, ?, ?, ?, ?, ?)`, code, target, authorSub, authorEmail, now.Unix(), flag)
		if err == nil {
			if err := tx.Commit(); err != nil {
				return "", err
			}
			return code, nil
		}
		if primaryKeyTaken(err) {
			return "", ErrCodeTaken
		}
		return "", fmt.Errorf("insert link: %w", err)
	}

	var last error
	for try := 0; try < createTries; try++ {
		generated, err := shortcode.New(s.rand)
		if err != nil {
			return "", fmt.Errorf("new short code: %w", err)
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO links (code, target_url, author_sub, author_email, created_at, permanent)
			VALUES (?, ?, ?, ?, ?, ?)`, generated, target, authorSub, authorEmail, now.Unix(), flag)
		if err == nil {
			if err := tx.Commit(); err != nil {
				return "", err
			}
			return generated, nil
		}
		if !primaryKeyTaken(err) {
			return "", fmt.Errorf("insert link: %w", err)
		}
		last = err
	}
	return "", fmt.Errorf("short code still taken after 5 tries: %w", last)
}

func primaryKeyTaken(err error) bool {
	var se *sqlite.Error
	if errors.As(err, &se) {
		code := se.Code()
		if code == sqliteConstraintPrimaryKey || code == sqliteConstraintUnique {
			return true
		}
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed: links.code")
}

// lookupCode returns the stored primary key.
// It tries code exactly, then the lowercase custom slug when that row is absent.
// A mixed-case random code is not matched through its lowercase spelling.
func lookupCode(ctx context.Context, tx *sql.Tx, code string) (string, error) {
	var stored string
	err := tx.QueryRowContext(ctx, `SELECT code FROM links WHERE code = ?`, code).Scan(&stored)
	if err == nil {
		return stored, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	lower := strings.ToLower(code)
	if lower == code || !shortcode.Custom(lower) {
		return "", sql.ErrNoRows
	}
	err = tx.QueryRowContext(ctx, `SELECT code FROM links WHERE code = ?`, lower).Scan(&stored)
	if err != nil {
		return "", err
	}
	return stored, nil
}

// Resolve returns the stored target and whether the code is live, blocked, or gone.
// An expired row is deleted in this call.
func (s *Store) Resolve(ctx context.Context, code string, now time.Time) (string, ResolveResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", ResolveGone, err
	}
	defer tx.Rollback()

	stored, err := lookupCode(ctx, tx, code)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ResolveGone, nil
	}
	if err != nil {
		return "", ResolveGone, err
	}

	var target string
	var created int64
	var permanent int
	err = tx.QueryRowContext(ctx, `
		SELECT target_url, created_at, permanent FROM links WHERE code = ?`, stored).Scan(&target, &created, &permanent)
	if err != nil {
		return "", ResolveGone, err
	}

	var days int
	if err := tx.QueryRowContext(ctx, `
		SELECT retention_days FROM settings WHERE id = 1`).Scan(&days); err != nil {
		return "", ResolveGone, err
	}
	if permanent == 0 && created+int64(days)*daySeconds < now.Unix() {
		if _, err := tx.ExecContext(ctx, `DELETE FROM links WHERE code = ?`, stored); err != nil {
			return "", ResolveGone, err
		}
		if err := tx.Commit(); err != nil {
			return "", ResolveGone, err
		}
		return "", ResolveGone, nil
	}
	if _, err := urlcheck.Validate(target); err != nil {
		return target, ResolveBlocked, nil
	}
	return target, ResolveLive, nil
}

// ListByAuthor returns this author's rows, newest first.
func (s *Store) ListByAuthor(ctx context.Context, sub string, limit, offset int) ([]Link, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT code, target_url, author_sub, author_email, created_at, permanent
		FROM links
		WHERE author_sub = ?
		ORDER BY created_at DESC, code DESC
		LIMIT ? OFFSET ?`, sub, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanLinks(rows)
}

// ListAll returns every row still in the table, newest first.
func (s *Store) ListAll(ctx context.Context, limit, offset int) ([]Link, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT code, target_url, author_sub, author_email, created_at, permanent
		FROM links
		ORDER BY created_at DESC, code DESC
		LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanLinks(rows)
}

func scanLinks(rows *sql.Rows) ([]Link, error) {
	out := []Link{}
	for rows.Next() {
		var link Link
		var created int64
		var permanent int
		if err := rows.Scan(&link.Code, &link.TargetURL, &link.AuthorSub, &link.AuthorEmail, &created, &permanent); err != nil {
			return nil, err
		}
		link.CreatedAt = time.Unix(created, 0).UTC()
		link.Permanent = permanent != 0
		out = append(out, link)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Delete removes the row when the actor is the author or owner.
// found is false when the code is absent.
// deleted is false when the actor is not the author and not owner.
func (s *Store) Delete(ctx context.Context, code, actorSub string, owner bool) (found, deleted bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, false, err
	}
	defer tx.Rollback()

	stored, err := lookupCode(ctx, tx, code)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	var author string
	err = tx.QueryRowContext(ctx, `SELECT author_sub FROM links WHERE code = ?`, stored).Scan(&author)
	if err != nil {
		return false, false, err
	}
	if !owner && author != actorSub {
		return true, false, nil
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM links WHERE code = ?`, stored)
	if err != nil {
		return false, false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, false, err
	}
	if n == 0 {
		return false, false, nil
	}
	if err := tx.Commit(); err != nil {
		return false, false, err
	}
	return true, true, nil
}

// SetPermanent sets links.permanent when the actor is the author or owner.
// found is false when the code is absent.
// updated is false when the actor is not the author and not owner.
// An allowed write of the current value still returns updated true.
func (s *Store) SetPermanent(ctx context.Context, code, actorSub string, owner, permanent bool) (found, updated bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, false, err
	}
	defer tx.Rollback()

	stored, err := lookupCode(ctx, tx, code)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	var author string
	if err := tx.QueryRowContext(ctx, `SELECT author_sub FROM links WHERE code = ?`, stored).Scan(&author); err != nil {
		return false, false, err
	}
	if !owner && author != actorSub {
		return true, false, nil
	}
	flag := 0
	if permanent {
		flag = 1
	}
	if _, err := tx.ExecContext(ctx, `UPDATE links SET permanent = ? WHERE code = ?`, flag, stored); err != nil {
		return false, false, err
	}
	if err := tx.Commit(); err != nil {
		return false, false, err
	}
	return true, true, nil
}

func (s *Store) GetSettings(ctx context.Context) (Settings, error) {
	var got Settings
	var updated int64
	err := s.db.QueryRowContext(ctx, `
		SELECT retention_days, max_creates_per_hour, updated_at
		FROM settings WHERE id = 1`).Scan(&got.RetentionDays, &got.MaxCreatesPerHour, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Settings{}, errors.New("settings row missing")
	}
	if err != nil {
		return Settings{}, err
	}
	got.UpdatedAt = time.Unix(updated, 0).UTC()
	return got, nil
}

// SaveSettings updates the single row and deletes rows expired under the
// new retention, in one transaction. Caller already checked bounds.
func (s *Store) SaveSettings(ctx context.Context, days, limit int, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `
		UPDATE settings
		SET retention_days = ?, max_creates_per_hour = ?, updated_at = ?
		WHERE id = 1`, days, limit, now.Unix())
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("settings row missing")
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM links
		WHERE permanent = 0 AND created_at + ? < ?`, int64(days)*daySeconds, now.Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

// Sweep deletes rows expired under the current retention and returns how many.
func (s *Store) Sweep(ctx context.Context, now time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM links
		WHERE permanent = 0
		  AND created_at + (
		    SELECT retention_days FROM settings WHERE id = 1
		  ) * ? < ?`, daySeconds, now.Unix())
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}
