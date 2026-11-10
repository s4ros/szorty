package httpapp

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"szorty.infinigy.pl/internal/clock"
	"szorty.infinigy.pl/internal/config"
	"szorty.infinigy.pl/internal/session"
	"szorty.infinigy.pl/internal/shortcode"
	"szorty.infinigy.pl/internal/store"
)

const (
	csp      = "default-src 'self'; style-src 'self'; form-action 'self'; base-uri 'self'; frame-ancestors 'none'"
	pageSize = 200

	msgBadURL       = "To nie jest publiczny adres http albo https."
	msgOAuth        = "Logowanie nie doszło do skutku."
	msgBadSettings  = "Podaj liczbę z dozwolonego zakresu."
	msgCreateFail   = "Nie udało się utworzyć skrótu. Spróbuj jeszcze raz."
	msgSettingsFail = "Nie udało się zapisać ustawień. Spróbuj jeszcze raz."
	msgSaved        = "Zapisane."
	msgEmptyMine    = "Nie masz jeszcze skrótów."
	msgEmptyAll     = "Nie ma żadnych skrótów."
	msgPageFail     = "Nie udało się wczytać strony. Spróbuj jeszcze raz."
)

// Exchanger turns an OAuth authorization code into an identity.
// The production implementation calls Google. Tests pass a fake.
type Exchanger interface {
	Exchange(ctx context.Context, code string) (Identity, error)
}

// Identity is the account an Exchanger read from the provider.
type Identity struct {
	Sub, Email    string
	EmailVerified bool
}

// App is the HTTP application.
type App struct {
	cfg       config.Config
	store     *store.Store
	clock     clock.Clock
	sessions  *session.Manager
	exchanger Exchanger
	tmpl      *template.Template
	css       []byte
	oauth     *oauth2.Config
}

type view struct {
	Title   string
	Email   string
	Owner   bool
	Error   string
	Notice  string
	NewLink string
	Links   []linkView
	Empty   string
	Older   string
	Newer   string
	Days    int
	Limit   int
}

type linkView struct {
	Code     string
	ShortURL string
	Target   string
	Created  string
	DaysLeft int
	Author   string
	Next     string
}

// route is a literal path. A single path segment must not match a short code.
type route struct {
	method string
	path   string
	fn     func(*App, http.ResponseWriter, *http.Request)
}

var routes = []route{
	{http.MethodGet, "/", (*App).home},
	{http.MethodPost, "/links", (*App).create},
	{http.MethodGet, "/wszystkie", (*App).listAll},
	{http.MethodGet, "/ustawienia", (*App).settingsGet},
	{http.MethodPost, "/ustawienia", (*App).settingsPost},
	{http.MethodGet, "/auth/google", (*App).authGoogle},
	{http.MethodGet, "/auth/callback", (*App).authCallback},
	{http.MethodPost, "/logout", (*App).logout},
	{http.MethodGet, "/health", (*App).health},
	{http.MethodGet, "/style.css", (*App).style},
}

// New builds the application. exchanger is called only on the OAuth callback.
func New(cfg config.Config, st *store.Store, clk clock.Clock, exchanger Exchanger) *App {
	tmpl, err := template.ParseFS(assets, "templates/*.html")
	if err != nil {
		panic(err)
	}
	css, err := assets.ReadFile("static/style.css")
	if err != nil {
		panic(err)
	}
	return &App{
		cfg:       cfg,
		store:     st,
		clock:     clk,
		sessions:  session.NewManager(cfg.SessionSecret, cfg.OwnerEmail, cfg.PublicBaseURL),
		exchanger: exchanger,
		tmpl:      tmpl,
		css:       css,
		oauth:     oauthConfig(cfg),
	}
}

// Handler serves every public route.
func (a *App) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", csp)
		a.serve(w, r)
	})
}

func (a *App) serve(w http.ResponseWriter, r *http.Request) {
	for _, rt := range routes {
		if r.Method == rt.method && r.URL.Path == rt.path {
			rt.fn(a, w, r)
			return
		}
	}
	if r.Method == http.MethodGet {
		if code, ok := oneSegment(r.URL.Path); ok && shortcode.Valid(code) {
			a.resolve(w, r, code)
			return
		}
	}
	if r.Method == http.MethodPost {
		if code, ok := deletePath(r.URL.Path); ok {
			a.remove(w, r, code)
			return
		}
	}
	a.unknown(w, r)
}

func oauthConfig(cfg config.Config) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     cfg.GoogleClientID,
		ClientSecret: cfg.GoogleClientSecret,
		RedirectURL:  strings.TrimRight(cfg.PublicBaseURL, "/") + "/auth/callback",
		Scopes:       []string{"openid", "email", "profile"},
		Endpoint:     google.Endpoint,
	}
}

func (a *App) render(w http.ResponseWriter, status int, name string, data view) {
	var buf bytes.Buffer
	if err := a.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		log.Printf("template %s failed", name)
		http.Error(w, msgPageFail, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

func (a *App) baseView(r *http.Request) view {
	v := view{Title: "Szort"}
	u, ok := a.sessions.FromRequest(r)
	if !ok {
		return v
	}
	v.Email = u.Email
	v.Owner = a.sessions.IsOwner(u)
	return v
}

func (a *App) owner(r *http.Request) (session.User, bool) {
	u, ok := a.sessions.FromRequest(r)
	if !ok || !a.sessions.IsOwner(u) {
		return session.User{}, false
	}
	return u, true
}

func (a *App) message(w http.ResponseWriter, r *http.Request, status int, msg string) {
	v := a.baseView(r)
	v.Error = msg
	a.render(w, status, "message", v)
}

func (a *App) unknown(w http.ResponseWriter, r *http.Request) {
	a.render(w, http.StatusNotFound, "unknown", a.baseView(r))
}

func (a *App) dead(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	a.render(w, http.StatusNotFound, "dead", a.baseView(r))
}

func (a *App) absolute(code string) string {
	return strings.TrimRight(a.cfg.PublicBaseURL, "/") + "/" + code
}

// daysLeft is full days until created_at + retention, floored, and never negative.
func daysLeft(created time.Time, retentionDays int, now time.Time) int {
	secs := created.Unix() + int64(retentionDays)*86400 - now.Unix()
	if secs <= 0 {
		return 0
	}
	return int(secs / 86400)
}

func (a *App) toLinks(rows []store.Link, retention int, now time.Time, next string, withAuthor bool) []linkView {
	out := make([]linkView, 0, len(rows))
	for _, row := range rows {
		item := linkView{
			Code:     row.Code,
			ShortURL: a.absolute(row.Code),
			Target:   row.TargetURL,
			Created:  row.CreatedAt.UTC().Format("2006-01-02 15:04 UTC"),
			DaysLeft: daysLeft(row.CreatedAt, retention, now),
			Next:     next,
		}
		if withAuthor {
			item.Author = row.AuthorEmail
		}
		out = append(out, item)
	}
	return out
}

func (a *App) loadLinks(ctx context.Context, v *view, page int, now time.Time, sub, next, empty string, mine bool) error {
	settings, err := a.store.GetSettings(ctx)
	if err != nil {
		return err
	}
	offset := (page - 1) * pageSize
	var rows []store.Link
	if mine {
		rows, err = a.store.ListByAuthor(ctx, sub, pageSize, offset)
	} else {
		rows, err = a.store.ListAll(ctx, pageSize, offset)
	}
	if err != nil {
		return err
	}
	path := "/"
	if !mine {
		path = "/wszystkie"
	}
	v.Links = a.toLinks(rows, settings.RetentionDays, now, next, !mine)
	if page == 1 && len(rows) == 0 {
		v.Empty = empty
	}
	if len(rows) == pageSize {
		v.Older = path + "?strona=" + strconv.Itoa(page+1)
	}
	if page > 1 {
		v.Newer = path + "?strona=" + strconv.Itoa(page-1)
	}
	return nil
}

func (a *App) authorHas(ctx context.Context, sub, code string) (bool, error) {
	if sub == "" || !shortcode.Valid(code) {
		return false, nil
	}
	for offset := 0; ; offset += pageSize {
		rows, err := a.store.ListByAuthor(ctx, sub, pageSize, offset)
		if err != nil {
			return false, err
		}
		for _, row := range rows {
			if row.Code == code {
				return true, nil
			}
		}
		if len(rows) < pageSize {
			return false, nil
		}
	}
}

func pageFrom(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("strona"))
	if err != nil || n < 1 {
		return 1
	}
	maxPage := (int(^uint(0)>>1) / pageSize) + 1
	if n > maxPage {
		return maxPage
	}
	return n
}

// safeNext allows only the two list pages. The owner list is not a return
// target for anyone else.
func safeNext(next string, owner bool) string {
	if next == "/" {
		return "/"
	}
	if next == "/wszystkie" && owner {
		return "/wszystkie"
	}
	return "/"
}

func oneSegment(path string) (string, bool) {
	if len(path) < 2 || path[0] != '/' {
		return "", false
	}
	seg := path[1:]
	if strings.Contains(seg, "/") {
		return "", false
	}
	return seg, true
}

func deletePath(path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, "/links/")
	if !ok {
		return "", false
	}
	code, ok := strings.CutSuffix(rest, "/usun")
	if !ok || code == "" || strings.Contains(code, "/") {
		return "", false
	}
	return code, true
}

func randomState() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func stateMatches(cookie, query string) bool {
	if cookie == "" || query == "" || len(cookie) != len(query) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(cookie), []byte(query)) == 1
}

func limitBody(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
}
