package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"szorty.infinigy.pl/internal/shortcode"
)

const codeAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

func openStore(t *testing.T) *Store {
	t.Helper()
	return openAt(t, filepath.Join(t.TempDir(), "szorty.db"))
}

func openAt(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func codeN(n int) string {
	var b [shortcode.Length]byte
	for i := shortcode.Length - 1; i >= 0; i-- {
		b[i] = codeAlphabet[n%len(codeAlphabet)]
		n /= len(codeAlphabet)
	}
	return string(b[:])
}

// codeReader yields the raw bytes shortcode.New maps to codes. Each symbol's
// byte is its alphabet index, which is below the rejection threshold.
func codeReader(codes ...string) *strings.Reader {
	var buf strings.Builder
	for _, code := range codes {
		if len(code) != shortcode.Length {
			panic("codeReader: " + code)
		}
		for i := 0; i < len(code); i++ {
			idx := strings.IndexByte(codeAlphabet, code[i])
			if idx < 0 {
				panic("codeReader: bad symbol in " + code)
			}
			buf.WriteByte(byte(idx))
		}
	}
	return strings.NewReader(buf.String())
}

func TestOpenSeedsSettingsWALAndMigration(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	got, err := s.GetSettings(ctx)
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if got.RetentionDays != 90 || got.MaxCreatesPerHour != 30 {
		t.Fatalf("settings = %+v, want retention 90 and limit 30", got)
	}
	if got.UpdatedAt.Location() != time.UTC {
		t.Fatalf("UpdatedAt location = %v, want UTC", got.UpdatedAt.Location())
	}

	var mode string
	if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
	var busy int
	if err := s.db.QueryRow(`PRAGMA busy_timeout`).Scan(&busy); err != nil {
		t.Fatalf("busy_timeout: %v", err)
	}
	if busy != 5000 {
		t.Fatalf("busy_timeout = %d, want 5000", busy)
	}

	var version string
	if err := s.db.QueryRow(`SELECT version FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatalf("schema_migrations: %v", err)
	}
	if version != "2" {
		t.Fatalf("schema version = %q, want 2", version)
	}
	var indexes int
	if err := s.db.QueryRow(`
		SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'index' AND name IN ('links_author_created', 'links_created')`).Scan(&indexes); err != nil {
		t.Fatalf("indexes: %v", err)
	}
	if indexes != 2 {
		t.Fatalf("named link indexes = %d, want 2", indexes)
	}
}

func TestUpgradeFromVersion1AddsPermanent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "szorty.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("Open raw: %v", err)
	}
	const v1 = `
CREATE TABLE schema_migrations (
  version TEXT PRIMARY KEY
);
CREATE TABLE links (
  code TEXT PRIMARY KEY,
  target_url TEXT NOT NULL,
  author_sub TEXT NOT NULL,
  author_email TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX links_author_created ON links (author_sub, created_at);
CREATE INDEX links_created ON links (created_at);
CREATE TABLE settings (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  retention_days INTEGER NOT NULL,
  max_creates_per_hour INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);`
	if _, err := db.Exec(v1); err != nil {
		t.Fatalf("v1 schema: %v", err)
	}
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC).Unix()
	if _, err := db.Exec(`INSERT INTO schema_migrations (version) VALUES ('1')`); err != nil {
		t.Fatalf("version: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO settings (id, retention_days, max_creates_per_hour, updated_at) VALUES (1, 90, 30, ?)`, now); err != nil {
		t.Fatalf("settings: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO links (code, target_url, author_sub, author_email, created_at) VALUES ('keptCod', 'https://example.com/kept', 'sub', 'a@example.com', ?)`, now); err != nil {
		t.Fatalf("link: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close raw: %v", err)
	}

	s := openAt(t, path)
	ctx := context.Background()
	var v1n, v2n, cols int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = '1'`).Scan(&v1n); err != nil {
		t.Fatalf("version 1: %v", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = '2'`).Scan(&v2n); err != nil {
		t.Fatalf("version 2: %v", err)
	}
	if v1n != 1 || v2n != 1 {
		t.Fatalf("versions v1=%d v2=%d, want 1 and 1", v1n, v2n)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('links') WHERE name = 'permanent'`).Scan(&cols); err != nil {
		t.Fatalf("table info: %v", err)
	}
	if cols != 1 {
		t.Fatalf("permanent column count = %d, want 1", cols)
	}
	links, err := s.ListByAuthor(ctx, "sub", 10, 0)
	if err != nil {
		t.Fatalf("ListByAuthor: %v", err)
	}
	if len(links) != 1 || links[0].Code != "keptCod" || links[0].Permanent {
		t.Fatalf("upgraded link = %+v, want keptCod and not permanent", links)
	}
	target, result, err := s.Resolve(ctx, "keptCod", time.Unix(now, 0).UTC())
	if err != nil || result != ResolveLive || target != "https://example.com/kept" {
		t.Fatalf("Resolve = %q, %v, %v", target, result, err)
	}
}

func TestReopenKeepsSettingsAndLinks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "szorty.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	if err := s.SaveSettings(ctx, 10, 4, now); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	s.rand = codeReader("keptCod")
	code, err := s.Create(ctx, "sub", "a@example.com", "https://example.com/kept", now)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2 := openAt(t, path)
	got, err := s2.GetSettings(ctx)
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if got.RetentionDays != 10 || got.MaxCreatesPerHour != 4 || !got.UpdatedAt.Equal(now) {
		t.Fatalf("settings = %+v, want days 10 limit 4 updated %s", got, now)
	}
	target, result, err := s2.Resolve(ctx, code, now)
	if err != nil || result != ResolveLive || target != "https://example.com/kept" {
		t.Fatalf("Resolve = %q, %v, %v; want live kept target", target, result, err)
	}
	var versions int
	if err := s2.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&versions); err != nil {
		t.Fatalf("schema_migrations: %v", err)
	}
	if versions != 1 {
		t.Fatalf("schema versions = %d, want 1", versions)
	}
}

func TestCreateUsesDefaultRand(t *testing.T) {
	s := openStore(t)
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	code, err := s.Create(context.Background(), "sub", "a@example.com", "https://example.com/", now)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !shortcode.Valid(code) {
		t.Fatalf("code = %q, want a valid short code from crypto/rand", code)
	}
}

func TestCreateAndRead(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	const target = "https://example.com/a?b=1#f"
	s.rand = codeReader("9AZa0z0")
	code, err := s.Create(ctx, "sub-1", "Author@Example.com", target, now)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if code != "9AZa0z0" || !shortcode.Valid(code) {
		t.Fatalf("code = %q", code)
	}

	got, result, err := s.Resolve(ctx, code, now)
	if err != nil || result != ResolveLive || got != target {
		t.Fatalf("Resolve = %q, %v, %v", got, result, err)
	}

	links, err := s.ListByAuthor(ctx, "sub-1", 10, 0)
	if err != nil {
		t.Fatalf("ListByAuthor: %v", err)
	}
	if len(links) != 1 {
		t.Fatalf("len = %d, want 1", len(links))
	}
	link := links[0]
	if link.Code != code || link.TargetURL != target || link.AuthorSub != "sub-1" || link.AuthorEmail != "Author@Example.com" || !link.CreatedAt.Equal(now) {
		t.Fatalf("link = %+v", link)
	}
	if link.CreatedAt.Location() != time.UTC {
		t.Fatalf("CreatedAt location = %v, want UTC", link.CreatedAt.Location())
	}

	missing, result, err := s.Resolve(ctx, "missing", now)
	if err != nil || result != ResolveGone || missing != "" {
		t.Fatalf("missing Resolve = %q, %v, %v", missing, result, err)
	}
}

func TestCreateRetriesUniqueConflict(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	s.rand = codeReader("aaaaaaa")
	first, err := s.Create(ctx, "sub", "a@example.com", "https://example.com/1", now)
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if first != "aaaaaaa" {
		t.Fatalf("first = %q", first)
	}

	s.rand = codeReader("aaaaaaa", "bbbbbbb")
	second, err := s.Create(ctx, "sub", "a@example.com", "https://example.com/2", now)
	if err != nil {
		t.Fatalf("second Create: %v, want retry past the taken code", err)
	}
	if second != "bbbbbbb" {
		t.Fatalf("second = %q, want bbbbbbb", second)
	}
}

func TestCreateUniqueConflictGivesUpAfterFive(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	s.rand = codeReader("aaaaaaa")
	if _, err := s.Create(ctx, "sub", "a@example.com", "https://example.com/1", now); err != nil {
		t.Fatalf("seed Create: %v", err)
	}

	// Five colliding draws, then a free code. The free code must not be inserted:
	// the write stops after the fifth taken primary key.
	s.rand = codeReader("aaaaaaa", "aaaaaaa", "aaaaaaa", "aaaaaaa", "aaaaaaa", "bbbbbbb")
	_, err := s.Create(ctx, "sub", "a@example.com", "https://example.com/2", now)
	if err == nil {
		t.Fatal("Create succeeded, want an error after 5 collisions")
	}
	if errors.Unwrap(err) == nil {
		t.Fatalf("error = %v, want a wrapped error", err)
	}

	links, err := s.ListAll(ctx, 10, 0)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(links) != 1 || links[0].Code != "aaaaaaa" {
		t.Fatalf("links = %+v, want only aaaaaaa", links)
	}
}

func TestResolveExpiredAndLive(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour

	s.rand = codeReader("expired")
	expiredAt := now.Add(-90*day - time.Second)
	expired, err := s.Create(ctx, "sub", "a@example.com", "https://example.com/old", expiredAt)
	if err != nil {
		t.Fatalf("Create expired: %v", err)
	}
	s.rand = codeReader("onlimit")
	boundary, err := s.Create(ctx, "sub", "a@example.com", "https://example.com/edge", now.Add(-90*day))
	if err != nil {
		t.Fatalf("Create boundary: %v", err)
	}

	target, result, err := s.Resolve(ctx, expired, now)
	if err != nil || result != ResolveGone || target != "" {
		t.Fatalf("expired Resolve = %q, %v, %v", target, result, err)
	}
	again, result, err := s.Resolve(ctx, expired, now)
	if err != nil || result != ResolveGone || again != "" {
		t.Fatalf("second Resolve = %q, %v, %v", again, result, err)
	}
	left, err := s.ListByAuthor(ctx, "sub", 10, 0)
	if err != nil {
		t.Fatalf("ListByAuthor: %v", err)
	}
	if len(left) != 1 || left[0].Code != boundary {
		t.Fatalf("links after expired resolve = %+v", left)
	}

	target, result, err = s.Resolve(ctx, boundary, now)
	if err != nil || result != ResolveLive || target != "https://example.com/edge" {
		t.Fatalf("boundary Resolve = %q, %v, %v; want live", target, result, err)
	}
}

func TestResolveBlockedKeepsRow(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	const blocked = "http://127.0.0.1/secret"
	s.rand = codeReader("blocked")
	code, err := s.Create(ctx, "sub", "a@example.com", blocked, now)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	target, result, err := s.Resolve(ctx, code, now)
	if err != nil || result != ResolveBlocked || target != blocked {
		t.Fatalf("Resolve = %q, %v, %v", target, result, err)
	}
	links, err := s.ListByAuthor(ctx, "sub", 10, 0)
	if err != nil {
		t.Fatalf("ListByAuthor: %v", err)
	}
	if len(links) != 1 || links[0].Code != code || links[0].TargetURL != blocked {
		t.Fatalf("row not kept: %+v", links)
	}
}

func TestCreateRateLimit(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 15, 0, 0, 0, time.UTC)
	if err := s.SaveSettings(ctx, 90, 2, now); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}

	s.rand = codeReader("oldrow1")
	if _, err := s.Create(ctx, "sub", "a@example.com", "https://example.com/old", now.Add(-3601*time.Second)); err != nil {
		t.Fatalf("older than an hour Create: %v", err)
	}
	s.rand = codeReader("edgerow")
	if _, err := s.Create(ctx, "sub", "a@example.com", "https://example.com/edge", now.Add(-3600*time.Second)); err != nil {
		t.Fatalf("exactly one hour Create: %v", err)
	}
	s.rand = codeReader("recent1")
	recent, err := s.Create(ctx, "sub", "a@example.com", "https://example.com/new", now)
	if err != nil {
		t.Fatalf("recent Create: %v", err)
	}

	s.rand = codeReader("recent2")
	_, err = s.Create(ctx, "sub", "a@example.com", "https://example.com/more", now)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("over limit error = %v, want ErrRateLimited", err)
	}

	s.rand = codeReader("other01")
	if _, err := s.Create(ctx, "other", "b@example.com", "https://example.com/b", now); err != nil {
		t.Fatalf("other author Create: %v", err)
	}

	found, deleted, err := s.Delete(ctx, recent, "sub", false)
	if err != nil || !found || !deleted {
		t.Fatalf("Delete recent = %v, %v, %v", found, deleted, err)
	}
	s.rand = codeReader("afterdl")
	if _, err := s.Create(ctx, "sub", "a@example.com", "https://example.com/freed", now); err != nil {
		t.Fatalf("Create after delete: %v", err)
	}
}

func TestListByAuthorHidesOthers(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	s.rand = codeReader("olderAA")
	if _, err := s.Create(ctx, "ann", "ann@example.com", "https://example.com/a1", now); err != nil {
		t.Fatalf("Create: %v", err)
	}
	s.rand = codeReader("newerAA")
	if _, err := s.Create(ctx, "ann", "ann@example.com", "https://example.com/a2", now.Add(time.Second)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	s.rand = codeReader("bobcode")
	if _, err := s.Create(ctx, "bob", "bob@example.com", "https://example.com/b", now.Add(2*time.Second)); err != nil {
		t.Fatalf("Create: %v", err)
	}

	ann, err := s.ListByAuthor(ctx, "ann", 10, 0)
	if err != nil {
		t.Fatalf("ListByAuthor: %v", err)
	}
	if len(ann) != 2 || ann[0].Code != "newerAA" || ann[1].Code != "olderAA" {
		t.Fatalf("ann = %+v", ann)
	}
	for _, link := range ann {
		if link.AuthorSub != "ann" {
			t.Fatalf("ann list contains %s", link.AuthorSub)
		}
	}

	none, err := s.ListByAuthor(ctx, "nobody", 10, 0)
	if err != nil {
		t.Fatalf("ListByAuthor nobody: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("nobody = %+v", none)
	}
}

func TestListOrdersEqualTimestampsByCodeDesc(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	s.rand = codeReader("aaaaaaa")
	if _, err := s.Create(ctx, "sub", "a@example.com", "https://example.com/a", now); err != nil {
		t.Fatalf("Create a: %v", err)
	}
	s.rand = codeReader("bbbbbbb")
	if _, err := s.Create(ctx, "sub", "a@example.com", "https://example.com/b", now); err != nil {
		t.Fatalf("Create b: %v", err)
	}
	links, err := s.ListAll(ctx, 10, 0)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(links) != 2 || links[0].Code != "bbbbbbb" || links[1].Code != "aaaaaaa" {
		t.Fatalf("links = %+v, want bbbbbbb then aaaaaaa", codesOf(links))
	}
}

func TestListAllNewestFirstLimit(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	if err := s.SaveSettings(ctx, 90, 1000, base); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}

	s.rand = codeReader("zzzzzzz")
	if _, err := s.Create(ctx, "other", "other@example.com", "https://example.com/z", base.Add(-time.Second)); err != nil {
		t.Fatalf("Create other: %v", err)
	}
	for i := 0; i < 201; i++ {
		s.rand = codeReader(codeN(i))
		if _, err := s.Create(ctx, "owner", "owner@example.com", "https://example.com/"+strconv.Itoa(i), base.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
	}

	page, err := s.ListAll(ctx, 200, 0)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(page) != 200 {
		t.Fatalf("len = %d, want 200", len(page))
	}
	if page[0].Code != codeN(200) || page[199].Code != codeN(1) {
		t.Fatalf("page ends = %s .. %s, want %s .. %s", page[0].Code, page[199].Code, codeN(200), codeN(1))
	}
	for i := 1; i < len(page); i++ {
		prev, cur := page[i-1], page[i]
		if prev.CreatedAt.Before(cur.CreatedAt) || (prev.CreatedAt.Equal(cur.CreatedAt) && prev.Code < cur.Code) {
			t.Fatalf("order broken at %d: %s %s then %s %s", i, prev.Code, prev.CreatedAt, cur.Code, cur.CreatedAt)
		}
	}

	next, err := s.ListAll(ctx, 200, 200)
	if err != nil {
		t.Fatalf("ListAll offset: %v", err)
	}
	if len(next) != 2 || next[0].Code != codeN(0) || next[1].Code != "zzzzzzz" {
		t.Fatalf("offset page = %+v", codesOf(next))
	}

	owner, err := s.ListByAuthor(ctx, "owner", 200, 0)
	if err != nil {
		t.Fatalf("ListByAuthor: %v", err)
	}
	if len(owner) != 200 {
		t.Fatalf("owner page = %d, want 200", len(owner))
	}
	for _, link := range owner {
		if link.AuthorSub != "owner" {
			t.Fatalf("owner list contains %s", link.AuthorSub)
		}
	}
}

func codesOf(links []Link) []string {
	out := make([]string, len(links))
	for i, link := range links {
		out[i] = link.Code
	}
	return out
}

func TestDelete(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	s.rand = codeReader("anncode")
	ann, err := s.Create(ctx, "ann", "ann@example.com", "https://example.com/a", now)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	found, deleted, err := s.Delete(ctx, ann, "bob", false)
	if err != nil || !found || deleted {
		t.Fatalf("non-owner Delete = %v, %v, %v", found, deleted, err)
	}
	target, result, err := s.Resolve(ctx, ann, now)
	if err != nil || result != ResolveLive || target != "https://example.com/a" {
		t.Fatalf("row changed: %q, %v, %v", target, result, err)
	}

	found, deleted, err = s.Delete(ctx, ann, "ann", false)
	if err != nil || !found || !deleted {
		t.Fatalf("author Delete = %v, %v, %v", found, deleted, err)
	}
	if _, result, err = s.Resolve(ctx, ann, now); err != nil || result != ResolveGone {
		t.Fatalf("after author delete: %v, %v", result, err)
	}

	s.rand = codeReader("ownercd")
	owned, err := s.Create(ctx, "ann", "ann@example.com", "https://example.com/o", now)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	found, deleted, err = s.Delete(ctx, owned, "owner", true)
	if err != nil || !found || !deleted {
		t.Fatalf("owner Delete = %v, %v, %v", found, deleted, err)
	}

	found, deleted, err = s.Delete(ctx, "missing", "ann", true)
	if err != nil || found || deleted {
		t.Fatalf("absent Delete = %v, %v, %v", found, deleted, err)
	}
}

func TestSaveSettingsLowerRetentionDeletes(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour

	s.rand = codeReader("tendays")
	old, err := s.Create(ctx, "sub", "a@example.com", "https://example.com/old", now.Add(-10*day))
	if err != nil {
		t.Fatalf("Create old: %v", err)
	}
	s.rand = codeReader("oneday1")
	young, err := s.Create(ctx, "sub", "a@example.com", "https://example.com/new", now.Add(-day))
	if err != nil {
		t.Fatalf("Create young: %v", err)
	}

	if err := s.SaveSettings(ctx, 9, 40, now); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	got, err := s.GetSettings(ctx)
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if got.RetentionDays != 9 || got.MaxCreatesPerHour != 40 || !got.UpdatedAt.Equal(now) {
		t.Fatalf("settings = %+v", got)
	}

	_, result, err := s.Resolve(ctx, old, now)
	if err != nil || result != ResolveGone {
		t.Fatalf("old Resolve = %v, %v", result, err)
	}
	target, result, err := s.Resolve(ctx, young, now)
	if err != nil || result != ResolveLive || target != "https://example.com/new" {
		t.Fatalf("young Resolve = %q, %v, %v", target, result, err)
	}
}

func TestSaveSettingsRaiseRetentionKeeps(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour

	// Still in the table, but already past the seeded 90-day retention.
	s.rand = codeReader("hundred")
	keep, err := s.Create(ctx, "sub", "a@example.com", "https://example.com/keep", now.Add(-100*day))
	if err != nil {
		t.Fatalf("Create keep: %v", err)
	}
	s.rand = codeReader("twohund")
	drop, err := s.Create(ctx, "sub", "a@example.com", "https://example.com/drop", now.Add(-200*day))
	if err != nil {
		t.Fatalf("Create drop: %v", err)
	}

	if err := s.SaveSettings(ctx, 120, 30, now); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}

	target, result, err := s.Resolve(ctx, keep, now)
	if err != nil || result != ResolveLive || target != "https://example.com/keep" {
		t.Fatalf("kept Resolve = %q, %v, %v", target, result, err)
	}
	_, result, err = s.Resolve(ctx, drop, now)
	if err != nil || result != ResolveGone {
		t.Fatalf("still-expired Resolve = %v, %v", result, err)
	}
	got, err := s.GetSettings(ctx)
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if got.RetentionDays != 120 || got.MaxCreatesPerHour != 30 {
		t.Fatalf("settings = %+v", got)
	}
}

func TestSweep(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour

	s.rand = codeReader("sweep01")
	old1, err := s.Create(ctx, "sub", "a@example.com", "https://example.com/1", now.Add(-91*day))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	s.rand = codeReader("sweep02")
	old2, err := s.Create(ctx, "sub", "a@example.com", "https://example.com/2", now.Add(-100*day))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	s.rand = codeReader("sweep03")
	live, err := s.Create(ctx, "sub", "a@example.com", "https://example.com/3", now.Add(-day))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	n, err := s.Sweep(ctx, now)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if n != 2 {
		t.Fatalf("Sweep deleted %d, want 2", n)
	}
	for _, code := range []string{old1, old2} {
		_, result, err := s.Resolve(ctx, code, now)
		if err != nil || result != ResolveGone {
			t.Fatalf("Resolve %s = %v, %v", code, result, err)
		}
	}
	target, result, err := s.Resolve(ctx, live, now)
	if err != nil || result != ResolveLive || target != "https://example.com/3" {
		t.Fatalf("live Resolve = %q, %v, %v", target, result, err)
	}
	n, err = s.Sweep(ctx, now)
	if err != nil || n != 0 {
		t.Fatalf("second Sweep = %d, %v", n, err)
	}
}

func TestCreateChosenPermanentAndTaken(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

	code, err := s.CreateChosen(ctx, "sub", "a@example.com", "https://example.com/a", "amazon", true, now)
	if err != nil {
		t.Fatalf("CreateChosen: %v", err)
	}
	if code != "amazon" {
		t.Fatalf("code = %q", code)
	}
	links, err := s.ListByAuthor(ctx, "sub", 10, 0)
	if err != nil {
		t.Fatalf("ListByAuthor: %v", err)
	}
	if len(links) != 1 || !links[0].Permanent || links[0].TargetURL != "https://example.com/a" {
		t.Fatalf("link = %+v", links)
	}

	_, err = s.CreateChosen(ctx, "other", "b@example.com", "https://example.com/b", "amazon", false, now)
	if !errors.Is(err, ErrCodeTaken) {
		t.Fatalf("taken error = %v, want ErrCodeTaken", err)
	}
	left, err := s.ListAll(ctx, 10, 0)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(left) != 1 || left[0].AuthorSub != "sub" || !left[0].Permanent {
		t.Fatalf("after taken = %+v", left)
	}

	_, err = s.CreateChosen(ctx, "sub", "a@example.com", "https://example.com/c", "Amazon", false, now)
	if !errors.Is(err, ErrBadCode) {
		t.Fatalf("mixed-case error = %v, want ErrBadCode", err)
	}
	_, err = s.CreateChosen(ctx, "sub", "a@example.com", "https://example.com/c", "a", false, now)
	if !errors.Is(err, ErrBadCode) {
		t.Fatalf("short error = %v, want ErrBadCode", err)
	}

	if err := s.SaveSettings(ctx, 90, 1, now); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	_, err = s.CreateChosen(ctx, "sub", "a@example.com", "https://example.com/d", "kept-name", true, now)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("rate error = %v, want ErrRateLimited", err)
	}
	if rows, err := s.ListAll(ctx, 10, 0); err != nil || len(rows) != 1 {
		t.Fatalf("rows after rate limit = %+v, %v", rows, err)
	}

	s.rand = codeReader("randCod")
	random, err := s.CreateChosen(ctx, "other", "b@example.com", "https://example.com/r", "", true, now)
	if err != nil || random != "randCod" {
		t.Fatalf("random permanent = %q, %v", random, err)
	}
	got, err := s.ListByAuthor(ctx, "other", 10, 0)
	if err != nil || len(got) != 1 || got[0].Code != "randCod" || !got[0].Permanent {
		t.Fatalf("random link = %+v, %v", got, err)
	}
}

func TestPermanentSurvivesExpiry(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	old := now.Add(-100 * day)

	kept, err := s.CreateChosen(ctx, "sub", "a@example.com", "https://example.com/kept", "amazon", true, old)
	if err != nil {
		t.Fatalf("permanent: %v", err)
	}
	s.rand = codeReader("expired")
	dropped, err := s.Create(ctx, "sub", "a@example.com", "https://example.com/old", old)
	if err != nil {
		t.Fatalf("ordinary: %v", err)
	}
	blocked, err := s.CreateChosen(ctx, "sub", "a@example.com", "http://127.0.0.1/secret", "blocked-keep", true, old)
	if err != nil {
		t.Fatalf("blocked permanent: %v", err)
	}

	target, result, err := s.Resolve(ctx, "Amazon", now)
	if err != nil || result != ResolveLive || target != "https://example.com/kept" {
		t.Fatalf("Amazon Resolve = %q, %v, %v", target, result, err)
	}
	target, result, err = s.Resolve(ctx, kept, now)
	if err != nil || result != ResolveLive || target != "https://example.com/kept" {
		t.Fatalf("exact Resolve = %q, %v, %v", target, result, err)
	}
	if _, result, err = s.Resolve(ctx, dropped, now); err != nil || result != ResolveGone {
		t.Fatalf("ordinary Resolve = %v, %v", result, err)
	}
	target, result, err = s.Resolve(ctx, "Blocked-Keep", now)
	if err != nil || result != ResolveBlocked || target != "http://127.0.0.1/secret" {
		t.Fatalf("blocked Resolve = %q, %v, %v", target, result, err)
	}

	s.rand = codeReader("sweep02")
	alsoOld, err := s.Create(ctx, "sub", "a@example.com", "https://example.com/2", old)
	if err != nil {
		t.Fatalf("sweep row: %v", err)
	}
	n, err := s.Sweep(ctx, now)
	if err != nil || n != 1 {
		t.Fatalf("Sweep = %d, %v, want 1", n, err)
	}
	if _, result, err = s.Resolve(ctx, alsoOld, now); err != nil || result != ResolveGone {
		t.Fatalf("swept Resolve = %v, %v", result, err)
	}
	if _, result, err = s.Resolve(ctx, kept, now); err != nil || result != ResolveLive {
		t.Fatalf("permanent after sweep = %v, %v", result, err)
	}

	if err := s.SaveSettings(ctx, 1, 30, now); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	if _, result, err = s.Resolve(ctx, kept, now); err != nil || result != ResolveLive {
		t.Fatalf("permanent after shorter retention = %v, %v", result, err)
	}
	if _, result, err = s.Resolve(ctx, blocked, now); err != nil || result != ResolveBlocked {
		t.Fatalf("blocked after shorter retention = %v, %v", result, err)
	}
}

func TestLookupDoesNotFoldRandomCase(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	s.rand = codeReader("Abcdefg")
	if _, err := s.Create(ctx, "sub", "a@example.com", "https://example.com/r", now); err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, result, err := s.Resolve(ctx, "abcdefg", now)
	if err != nil || result != ResolveGone {
		t.Fatalf("lowercase random Resolve = %v, %v", result, err)
	}
	target, result, err := s.Resolve(ctx, "Abcdefg", now)
	if err != nil || result != ResolveLive || target != "https://example.com/r" {
		t.Fatalf("exact random Resolve = %q, %v, %v", target, result, err)
	}

	found, deleted, err := s.Delete(ctx, "AMAZON", "sub", false)
	if err != nil || found || deleted {
		t.Fatalf("missing Delete = %v, %v, %v", found, deleted, err)
	}
	kept, err := s.CreateChosen(ctx, "sub", "a@example.com", "https://example.com/a", "amazon", false, now)
	if err != nil {
		t.Fatalf("CreateChosen: %v", err)
	}
	found, deleted, err = s.Delete(ctx, "Amazon", "bob", false)
	if err != nil || !found || deleted {
		t.Fatalf("foreign Delete = %v, %v, %v", found, deleted, err)
	}
	found, deleted, err = s.Delete(ctx, "Amazon", "sub", false)
	if err != nil || !found || !deleted {
		t.Fatalf("author Delete = %v, %v, %v", found, deleted, err)
	}
	if _, result, err = s.Resolve(ctx, kept, now); err != nil || result != ResolveGone {
		t.Fatalf("after delete = %v, %v", result, err)
	}
}

func TestSetPermanent(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	if _, err := s.CreateChosen(ctx, "ann", "ann@example.com", "https://example.com/a", "amazon", false, now); err != nil {
		t.Fatalf("CreateChosen: %v", err)
	}

	found, updated, err := s.SetPermanent(ctx, "Amazon", "bob", false, true)
	if err != nil || !found || updated {
		t.Fatalf("foreign SetPermanent = %v, %v, %v", found, updated, err)
	}
	links, err := s.ListByAuthor(ctx, "ann", 10, 0)
	if err != nil || len(links) != 1 || links[0].Permanent {
		t.Fatalf("foreign write changed row: %+v, %v", links, err)
	}

	found, updated, err = s.SetPermanent(ctx, "Amazon", "ann", false, true)
	if err != nil || !found || !updated {
		t.Fatalf("author pin = %v, %v, %v", found, updated, err)
	}
	found, updated, err = s.SetPermanent(ctx, "amazon", "ann", false, true)
	if err != nil || !found || !updated {
		t.Fatalf("second pin = %v, %v, %v", found, updated, err)
	}
	links, err = s.ListByAuthor(ctx, "ann", 10, 0)
	if err != nil || !links[0].Permanent {
		t.Fatalf("after pin = %+v, %v", links, err)
	}

	found, updated, err = s.SetPermanent(ctx, "amazon", "owner", true, false)
	if err != nil || !found || !updated {
		t.Fatalf("owner unpin = %v, %v, %v", found, updated, err)
	}
	links, err = s.ListByAuthor(ctx, "ann", 10, 0)
	if err != nil || links[0].Permanent {
		t.Fatalf("after unpin = %+v, %v", links, err)
	}

	found, updated, err = s.SetPermanent(ctx, "missing", "ann", true, true)
	if err != nil || found || updated {
		t.Fatalf("absent SetPermanent = %v, %v, %v", found, updated, err)
	}
}
