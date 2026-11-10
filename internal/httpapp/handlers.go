package httpapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"szorty.infinigy.pl/internal/session"
	"szorty.infinigy.pl/internal/shortcode"
	"szorty.infinigy.pl/internal/store"
	"szorty.infinigy.pl/internal/urlcheck"
)

func (a *App) home(w http.ResponseWriter, r *http.Request) {
	v := a.baseView(r)
	if v.Email == "" {
		a.render(w, http.StatusOK, "home", v)
		return
	}
	u, _ := a.sessions.FromRequest(r)
	now := a.clock.Now()
	if err := a.fillNewLink(r.Context(), &v, u, r.URL.Query().Get("nowy"), now); err != nil {
		log.Printf("home failed")
		a.message(w, r, http.StatusInternalServerError, msgPageFail)
		return
	}
	if err := a.loadLinks(r.Context(), &v, pageFrom(r), now, u.Sub, "/", msgEmptyMine, true); err != nil {
		log.Printf("home failed")
		a.message(w, r, http.StatusInternalServerError, msgPageFail)
		return
	}
	a.render(w, http.StatusOK, "home", v)
}

func (a *App) fillNewLink(ctx context.Context, v *view, u session.User, code string, now time.Time) error {
	if !shortcode.Valid(code) {
		return nil
	}
	owned, err := a.authorHas(ctx, u.Sub, code)
	if err != nil || !owned {
		return err
	}
	_, result, err := a.store.Resolve(ctx, code, now)
	if err != nil {
		return err
	}
	if result == store.ResolveLive {
		v.NewLink = a.absolute(code)
	}
	return nil
}

func (a *App) create(w http.ResponseWriter, r *http.Request) {
	u, ok := a.sessions.FromRequest(r)
	if !ok {
		http.Redirect(w, r, "/auth/google", http.StatusSeeOther)
		return
	}
	limitBody(w, r)
	if err := r.ParseForm(); err != nil {
		a.homeError(w, r, u, http.StatusUnprocessableEntity, msgBadURL)
		return
	}
	target, err := urlcheck.Validate(r.PostFormValue("url"))
	if err != nil {
		a.homeError(w, r, u, http.StatusUnprocessableEntity, msgBadURL)
		return
	}
	code, err := a.store.Create(r.Context(), u.Sub, u.Email, target, a.clock.Now())
	if err != nil {
		if errors.Is(err, store.ErrRateLimited) {
			settings, serr := a.store.GetSettings(r.Context())
			if serr != nil {
				log.Printf("create failed")
				a.homeError(w, r, u, http.StatusInternalServerError, msgCreateFail)
				return
			}
			a.homeError(w, r, u, http.StatusTooManyRequests, fmt.Sprintf("Za dużo skrótów w tej godzinie. Limit to %d.", settings.MaxCreatesPerHour))
			return
		}
		log.Printf("create failed")
		a.homeError(w, r, u, http.StatusInternalServerError, msgCreateFail)
		return
	}
	log.Printf("created code=%s sub=%s", code, u.Sub)
	w.Header().Set("Location", "/?nowy="+url.QueryEscape(code))
	w.WriteHeader(http.StatusSeeOther)
}

func (a *App) homeError(w http.ResponseWriter, r *http.Request, u session.User, status int, msg string) {
	v := view{
		Title: "Szorty",
		Email: u.Email,
		Owner: a.sessions.IsOwner(u),
		Error: msg,
	}
	if err := a.loadLinks(r.Context(), &v, 1, a.clock.Now(), u.Sub, "/", msgEmptyMine, true); err != nil {
		log.Printf("home failed")
	}
	a.render(w, status, "home", v)
}

func (a *App) resolve(w http.ResponseWriter, r *http.Request, code string) {
	target, result, err := a.store.Resolve(r.Context(), code, a.clock.Now())
	if err != nil {
		log.Printf("resolve failed")
		a.message(w, r, http.StatusInternalServerError, msgPageFail)
		return
	}
	if result != store.ResolveLive {
		a.dead(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Location", target)
	w.WriteHeader(http.StatusFound)
}

func (a *App) remove(w http.ResponseWriter, r *http.Request, code string) {
	u, ok := a.sessions.FromRequest(r)
	if !ok {
		http.Redirect(w, r, "/auth/google", http.StatusSeeOther)
		return
	}
	if !shortcode.Valid(code) {
		a.unknown(w, r)
		return
	}
	limitBody(w, r)
	_ = r.ParseForm()
	owner := a.sessions.IsOwner(u)
	found, deleted, err := a.store.Delete(r.Context(), code, u.Sub, owner)
	if err != nil {
		log.Printf("delete failed")
		a.message(w, r, http.StatusInternalServerError, msgPageFail)
		return
	}
	if found && !deleted {
		a.unknown(w, r)
		return
	}
	if safeNext(r.PostFormValue("next"), owner) == "/wszystkie" {
		http.Redirect(w, r, "/wszystkie", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *App) listAll(w http.ResponseWriter, r *http.Request) {
	u, ok := a.owner(r)
	if !ok {
		a.unknown(w, r)
		return
	}
	v := view{Title: "Wszystkie", Email: u.Email, Owner: true}
	if err := a.loadLinks(r.Context(), &v, pageFrom(r), a.clock.Now(), "", "/wszystkie", msgEmptyAll, false); err != nil {
		log.Printf("list failed")
		a.message(w, r, http.StatusInternalServerError, msgPageFail)
		return
	}
	a.render(w, http.StatusOK, "all", v)
}

func (a *App) settingsGet(w http.ResponseWriter, r *http.Request) {
	u, ok := a.owner(r)
	if !ok {
		a.unknown(w, r)
		return
	}
	a.renderSettings(w, r, u, http.StatusOK, "", r.URL.Query().Get("zapisane") == "1")
}

func (a *App) settingsPost(w http.ResponseWriter, r *http.Request) {
	u, ok := a.owner(r)
	if !ok {
		a.unknown(w, r)
		return
	}
	limitBody(w, r)
	if err := r.ParseForm(); err != nil {
		a.renderSettings(w, r, u, http.StatusUnprocessableEntity, msgBadSettings, false)
		return
	}
	days, err1 := strconv.Atoi(r.PostFormValue("retention_days"))
	limit, err2 := strconv.Atoi(r.PostFormValue("max_creates_per_hour"))
	if err1 != nil || err2 != nil || days < 1 || days > 3650 || limit < 1 || limit > 1000 {
		a.renderSettings(w, r, u, http.StatusUnprocessableEntity, msgBadSettings, false)
		return
	}
	if err := a.store.SaveSettings(r.Context(), days, limit, a.clock.Now()); err != nil {
		log.Printf("save settings failed")
		a.renderSettings(w, r, u, http.StatusInternalServerError, msgSettingsFail, false)
		return
	}
	http.Redirect(w, r, "/ustawienia?zapisane=1", http.StatusSeeOther)
}

func (a *App) renderSettings(w http.ResponseWriter, r *http.Request, u session.User, status int, errMsg string, saved bool) {
	v := view{
		Title: "Ustawienia",
		Email: u.Email,
		Owner: true,
		Error: errMsg,
	}
	if saved {
		v.Notice = msgSaved
	}
	settings, err := a.store.GetSettings(r.Context())
	if err != nil {
		log.Printf("settings read failed")
		a.message(w, r, http.StatusInternalServerError, msgSettingsFail)
		return
	}
	v.Days = settings.RetentionDays
	v.Limit = settings.MaxCreatesPerHour
	a.render(w, status, "settings", v)
}

func (a *App) authGoogle(w http.ResponseWriter, r *http.Request) {
	state, err := randomState()
	if err != nil {
		log.Printf("oauth failed")
		a.message(w, r, http.StatusBadRequest, msgOAuth)
		return
	}
	a.sessions.SetState(w, state)
	http.Redirect(w, r, a.oauth.AuthCodeURL(state), http.StatusFound)
}

func (a *App) authCallback(w http.ResponseWriter, r *http.Request) {
	cookieState, ok := a.sessions.State(r)
	if !ok || !stateMatches(cookieState, r.URL.Query().Get("state")) {
		log.Printf("oauth failed")
		a.message(w, r, http.StatusBadRequest, msgOAuth)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		log.Printf("oauth failed")
		a.message(w, r, http.StatusBadRequest, msgOAuth)
		return
	}
	id, err := a.exchanger.Exchange(r.Context(), code)
	if err != nil || id.Sub == "" || id.Email == "" || !id.EmailVerified {
		log.Printf("oauth failed")
		a.message(w, r, http.StatusBadRequest, msgOAuth)
		return
	}
	a.sessions.Set(w, session.User{
		Sub:   id.Sub,
		Email: id.Email,
		Exp:   a.clock.Now().Add(14 * 24 * time.Hour),
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	a.sessions.Clear(w)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *App) health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ok")
}

func (a *App) style(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(a.css)
}

func (a *App) copyJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(a.js)
}

func (a *App) font(w http.ResponseWriter, r *http.Request) {
	name := map[string]string{
		"/font-display.woff2": "static/display.woff2",
		"/font-body.woff2":    "static/body.woff2",
		"/font-mono.woff2":    "static/mono.woff2",
	}[r.URL.Path]
	body, err := assets.ReadFile(name)
	if err != nil {
		a.unknown(w, r)
		return
	}
	w.Header().Set("Content-Type", "font/woff2")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
