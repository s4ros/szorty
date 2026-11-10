package session

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	sessionCookie = "szort_session"
	stateCookie   = "szort_oauth_state"

	sessionLifetime = 14 * 24 * time.Hour
	stateLifetime   = 10 * time.Minute
)

// User is the signed session payload.
type User struct {
	Sub, Email string
	Exp        time.Time
}

// Manager signs szort_session and writes the OAuth state cookie.
// Secure is set when publicBaseURL uses https.
type Manager struct {
	secret     []byte
	ownerEmail string
	secure     bool
}

type payload struct {
	Sub   string    `json:"sub"`
	Email string    `json:"email"`
	Exp   time.Time `json:"exp"`
}

// NewManager copies secret and treats publicBaseURL as https when its scheme is https.
func NewManager(secret []byte, ownerEmail, publicBaseURL string) *Manager {
	copied := make([]byte, len(secret))
	copy(copied, secret)
	return &Manager{
		secret:     copied,
		ownerEmail: ownerEmail,
		secure:     httpsBase(publicBaseURL),
	}
}

// Set writes szort_session for 14 days. Exp in the payload is the caller's.
func (m *Manager) Set(w http.ResponseWriter, u User) {
	token, err := m.seal(payload{Sub: u.Sub, Email: u.Email, Exp: u.Exp})
	if err != nil {
		return
	}
	http.SetCookie(w, m.cookie(sessionCookie, token, sessionLifetime))
}

// Clear expires szort_session.
func (m *Manager) Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// FromRequest reads szort_session. It reports false when the cookie is missing,
// the HMAC does not match, or exp is not strictly in the future.
func (m *Manager) FromRequest(r *http.Request) (User, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return User{}, false
	}
	var p payload
	if !m.open(c.Value, &p) {
		return User{}, false
	}
	if !p.Exp.After(time.Now()) {
		return User{}, false
	}
	return User{Sub: p.Sub, Email: p.Email, Exp: p.Exp}, true
}

// IsOwner reports whether u.Email matches the owner address, ignoring case.
func (m *Manager) IsOwner(u User) bool {
	return strings.EqualFold(u.Email, m.ownerEmail)
}

// SetState writes szort_oauth_state for 10 minutes.
func (m *Manager) SetState(w http.ResponseWriter, state string) {
	http.SetCookie(w, m.cookie(stateCookie, state, stateLifetime))
}

// State returns the OAuth state cookie value.
func (m *Manager) State(r *http.Request) (string, bool) {
	c, err := r.Cookie(stateCookie)
	if err != nil {
		return "", false
	}
	return c.Value, true
}

func (m *Manager) cookie(name, value string, life time.Duration) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		Expires:  time.Now().UTC().Add(life),
		MaxAge:   int(life / time.Second),
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteLaxMode,
	}
}

func (m *Manager) seal(p payload) (string, error) {
	body, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte(enc))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return enc + "." + sig, nil
}

func (m *Manager) open(token string, dst *payload) bool {
	enc, sigEnc, ok := strings.Cut(token, ".")
	if !ok || enc == "" || sigEnc == "" || strings.Contains(sigEnc, ".") {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigEnc)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte(enc))
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return false
	}
	body, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return false
	}
	return json.Unmarshal(body, dst) == nil
}

func httpsBase(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Scheme, "https")
}
