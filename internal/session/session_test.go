package session

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testSecret() []byte {
	return []byte(strings.Repeat("s", 32))
}

func setCookie(t *testing.T, write func(http.ResponseWriter)) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	write(rec)
	res := rec.Result()
	t.Cleanup(func() { res.Body.Close() })
	cookies := res.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("got %d Set-Cookie headers, want 1", len(cookies))
	}
	return cookies[0]
}

func reqWith(c *http.Cookie) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "https://szorty.infinigy.pl/", nil)
	if c != nil {
		r.AddCookie(c)
	}
	return r
}

func assertCookieFlags(t *testing.T, c *http.Cookie, name string, maxAge int, secure bool) {
	t.Helper()
	if c.Name != name {
		t.Fatalf("name = %q, want %q", c.Name, name)
	}
	if c.Path != "/" {
		t.Fatalf("path = %q, want /", c.Path)
	}
	if c.Domain != "" {
		t.Fatalf("domain = %q, want host-only", c.Domain)
	}
	if !c.HttpOnly {
		t.Fatal("HttpOnly is false")
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("SameSite = %v, want Lax", c.SameSite)
	}
	if c.Secure != secure {
		t.Fatalf("Secure = %v, want %v", c.Secure, secure)
	}
	if c.MaxAge != maxAge {
		t.Fatalf("MaxAge = %d, want %d", c.MaxAge, maxAge)
	}
}

func TestSessionRoundTrip(t *testing.T) {
	secret := testSecret()
	user := User{
		Sub:   "google-sub",
		Email: "person@example.com",
		Exp:   time.Now().UTC().Add(48 * time.Hour),
	}
	m := NewManager(secret, "owner@example.com", "https://szorty.infinigy.pl")
	c := setCookie(t, func(w http.ResponseWriter) { m.Set(w, user) })
	assertCookieFlags(t, c, "szorty_session", 14*24*60*60, true)

	reader := NewManager(append([]byte(nil), secret...), "owner@example.com", "https://szorty.infinigy.pl")
	got, ok := reader.FromRequest(reqWith(c))
	if !ok {
		t.Fatal("FromRequest rejected a cookie signed with the same secret")
	}
	if got.Sub != user.Sub || got.Email != user.Email || !got.Exp.Equal(user.Exp) {
		t.Fatalf("FromRequest = %+v, want %+v", got, user)
	}

	if _, ok := m.FromRequest(reqWith(nil)); ok {
		t.Fatal("FromRequest accepted a request with no cookie")
	}
}

func TestSessionTamper(t *testing.T) {
	m := NewManager(testSecret(), "owner@example.com", "https://szorty.infinigy.pl")
	user := User{Sub: "sub", Email: "person@example.com", Exp: time.Now().UTC().Add(time.Hour)}
	c := setCookie(t, func(w http.ResponseWriter) { m.Set(w, user) })

	flipped := *c
	flipped.Value = flipCookie(c.Value)
	if _, ok := m.FromRequest(reqWith(&flipped)); ok {
		t.Fatal("FromRequest accepted a tampered cookie")
	}

	other := NewManager([]byte(strings.Repeat("o", 32)), "owner@example.com", "https://szorty.infinigy.pl")
	if _, ok := other.FromRequest(reqWith(c)); ok {
		t.Fatal("FromRequest accepted a cookie signed with a different secret")
	}

	bogus := &http.Cookie{Name: "szorty_session", Value: "not-a-signed-cookie"}
	if _, ok := m.FromRequest(reqWith(bogus)); ok {
		t.Fatal("FromRequest accepted an unsigned cookie")
	}
}

func TestSessionExpiry(t *testing.T) {
	m := NewManager(testSecret(), "owner@example.com", "https://szorty.infinigy.pl")
	user := User{
		Sub:   "sub",
		Email: "person@example.com",
		Exp:   time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC),
	}
	c := setCookie(t, func(w http.ResponseWriter) { m.Set(w, user) })
	if _, ok := m.FromRequest(reqWith(c)); ok {
		t.Fatal("FromRequest accepted a cookie whose exp is in the past")
	}
}

func TestIsOwner(t *testing.T) {
	m := NewManager(testSecret(), "Owner@Example.com", "https://szorty.infinigy.pl")
	tests := []struct {
		email string
		want  bool
	}{
		{email: "owner@example.com", want: true},
		{email: "OWNER@EXAMPLE.COM", want: true},
		{email: "Owner@Example.com", want: true},
		{email: "other@example.com", want: false},
		{email: "owner@example.com.evil", want: false},
		{email: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.email, func(t *testing.T) {
			got := m.IsOwner(User{Email: tt.email, Sub: "ignored"})
			if got != tt.want {
				t.Fatalf("IsOwner(%q) = %v, want %v", tt.email, got, tt.want)
			}
		})
	}
}

func TestStateCookie(t *testing.T) {
	m := NewManager(testSecret(), "owner@example.com", "https://szorty.infinigy.pl")
	const state = "oauth-state-token"
	c := setCookie(t, func(w http.ResponseWriter) { m.SetState(w, state) })
	assertCookieFlags(t, c, "szorty_oauth_state", 10*60, true)
	if c.Value != state {
		t.Fatalf("state cookie value = %q, want %q", c.Value, state)
	}

	got, ok := m.State(reqWith(c))
	if !ok || got != state {
		t.Fatalf("State = %q, %v; want %q, true", got, ok, state)
	}
	if _, ok := m.State(reqWith(nil)); ok {
		t.Fatal("State accepted a request with no cookie")
	}

	session := setCookie(t, func(w http.ResponseWriter) {
		m.Set(w, User{Sub: "sub", Email: "person@example.com", Exp: time.Now().UTC().Add(time.Hour)})
	})
	if _, ok := m.State(reqWith(session)); ok {
		t.Fatal("State accepted the session cookie")
	}
	if _, ok := m.FromRequest(reqWith(c)); ok {
		t.Fatal("FromRequest accepted the state cookie")
	}
}

func TestSecureFollowsBaseURL(t *testing.T) {
	tests := []struct {
		base   string
		secure bool
	}{
		{base: "https://szorty.infinigy.pl", secure: true},
		{base: "http://127.0.0.1:8080", secure: false},
		{base: "http://localhost:8080", secure: false},
	}
	user := User{Sub: "sub", Email: "person@example.com", Exp: time.Now().UTC().Add(time.Hour)}
	for _, tt := range tests {
		t.Run(tt.base, func(t *testing.T) {
			m := NewManager(testSecret(), "owner@example.com", tt.base)
			session := setCookie(t, func(w http.ResponseWriter) { m.Set(w, user) })
			assertCookieFlags(t, session, "szorty_session", 14*24*60*60, tt.secure)
			state := setCookie(t, func(w http.ResponseWriter) { m.SetState(w, "state") })
			assertCookieFlags(t, state, "szorty_oauth_state", 10*60, tt.secure)
		})
	}
}

func TestClear(t *testing.T) {
	tests := []struct {
		base   string
		secure bool
	}{
		{base: "https://szorty.infinigy.pl", secure: true},
		{base: "http://localhost:8080", secure: false},
	}
	for _, tt := range tests {
		t.Run(tt.base, func(t *testing.T) {
			m := NewManager(testSecret(), "owner@example.com", tt.base)
			c := setCookie(t, m.Clear)
			if c.Name != "szorty_session" {
				t.Fatalf("name = %q, want szorty_session", c.Name)
			}
			if c.Value != "" {
				t.Fatalf("value = %q, want empty", c.Value)
			}
			if c.Path != "/" {
				t.Fatalf("path = %q, want /", c.Path)
			}
			if c.MaxAge >= 0 {
				t.Fatalf("MaxAge = %d, want a deletion", c.MaxAge)
			}
			if !c.HttpOnly {
				t.Fatal("HttpOnly is false")
			}
			if c.SameSite != http.SameSiteLaxMode {
				t.Fatalf("SameSite = %v, want Lax", c.SameSite)
			}
			if c.Secure != tt.secure {
				t.Fatalf("Secure = %v, want %v", c.Secure, tt.secure)
			}
			if c.Expires.After(time.Now()) {
				t.Fatalf("Expires = %s, still in the future", c.Expires)
			}
		})
	}
}

func flipCookie(value string) string {
	if value == "" {
		return "x"
	}
	b := []byte(value)
	i := len(b) / 2
	b[i] ^= 1
	if string(b) == value {
		b[i] = 'Z'
	}
	return string(b)
}
