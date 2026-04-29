package handler

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Derrity/kie2api-go/config"
)

const (
	sessionCookieName = "kie2api_session"
	sessionTTL        = 7 * 24 * time.Hour
)

// WebAuth provides cookie/password protection for the web console API.
type WebAuth struct {
	Store    *config.Store
	mu       sync.Mutex
	sessions map[string]time.Time // token -> expiry
}

// NewWebAuth constructs a WebAuth.
func NewWebAuth(s *config.Store) *WebAuth {
	return &WebAuth{Store: s, sessions: map[string]time.Time{}}
}

// EnsurePassword generates a random password if one is not yet configured,
// returning whether a fresh password was created and the password value.
// Caller is responsible for logging it once at startup.
func (a *WebAuth) EnsurePassword() (created bool, pw string, err error) {
	cfg := a.Store.Get()
	if cfg.WebPassword != "" {
		return false, cfg.WebPassword, nil
	}
	generated := randomPassword()
	if _, err := a.Store.Update(func(c *config.Config) {
		if c.WebPassword == "" {
			c.WebPassword = generated
		}
	}); err != nil {
		return false, "", err
	}
	return true, a.Store.Get().WebPassword, nil
}

func (a *WebAuth) issueSession() string {
	tok := randomHex(24)
	a.mu.Lock()
	a.sessions[tok] = time.Now().Add(sessionTTL)
	// opportunistic GC
	now := time.Now()
	for k, exp := range a.sessions {
		if now.After(exp) {
			delete(a.sessions, k)
		}
	}
	a.mu.Unlock()
	return tok
}

func (a *WebAuth) revokeSession(tok string) {
	a.mu.Lock()
	delete(a.sessions, tok)
	a.mu.Unlock()
}

func (a *WebAuth) revokeAll() {
	a.mu.Lock()
	a.sessions = map[string]time.Time{}
	a.mu.Unlock()
}

func (a *WebAuth) validSession(tok string) bool {
	if tok == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	exp, ok := a.sessions[tok]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(a.sessions, tok)
		return false
	}
	return true
}

// Require wraps an http.HandlerFunc, requiring a valid session cookie or a
// matching password in the Authorization header.
func (a *WebAuth) Require(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.authenticated(r) {
			h(w, r)
			return
		}
		writeJSONError(w, http.StatusUnauthorized, "authentication_error", "login required")
	}
}

func (a *WebAuth) authenticated(r *http.Request) bool {
	if c, err := r.Cookie(sessionCookieName); err == nil && a.validSession(c.Value) {
		return true
	}
	pw := a.Store.Get().WebPassword
	if pw == "" {
		return false
	}
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		if subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(h, "Bearer ")), []byte(pw)) == 1 {
			return true
		}
	}
	return false
}

// HandleStatus returns whether the caller is currently authenticated.
// Used by the SPA to decide whether to show the login form.
func (a *WebAuth) HandleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": a.authenticated(r),
		"has_password":  a.Store.Get().WebPassword != "",
	})
}

// HandleLogin verifies the password and sets a session cookie.
func (a *WebAuth) HandleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	pw := a.Store.Get().WebPassword
	if pw == "" || subtle.ConstantTimeCompare([]byte(body.Password), []byte(pw)) != 1 {
		// constant-ish delay against trivial brute force
		time.Sleep(300 * time.Millisecond)
		writeJSONError(w, http.StatusUnauthorized, "authentication_error", "incorrect password")
		return
	}
	tok := a.issueSession()
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(sessionTTL),
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// HandleLogout drops the caller's session cookie.
func (a *WebAuth) HandleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil {
		a.revokeSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// HandleChangePassword updates the web password and revokes all sessions.
// Requires the caller to already be authenticated AND to confirm the
// current password (defence against XSRF-ish accidents).
func (a *WebAuth) HandleChangePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	cur := a.Store.Get().WebPassword
	if subtle.ConstantTimeCompare([]byte(body.Current), []byte(cur)) != 1 {
		writeJSONError(w, http.StatusUnauthorized, "authentication_error", "current password incorrect")
		return
	}
	if len(body.New) < 6 {
		writeJSONError(w, http.StatusBadRequest, "invalid_request_error", "new password must be at least 6 chars")
		return
	}
	if _, err := a.Store.Update(func(c *config.Config) { c.WebPassword = body.New }); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	a.revokeAll()
	// re-issue a fresh session for the caller
	tok := a.issueSession()
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(sessionTTL),
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func randomPassword() string {
	// 12-char base32-ish: 6 random bytes -> 12 hex chars (compact, easy to type once).
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "kie2api-" + hex.EncodeToString([]byte(time.Now().String()))[:8]
	}
	return hex.EncodeToString(b)
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return strings.Repeat("0", n*2)
	}
	return hex.EncodeToString(b)
}
