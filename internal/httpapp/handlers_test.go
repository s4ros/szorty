package httpapp

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"szorty.infinigy.pl/internal/config"
	"szorty.infinigy.pl/internal/session"
	"szorty.infinigy.pl/internal/shortcode"
	"szorty.infinigy.pl/internal/store"
)

const (
	wantCSP = "default-src 'self'; style-src 'self'; form-action 'self'; base-uri 'self'; frame-ancestors 'none'"

	wantBadURL       = "To nie jest publiczny adres http albo https."
	wantRate         = "Za dużo skrótów w tej godzinie. Limit to 1."
	wantOAuth        = "Logowanie nie doszło do skutku."
	wantDead         = "Ten skrót nie działa."
	wantUnknown      = "Nie ma takiej strony."
	wantBadSettings  = "Podaj liczbę z dozwolonego zakresu."
	wantSaved        = "Zapisane."
	wantEmptyMine    = "Nie masz jeszcze skrótów."
	wantEmptyAll     = "Nie ma żadnych skrótów."
	wantLogin        = "Zaloguj przez Google"
	wantButton       = "Szortuj to!"
	wantSettingsNote = "Mniejsza liczba dni kasuje starsze żywe skróty przy tym zapisie."

	publicBase = "https://szorty.infinigy.pl"
	ownerEmail = "owner@example.com"
)

func TestAnonymousHome(t *testing.T) {
	f := newFixture(t)
	rec := get(f.h(), "/")
	status(t, rec, http.StatusOK)
	assertHTML(t, rec)
	body := rec.Body.String()
	mustContain(t, body, wantLogin)
	mustContain(t, body, `href="/auth/google"`)
	mustNotContain(t, body, wantButton)
	mustNotContain(t, body, `name="url"`)
	mustNotContain(t, body, wantEmptyMine)
}

func TestUnauthenticatedMutationsRedirectToLogin(t *testing.T) {
	f := newFixture(t)
	before := f.count(t)

	links := postForm(f.h(), "/links", url.Values{"url": {"https://example.com/a"}})
	status(t, links, http.StatusSeeOther)
	if loc := links.Header().Get("Location"); loc != "/auth/google" {
		t.Fatalf("POST /links location = %q", loc)
	}

	del := postForm(f.h(), "/links/abcdefg/usun", url.Values{"next": {"/"}})
	status(t, del, http.StatusSeeOther)
	if loc := del.Header().Get("Location"); loc != "/auth/google" {
		t.Fatalf("POST delete location = %q", loc)
	}

	// A code that is not 7 base62 characters still requires a session first.
	short := postForm(f.h(), "/links/ab/usun", nil)
	status(t, short, http.StatusSeeOther)
	if loc := short.Header().Get("Location"); loc != "/auth/google" {
		t.Fatalf("POST short delete location = %q", loc)
	}
	if f.count(t) != before {
		t.Fatalf("rows = %d, want %d", f.count(t), before)
	}
}

func TestCreateResolveAndRepeatURL(t *testing.T) {
	f := newFixture(t)
	user := f.session(t, "user-sub", "user@example.com")
	target := "https://example.com/a?b=1#f"

	var logBuf bytes.Buffer
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	first := postForm(f.h(), "/links", url.Values{"url": {target}}, user)
	status(t, first, http.StatusSeeOther)
	code1 := nowyCode(t, first.Header().Get("Location"))
	if !strings.Contains(logBuf.String(), code1) || !strings.Contains(logBuf.String(), "user-sub") {
		t.Fatalf("create log = %q, want code and sub", logBuf.String())
	}
	if strings.Contains(logBuf.String(), target) {
		t.Fatalf("create log contains target: %s", logBuf.String())
	}

	rec := get(f.h(), "/"+code1)
	status(t, rec, http.StatusFound)
	if got := rec.Header().Get("Location"); got != target {
		t.Fatalf("location = %q, want %q", got, target)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache-control = %q", got)
	}
	if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("referrer-policy = %q", got)
	}

	home := get(f.h(), "/?nowy="+code1, user)
	status(t, home, http.StatusOK)
	assertHTML(t, home)
	body := home.Body.String()
	short := publicBase + "/" + code1
	mustContain(t, body, `id="nowy"`)
	mustContain(t, body, short)
	mustContain(t, body, wantButton)

	// Leading and trailing spaces are not part of the stored target.
	spaced := postForm(f.h(), "/links", url.Values{"url": {"  https://example.com/spaced  "}}, user)
	status(t, spaced, http.StatusSeeOther)
	codeSpaced := nowyCode(t, spaced.Header().Get("Location"))
	gotSpaced := get(f.h(), "/"+codeSpaced)
	if loc := gotSpaced.Header().Get("Location"); loc != "https://example.com/spaced" {
		t.Fatalf("trimmed location = %q", loc)
	}

	second := postForm(f.h(), "/links", url.Values{"url": {target}}, user)
	status(t, second, http.StatusSeeOther)
	code2 := nowyCode(t, second.Header().Get("Location"))
	if code2 == code1 {
		t.Fatal("same URL reused one code")
	}
	again := get(f.h(), "/"+code2)
	if again.Code != http.StatusFound || again.Header().Get("Location") != target {
		t.Fatalf("second resolve = %d %q", again.Code, again.Header().Get("Location"))
	}
}

func TestBadURLDoesNotInsert(t *testing.T) {
	f := newFixture(t)
	user := f.session(t, "user-sub", "user@example.com")
	before := f.count(t)
	for _, raw := range []string{"", "javascript:alert(1)", "http://127.0.0.1/secret", "notaurl"} {
		rec := postForm(f.h(), "/links", url.Values{"url": {raw}}, user)
		status(t, rec, http.StatusUnprocessableEntity)
		assertHTML(t, rec)
		body := rec.Body.String()
		mustContain(t, body, wantBadURL)
		mustContain(t, body, wantButton)
	}
	if got := f.count(t); got != before {
		t.Fatalf("rows = %d, want %d", got, before)
	}
}

func TestRateLimit(t *testing.T) {
	f := newFixture(t)
	if err := f.st.SaveSettings(context.Background(), 90, 1, f.clk.Now()); err != nil {
		t.Fatal(err)
	}
	user := f.session(t, "user-sub", "user@example.com")
	ok := postForm(f.h(), "/links", url.Values{"url": {"https://example.com/one"}}, user)
	status(t, ok, http.StatusSeeOther)
	before := f.count(t)
	if before != 1 {
		t.Fatalf("rows after first create = %d", before)
	}
	rec := postForm(f.h(), "/links", url.Values{"url": {"https://example.com/two"}}, user)
	status(t, rec, http.StatusTooManyRequests)
	assertHTML(t, rec)
	body := rec.Body.String()
	mustContain(t, body, wantRate)
	mustContain(t, body, wantButton)
	if got := f.count(t); got != before {
		t.Fatalf("rows = %d, want %d", got, before)
	}
}

func TestDeadCodeSameBody(t *testing.T) {
	f := newFixture(t)
	old := f.clk.Now().Add(-91 * 24 * time.Hour)
	expired, err := f.st.Create(context.Background(), "user-sub", "user@example.com", "https://example.com/old", old)
	if err != nil {
		t.Fatal(err)
	}
	unknownCode := "zzzzzzz"
	if unknownCode == expired {
		unknownCode = "yyyyyyy"
	}

	unknown := get(f.h(), "/"+unknownCode)
	justExpired := get(f.h(), "/"+expired)
	status(t, unknown, http.StatusNotFound)
	status(t, justExpired, http.StatusNotFound)
	assertHTML(t, unknown)
	assertHTML(t, justExpired)
	if unknown.Body.String() != justExpired.Body.String() {
		t.Fatalf("bodies differ\nunknown:\n%s\nexpired:\n%s", unknown.Body.String(), justExpired.Body.String())
	}
	body := unknown.Body.String()
	mustContain(t, body, wantDead)
	mustContain(t, body, `href="/"`)
	mustNotContain(t, body, wantUnknown)
	if unknown.Header().Get("Cache-Control") != "no-store" || justExpired.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("dead code responses must be no-store")
	}
	if f.count(t) != 0 {
		t.Fatal("expired row was kept")
	}
}

func TestBlockedTargetIsNotRedirect(t *testing.T) {
	f := newFixture(t)
	now := f.clk.Now()
	blocked, err := f.st.Create(context.Background(), "user-sub", "user@example.com", "http://127.0.0.1/secret", now)
	if err != nil {
		t.Fatal(err)
	}
	script, err := f.st.Create(context.Background(), "user-sub", "user@example.com", "javascript:alert(1)", now)
	if err != nil {
		t.Fatal(err)
	}
	unknown := get(f.h(), "/zzzzzzz")
	for _, code := range []string{blocked, script} {
		rec := get(f.h(), "/"+code)
		status(t, rec, http.StatusNotFound)
		if rec.Header().Get("Location") != "" {
			t.Fatalf("blocked %s redirected to %q", code, rec.Header().Get("Location"))
		}
		if rec.Body.String() != unknown.Body.String() {
			t.Fatalf("blocked body differs for %s", code)
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("missing no-store")
		}
	}
	if f.count(t) != 2 {
		t.Fatalf("blocked rows = %d, want 2", f.count(t))
	}
}

func TestHealthAndNonCodePaths(t *testing.T) {
	f := newFixture(t)
	health := get(f.h(), "/health")
	status(t, health, http.StatusOK)
	if health.Body.String() != "ok" {
		t.Fatalf("health body = %q", health.Body.String())
	}

	// Neighbors of /health, and other single segments that are not 7 base62
	// characters, are unknown pages and are not looked up as short codes.
	for _, path := range []string{"/healt", "/health/", "/health.", "/HEALTH", "/abcdef", "/abcdefgh", "/abc-def", "/links"} {
		rec := get(f.h(), path)
		status(t, rec, http.StatusNotFound)
		assertHTML(t, rec)
		body := rec.Body.String()
		mustContain(t, body, wantUnknown)
		mustNotContain(t, body, wantDead)
	}

	// Seven base62 characters are a code lookup, even next to /health.
	missing := get(f.h(), "/healthy")
	status(t, missing, http.StatusNotFound)
	mustContain(t, missing.Body.String(), wantDead)
	mustNotContain(t, missing.Body.String(), wantUnknown)
}

func TestRegisteredRoutesAreNotCodes(t *testing.T) {
	if len(routes) == 0 {
		t.Fatal("no registered routes")
	}
	seen := map[string]bool{}
	for _, rt := range routes {
		seen[rt.method+" "+rt.path] = true
		if !strings.HasPrefix(rt.path, "/") {
			t.Errorf("route %s %q does not start with /", rt.method, rt.path)
		}
		seg := strings.TrimPrefix(rt.path, "/")
		if seg == "" || strings.Contains(seg, "/") {
			continue
		}
		if shortcode.Valid(seg) {
			t.Errorf("single-segment route %s %s matches ^[0-9A-Za-z]{7}$", rt.method, rt.path)
		}
	}
	for _, want := range []string{
		"GET /",
		"POST /links",
		"GET /wszystkie",
		"GET /ustawienia",
		"POST /ustawienia",
		"GET /auth/google",
		"GET /auth/callback",
		"POST /logout",
		"GET /health",
		"GET /style.css",
	} {
		if !seen[want] {
			t.Errorf("missing registered route %s", want)
		}
	}

	f := newFixture(t)
	for _, rt := range routes {
		req := httptest.NewRequest(rt.method, rt.path, nil)
		rec := httptest.NewRecorder()
		f.h().ServeHTTP(rec, req)
		if strings.Contains(rec.Body.String(), wantDead) {
			t.Errorf("%s %s was handled as a short code", rt.method, rt.path)
		}
	}
}

func TestSettingsHiddenFromNonOwner(t *testing.T) {
	f := newFixture(t)
	outsider := f.session(t, "other-sub", "other@example.com")
	for _, who := range []struct {
		name   string
		cookie *http.Cookie
	}{
		{name: "anonymous", cookie: nil},
		{name: "non-owner", cookie: outsider},
	} {
		t.Run(who.name, func(t *testing.T) {
			for _, c := range []struct {
				method, path string
			}{
				{http.MethodGet, "/ustawienia"},
				{http.MethodPost, "/ustawienia"},
				{http.MethodGet, "/wszystkie"},
				{http.MethodPost, "/wszystkie"},
			} {
				req := httptest.NewRequest(c.method, c.path, strings.NewReader("retention_days=1&max_creates_per_hour=1"))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				if who.cookie != nil {
					req.AddCookie(who.cookie)
				}
				rec := httptest.NewRecorder()
				f.h().ServeHTTP(rec, req)
				status(t, rec, http.StatusNotFound)
				assertHTML(t, rec)
				body := rec.Body.String()
				mustContain(t, body, wantUnknown)
				mustNotContain(t, body, "dni retencji")
				mustNotContain(t, body, wantEmptyAll)
				mustNotContain(t, body, `href="/ustawienia"`)
				mustNotContain(t, body, `href="/wszystkie"`)
			}
		})
	}

	// Owner matching ignores email case.
	owner := f.session(t, "owner-sub", "Owner@Example.com")
	page := get(f.h(), "/ustawienia", owner)
	status(t, page, http.StatusOK)
	mustContain(t, page.Body.String(), "dni retencji")
	all := get(f.h(), "/wszystkie", owner)
	status(t, all, http.StatusOK)
	mustContain(t, all.Body.String(), wantEmptyAll)
}

func TestSettingsSaveAndReject(t *testing.T) {
	f := newFixture(t)
	owner := f.session(t, "owner-sub", ownerEmail)
	form := get(f.h(), "/ustawienia", owner)
	status(t, form, http.StatusOK)
	assertHTML(t, form)
	mustContain(t, form.Body.String(), wantSettingsNote)
	mustContain(t, form.Body.String(), "Zapisz")
	mustNotContain(t, form.Body.String(), wantSaved)
	mustContain(t, form.Body.String(), `href="/wszystkie"`)
	mustContain(t, form.Body.String(), ">Wszystkie<")
	mustContain(t, form.Body.String(), `href="/ustawienia"`)
	mustContain(t, form.Body.String(), ">Ustawienia<")

	ok := postForm(f.h(), "/ustawienia", url.Values{
		"retention_days":       {"10"},
		"max_creates_per_hour": {"5"},
	}, owner)
	status(t, ok, http.StatusSeeOther)
	if loc := ok.Header().Get("Location"); loc != "/ustawienia?zapisane=1" {
		t.Fatalf("location = %q", loc)
	}
	saved := get(f.h(), ok.Header().Get("Location"), owner)
	status(t, saved, http.StatusOK)
	mustContain(t, saved.Body.String(), wantSaved)
	mustContain(t, saved.Body.String(), `value="10"`)
	mustContain(t, saved.Body.String(), `value="5"`)
	f.wantSettings(t, 10, 5)

	for _, v := range []url.Values{
		{"retention_days": {"0"}, "max_creates_per_hour": {"5"}},
		{"retention_days": {"10"}, "max_creates_per_hour": {"0"}},
		{"retention_days": {"3651"}, "max_creates_per_hour": {"5"}},
		{"retention_days": {"10"}, "max_creates_per_hour": {"1001"}},
		{"retention_days": {"abc"}, "max_creates_per_hour": {"5"}},
		{"retention_days": {"10"}, "max_creates_per_hour": {"1.5"}},
		{"retention_days": {""}, "max_creates_per_hour": {"5"}},
	} {
		rec := postForm(f.h(), "/ustawienia", v, owner)
		status(t, rec, http.StatusUnprocessableEntity)
		assertHTML(t, rec)
		body := rec.Body.String()
		mustContain(t, body, wantBadSettings)
		mustContain(t, body, `value="10"`)
		mustContain(t, body, `value="5"`)
		rejected := v.Get("retention_days")
		if rejected != "10" && rejected != "" {
			mustNotContain(t, body, `value="`+rejected+`"`)
		}
		f.wantSettings(t, 10, 5)
	}

	top := postForm(f.h(), "/ustawienia", url.Values{
		"retention_days":       {"3650"},
		"max_creates_per_hour": {"1000"},
	}, owner)
	status(t, top, http.StatusSeeOther)
	f.wantSettings(t, 3650, 1000)

	floor := postForm(f.h(), "/ustawienia", url.Values{
		"retention_days":       {"1"},
		"max_creates_per_hour": {"1"},
	}, owner)
	status(t, floor, http.StatusSeeOther)
	f.wantSettings(t, 1, 1)
}

func TestSettingsSaveSweepsExpired(t *testing.T) {
	f := newFixture(t)
	owner := f.session(t, "owner-sub", ownerEmail)
	old := f.mustCreate(t, "owner-sub", ownerEmail, "https://example.com/old", f.clk.Now().Add(-48*time.Hour))
	fresh := f.mustCreate(t, "owner-sub", ownerEmail, "https://example.com/new", f.clk.Now())
	rec := postForm(f.h(), "/ustawienia", url.Values{
		"retention_days":       {"1"},
		"max_creates_per_hour": {"30"},
	}, owner)
	status(t, rec, http.StatusSeeOther)
	rows, err := f.st.ListAll(context.Background(), 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Code != fresh {
		t.Fatalf("rows after shorter retention = %+v, old %s should be gone", rows, old)
	}
}

func TestDeleteRulesAndNext(t *testing.T) {
	f := newFixture(t)
	author := f.session(t, "author-sub", "author@example.com")
	other := f.session(t, "other-sub", "other@example.com")
	owner := f.session(t, "owner-sub", ownerEmail)

	own := f.mustCreate(t, "author-sub", "author@example.com", "https://example.com/own", f.clk.Now())
	foreign := f.mustCreate(t, "other-sub", "other@example.com", "https://example.com/foreign", f.clk.Now())
	any := f.mustCreate(t, "other-sub", "other@example.com", "https://example.com/any", f.clk.Now())

	home := get(f.h(), "/", author)
	mustContain(t, home.Body.String(), `name="next" value="/"`)
	mustContain(t, home.Body.String(), wantButton)
	mustContain(t, home.Body.String(), "Usuń")
	mustNotContain(t, home.Body.String(), `href="/wszystkie"`)

	all := get(f.h(), "/wszystkie", owner)
	mustContain(t, all.Body.String(), `name="next" value="/wszystkie"`)
	mustContain(t, all.Body.String(), "other@example.com")

	denied := postForm(f.h(), "/links/"+foreign+"/usun", url.Values{"next": {"/"}}, author)
	status(t, denied, http.StatusNotFound)
	mustContain(t, denied.Body.String(), wantUnknown)
	if !f.has(t, foreign) {
		t.Fatal("foreign row was deleted")
	}

	badNext := postForm(f.h(), "/links/"+own+"/usun", url.Values{"next": {"https://evil.example/phish"}}, author)
	status(t, badNext, http.StatusSeeOther)
	if loc := badNext.Header().Get("Location"); loc != "/" {
		t.Fatalf("bad next location = %q", loc)
	}
	if f.has(t, own) {
		t.Fatal("author delete kept the row")
	}

	// Non-owner cannot bounce a delete onto the owner list.
	again := f.mustCreate(t, "author-sub", "author@example.com", "https://example.com/again", f.clk.Now())
	notOwnerNext := postForm(f.h(), "/links/"+again+"/usun", url.Values{"next": {"/wszystkie"}}, author)
	status(t, notOwnerNext, http.StatusSeeOther)
	if loc := notOwnerNext.Header().Get("Location"); loc != "/" {
		t.Fatalf("non-owner /wszystkie next = %q", loc)
	}

	for _, next := range []string{"//evil.example", "/ustawienia", "/wszystkie/../", "/wszystkie?strona=2", "wszystkie", ""} {
		code := f.mustCreate(t, "author-sub", "author@example.com", "https://example.com/n", f.clk.Now())
		rec := postForm(f.h(), "/links/"+code+"/usun", url.Values{"next": {next}}, author)
		status(t, rec, http.StatusSeeOther)
		if loc := rec.Header().Get("Location"); loc != "/" {
			t.Fatalf("next %q location = %q", next, loc)
		}
	}

	ownerDel := postForm(f.h(), "/links/"+any+"/usun", url.Values{"next": {"/wszystkie"}}, owner)
	status(t, ownerDel, http.StatusSeeOther)
	if loc := ownerDel.Header().Get("Location"); loc != "/wszystkie" {
		t.Fatalf("owner next location = %q", loc)
	}
	if f.has(t, any) {
		t.Fatal("owner delete kept the row")
	}

	// A second click on a missing code is not an error.
	missing := postForm(f.h(), "/links/"+any+"/usun", url.Values{"next": {"/"}}, owner)
	status(t, missing, http.StatusSeeOther)
	if loc := missing.Header().Get("Location"); loc != "/" {
		t.Fatalf("missing delete location = %q", loc)
	}

	badCode := postForm(f.h(), "/links/ab/usun", url.Values{"next": {"/"}}, other)
	status(t, badCode, http.StatusNotFound)
	mustContain(t, badCode.Body.String(), wantUnknown)
	mustNotContain(t, badCode.Body.String(), wantDead)

	empty := get(f.h(), "/", author)
	mustContain(t, empty.Body.String(), wantEmptyMine)
}

func TestOAuthFake(t *testing.T) {
	f := newFixture(t)
	start := get(f.h(), "/auth/google")
	status(t, start, http.StatusFound)
	state := cookieNamed(t, start, "szort_oauth_state")
	if state == nil || state.Value == "" {
		t.Fatal("missing state cookie")
	}
	loc, err := url.Parse(start.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if loc.Scheme != "https" || loc.Host != "accounts.google.com" {
		t.Fatalf("auth location = %q", start.Header().Get("Location"))
	}
	q := loc.Query()
	if q.Get("state") != state.Value {
		t.Fatalf("state query = %q, cookie %q", q.Get("state"), state.Value)
	}
	if q.Get("client_id") != "client-id" {
		t.Fatalf("client_id = %q", q.Get("client_id"))
	}
	if q.Get("redirect_uri") != publicBase+"/auth/callback" {
		t.Fatalf("redirect_uri = %q", q.Get("redirect_uri"))
	}
	if q.Get("response_type") != "code" {
		t.Fatalf("response_type = %q", q.Get("response_type"))
	}
	scope := q.Get("scope")
	for _, want := range []string{"openid", "email", "profile"} {
		if !strings.Contains(scope, want) {
			t.Fatalf("scope %q missing %s", scope, want)
		}
	}
	if f.fx.calls != 0 {
		t.Fatal("start dialed the exchanger")
	}

	var logBuf bytes.Buffer
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	bad := get(f.h(), "/auth/callback?state=leaked-state&code=leaked-auth-code", state)
	status(t, bad, http.StatusBadRequest)
	assertHTML(t, bad)
	mustContain(t, bad.Body.String(), wantOAuth)
	if cookieNamed(t, bad, "szort_session") != nil {
		t.Fatal("bad state set a session")
	}
	if f.fx.calls != 0 {
		t.Fatal("bad state called Exchange")
	}
	if strings.Contains(logBuf.String(), "leaked-state") || strings.Contains(logBuf.String(), "leaked-auth-code") {
		t.Fatalf("oauth log = %q", logBuf.String())
	}

	f.fx.id = Identity{Sub: "sub-1", Email: "person@example.com", EmailVerified: false}
	unverified := get(f.h(), "/auth/callback?state="+url.QueryEscape(state.Value)+"&code=abc", state)
	status(t, unverified, http.StatusBadRequest)
	mustContain(t, unverified.Body.String(), wantOAuth)
	if cookieNamed(t, unverified, "szort_session") != nil {
		t.Fatal("unverified email set a session")
	}
	if f.fx.calls != 1 {
		t.Fatalf("exchange calls = %d", f.fx.calls)
	}

	f.fx.id = Identity{Sub: "", Email: "person@example.com", EmailVerified: true}
	noSub := get(f.h(), "/auth/callback?state="+url.QueryEscape(state.Value)+"&code=abc", state)
	status(t, noSub, http.StatusBadRequest)
	if cookieNamed(t, noSub, "szort_session") != nil {
		t.Fatal("empty sub set a session")
	}

	f.fx.id = Identity{Sub: "sub-1", Email: "", EmailVerified: true}
	noEmail := get(f.h(), "/auth/callback?state="+url.QueryEscape(state.Value)+"&code=abc", state)
	status(t, noEmail, http.StatusBadRequest)
	if cookieNamed(t, noEmail, "szort_session") != nil {
		t.Fatal("empty email set a session")
	}

	f.fx.err = context.Canceled
	f.fx.id = Identity{Sub: "sub-1", Email: "person@example.com", EmailVerified: true}
	failed := get(f.h(), "/auth/callback?state="+url.QueryEscape(state.Value)+"&code=abc", state)
	status(t, failed, http.StatusBadRequest)
	if cookieNamed(t, failed, "szort_session") != nil {
		t.Fatal("exchange error set a session")
	}
	f.fx.err = nil

	missingCode := get(f.h(), "/auth/callback?state="+url.QueryEscape(state.Value), state)
	status(t, missingCode, http.StatusBadRequest)
	callsAfterMissing := f.fx.calls

	f.fx.id = Identity{Sub: "sub-1", Email: "person@example.com", EmailVerified: true}
	ok := get(f.h(), "/auth/callback?state="+url.QueryEscape(state.Value)+"&code=good-code", state)
	status(t, ok, http.StatusSeeOther)
	if loc := ok.Header().Get("Location"); loc != "/" {
		t.Fatalf("callback location = %q", loc)
	}
	sess := cookieNamed(t, ok, "szort_session")
	if sess == nil || sess.Value == "" {
		t.Fatal("verified identity did not set a session")
	}
	if f.fx.calls != callsAfterMissing+1 || f.fx.last != "good-code" {
		t.Fatalf("exchange calls = %d last %q", f.fx.calls, f.fx.last)
	}
	home := get(f.h(), "/", sess)
	status(t, home, http.StatusOK)
	mustContain(t, home.Body.String(), "person@example.com")
	mustContain(t, home.Body.String(), wantButton)
	mustContain(t, home.Body.String(), "Wyloguj")
	mustContain(t, home.Body.String(), wantEmptyMine)
}

func TestDaysLeft(t *testing.T) {
	f := newFixture(t)
	user := f.session(t, "user-sub", "user@example.com")
	start := f.clk.Now()
	if err := f.st.SaveSettings(context.Background(), 10, 30, start); err != nil {
		t.Fatal(err)
	}
	code := f.mustCreate(t, "user-sub", "user@example.com", "https://example.com/d", start)

	body := get(f.h(), "/", user).Body.String()
	mustContain(t, body, `<span class="days">10</span>`)

	f.clk.t = start.Add(time.Second)
	body = get(f.h(), "/", user).Body.String()
	mustContain(t, body, `<span class="days">9</span>`)

	if err := f.st.SaveSettings(context.Background(), 3, 30, f.clk.Now()); err != nil {
		t.Fatal(err)
	}
	body = get(f.h(), "/", user).Body.String()
	mustContain(t, body, `<span class="days">2</span>`)

	if err := f.st.SaveSettings(context.Background(), 10, 30, f.clk.Now()); err != nil {
		t.Fatal(err)
	}
	f.clk.t = start.Add(9*24*time.Hour + time.Second)
	body = get(f.h(), "/", user).Body.String()
	mustContain(t, body, `<span class="days">0</span>`)
	if !f.has(t, code) {
		t.Fatal("row disappeared before expiry")
	}

	f.clk.t = start.Add(10*24*time.Hour + time.Second)
	body = get(f.h(), "/", user).Body.String()
	mustContain(t, body, `<span class="days">0</span>`)
	rec := get(f.h(), "/"+code)
	status(t, rec, http.StatusNotFound)
	mustContain(t, rec.Body.String(), wantDead)
	if f.has(t, code) {
		t.Fatal("expired resolve kept the row")
	}
}

func TestNewLinkOnlyForOwner(t *testing.T) {
	f := newFixture(t)
	owner := f.session(t, "user-sub", "user@example.com")
	other := f.session(t, "other-sub", "other@example.com")
	code := f.mustCreate(t, "user-sub", "user@example.com", "https://example.com/mine", f.clk.Now())
	short := publicBase + "/" + code

	mine := get(f.h(), "/?nowy="+code, owner)
	mustContain(t, mine.Body.String(), `id="nowy"`)
	mustContain(t, mine.Body.String(), `value="`+short+`"`)

	theirs := get(f.h(), "/?nowy="+code, other)
	mustNotContain(t, theirs.Body.String(), `id="nowy"`)
	mustNotContain(t, theirs.Body.String(), short)

	anon := get(f.h(), "/?nowy="+code)
	mustNotContain(t, anon.Body.String(), `id="nowy"`)
	mustNotContain(t, anon.Body.String(), short)
	mustNotContain(t, anon.Body.String(), wantButton)

	missing := get(f.h(), "/?nowy=zzzzzzz", owner)
	mustNotContain(t, missing.Body.String(), `id="nowy"`)

	odd := get(f.h(), "/?nowy=not-a-code", owner)
	mustNotContain(t, odd.Body.String(), `id="nowy"`)

	blocked := f.mustCreate(t, "user-sub", "user@example.com", "http://10.1.2.3/x", f.clk.Now())
	blockedPage := get(f.h(), "/?nowy="+blocked, owner)
	mustNotContain(t, blockedPage.Body.String(), `id="nowy"`)
	if !f.has(t, blocked) {
		t.Fatal("blocked row was deleted while checking nowy")
	}

	expired := f.mustCreate(t, "user-sub", "user@example.com", "https://example.com/old", f.clk.Now().Add(-91*24*time.Hour))
	expiredPage := get(f.h(), "/?nowy="+expired, owner)
	mustNotContain(t, expiredPage.Body.String(), `id="nowy"`)
}

func TestPager(t *testing.T) {
	f := newFixture(t)
	if err := f.st.SaveSettings(context.Background(), 90, 1000, f.clk.Now()); err != nil {
		t.Fatal(err)
	}
	owner := f.session(t, "owner-sub", ownerEmail)
	const authorSub = "owner-sub"
	for i := 0; i < 199; i++ {
		f.mustCreate(t, authorSub, ownerEmail, "https://example.com/x", f.clk.Now())
	}
	page := get(f.h(), "/", owner)
	mustNotContain(t, page.Body.String(), "strona=")

	f.mustCreate(t, authorSub, ownerEmail, "https://example.com/x", f.clk.Now())
	full := get(f.h(), "/", owner)
	mustContain(t, full.Body.String(), `href="/?strona=2"`)
	mustContain(t, full.Body.String(), "Starsze")
	secondEmpty := get(f.h(), "/?strona=2", owner)
	mustContain(t, secondEmpty.Body.String(), `href="/?strona=1"`)
	mustContain(t, secondEmpty.Body.String(), "Nowsze")
	mustNotContain(t, secondEmpty.Body.String(), "strona=3")
	mustNotContain(t, secondEmpty.Body.String(), wantEmptyMine)

	f.mustCreate(t, authorSub, ownerEmail, "https://example.com/x", f.clk.Now())
	rows, err := f.st.ListByAuthor(context.Background(), authorSub, 1, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("store page 2 = %d rows", len(rows))
	}
	later := get(f.h(), "/?strona=2", owner)
	mustContain(t, later.Body.String(), publicBase+"/"+rows[0].Code)
	mustContain(t, later.Body.String(), `href="/?strona=1"`)
	mustNotContain(t, later.Body.String(), "strona=3")

	zero := get(f.h(), "/?strona=0", owner)
	mustContain(t, zero.Body.String(), `href="/?strona=2"`)
	junk := get(f.h(), "/?strona=abc", owner)
	mustContain(t, junk.Body.String(), `href="/?strona=2"`)

	all := get(f.h(), "/wszystkie", owner)
	mustContain(t, all.Body.String(), `href="/wszystkie?strona=2"`)
	all2 := get(f.h(), "/wszystkie?strona=2", owner)
	mustContain(t, all2.Body.String(), publicBase+"/"+rows[0].Code)
	mustContain(t, all2.Body.String(), `href="/wszystkie?strona=1"`)
}

func TestLogout(t *testing.T) {
	f := newFixture(t)
	user := f.session(t, "user-sub", "user@example.com")
	rec := postForm(f.h(), "/logout", nil, user)
	status(t, rec, http.StatusSeeOther)
	if loc := rec.Header().Get("Location"); loc != "/" {
		t.Fatalf("location = %q", loc)
	}
	cleared := cookieNamed(t, rec, "szort_session")
	if cleared == nil || cleared.MaxAge >= 0 {
		t.Fatal("session cookie was not cleared")
	}
	anon := postForm(f.h(), "/logout", nil)
	status(t, anon, http.StatusSeeOther)
	if loc := anon.Header().Get("Location"); loc != "/" {
		t.Fatalf("anonymous logout location = %q", loc)
	}
}

func TestExpiredSessionIsAnonymous(t *testing.T) {
	f := newFixture(t)
	rec := httptest.NewRecorder()
	f.app.sessions.Set(rec, session.User{
		Sub:   "user-sub",
		Email: "user@example.com",
		Exp:   time.Now().Add(-time.Hour),
	})
	c := cookieNamed(t, rec, "szort_session")
	if c == nil {
		t.Fatal("missing cookie")
	}
	got := postForm(f.h(), "/links", url.Values{"url": {"https://example.com/a"}}, c)
	status(t, got, http.StatusSeeOther)
	if loc := got.Header().Get("Location"); loc != "/auth/google" {
		t.Fatalf("location = %q", loc)
	}
	if f.count(t) != 0 {
		t.Fatal("expired session created a row")
	}
}

func TestStyleCSS(t *testing.T) {
	f := newFixture(t)
	rec := get(f.h(), "/style.css")
	status(t, rec, http.StatusOK)
	if ct := rec.Header().Get("Content-Type"); ct != "text/css; charset=utf-8" {
		t.Fatalf("content-type = %q", ct)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing nosniff")
	}
	if !bytes.Equal(rec.Body.Bytes(), f.app.css) {
		t.Fatal("css body is not the embedded file")
	}
	if strings.Contains(rec.Body.String(), "http://") || strings.Contains(rec.Body.String(), "https://") {
		t.Fatal("css references an external URL")
	}
	home := get(f.h(), "/")
	mustContain(t, home.Body.String(), `href="/style.css?v=2"`)
	mustContain(t, home.Body.String(), "<title>Szorty</title>")
	mustContain(t, home.Body.String(), `>Szorty</a>`)
}

type fakeClock struct {
	t time.Time
}

func (f *fakeClock) Now() time.Time { return f.t }

type fakeExchanger struct {
	id    Identity
	err   error
	calls int
	last  string
}

func (f *fakeExchanger) Exchange(ctx context.Context, code string) (Identity, error) {
	f.calls++
	f.last = code
	if f.err != nil {
		return Identity{}, f.err
	}
	return f.id, nil
}

var _ Exchanger = (*fakeExchanger)(nil)

type fixture struct {
	app *App
	st  *store.Store
	clk *fakeClock
	fx  *fakeExchanger
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "szort.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	// Session cookies are judged by time.Now in the session package, so the
	// app clock stays near the wall clock. Link expiry moves this clock.
	clk := &fakeClock{t: time.Now().UTC().Truncate(time.Second)}
	fx := &fakeExchanger{}
	cfg := config.Config{
		GoogleClientID:     "client-id",
		GoogleClientSecret: "client-secret",
		OwnerEmail:         ownerEmail,
		PublicBaseURL:      publicBase,
		DBPath:             "unused",
		ListenAddr:         ":8080",
		SessionSecret:      []byte(strings.Repeat("s", 32)),
	}
	return &fixture{app: New(cfg, st, clk, fx), st: st, clk: clk, fx: fx}
}

func (f *fixture) h() http.Handler { return f.app.Handler() }

func (f *fixture) session(t *testing.T, sub, email string) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	f.app.sessions.Set(rec, session.User{
		Sub:   sub,
		Email: email,
		Exp:   time.Now().Add(14 * 24 * time.Hour),
	})
	c := cookieNamed(t, rec, "szort_session")
	if c == nil || c.Value == "" {
		t.Fatal("missing session cookie")
	}
	return c
}

func (f *fixture) count(t *testing.T) int {
	t.Helper()
	n := 0
	for offset := 0; ; offset += 200 {
		rows, err := f.st.ListAll(context.Background(), 200, offset)
		if err != nil {
			t.Fatal(err)
		}
		n += len(rows)
		if len(rows) < 200 {
			return n
		}
	}
}

func (f *fixture) has(t *testing.T, code string) bool {
	t.Helper()
	rows, err := f.st.ListAll(context.Background(), 500, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Code == code {
			return true
		}
	}
	return false
}

func (f *fixture) mustCreate(t *testing.T, sub, email, target string, at time.Time) string {
	t.Helper()
	code, err := f.st.Create(context.Background(), sub, email, target, at)
	if err != nil {
		t.Fatal(err)
	}
	return code
}

func (f *fixture) wantSettings(t *testing.T, days, limit int) {
	t.Helper()
	got, err := f.st.GetSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.RetentionDays != days || got.MaxCreatesPerHour != limit {
		t.Fatalf("settings = %d days, %d/hour, want %d and %d", got.RetentionDays, got.MaxCreatesPerHour, days, limit)
	}
}

func get(h http.Handler, path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for _, c := range cookies {
		if c != nil {
			req.AddCookie(c)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func postForm(h http.Handler, path string, v url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	body := ""
	if v != nil {
		body = v.Encode()
	}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range cookies {
		if c != nil {
			req.AddCookie(c)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func cookieNamed(t *testing.T, rec *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	res := rec.Result()
	t.Cleanup(func() { res.Body.Close() })
	for _, c := range res.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func nowyCode(t *testing.T, location string) string {
	t.Helper()
	u, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	if u.Path != "/" {
		t.Fatalf("create location path = %q", location)
	}
	code := u.Query().Get("nowy")
	if !shortcode.Valid(code) {
		t.Fatalf("nowy = %q", code)
	}
	return code
}

func status(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d, location %q, body %s", rec.Code, want, rec.Header().Get("Location"), snippet(rec.Body.String()))
	}
}

func assertHTML(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("content-type = %q", ct)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("x-content-type-options = %q", got)
	}
	if got := rec.Header().Get("Content-Security-Policy"); got != wantCSP {
		t.Fatalf("csp = %q", got)
	}
	if strings.Contains(strings.ToLower(rec.Body.String()), "<script") {
		t.Fatal("response contains a script tag")
	}
}

func mustContain(t *testing.T, body, sub string) {
	t.Helper()
	if !strings.Contains(body, sub) {
		t.Fatalf("body missing %q\n%s", sub, snippet(body))
	}
}

func mustNotContain(t *testing.T, body, sub string) {
	t.Helper()
	if strings.Contains(body, sub) {
		t.Fatalf("body contains %q\n%s", sub, snippet(body))
	}
}

func snippet(s string) string {
	if len(s) > 1500 {
		return s[:1500] + "...(truncated)"
	}
	return s
}
