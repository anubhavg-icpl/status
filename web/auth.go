package web

import (
	"crypto/subtle"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/status/config"
	"golang.org/x/crypto/bcrypt"
)

const sessionCookieName = "status_session"

// authenticator verifies logins and manages session cookies.
type authenticator struct {
	cfg    config.AuthConfig
	users  map[string]string // lowercased username -> bcrypt hash
	secure bool

	// loginLimiter throttles password guessing per client IP.
	mu      sync.Mutex
	fails   map[string]*loginAttempts
	stopped bool
}

type loginAttempts struct {
	count       int
	windowStart time.Time
	lockedUntil time.Time
}

const (
	maxLoginFailures = 5
	loginWindow      = 5 * time.Minute
	loginLockout     = 15 * time.Minute
)

// newAuthenticator hashes any plaintext bootstrap passwords and builds the
// lookup table. A user whose password cannot be hashed is dropped rather than
// silently admitted.
func newAuthenticator(cfg config.AuthConfig) *authenticator {
	a := &authenticator{
		cfg:   cfg,
		users: make(map[string]string, len(cfg.Users)),
		fails: make(map[string]*loginAttempts),
	}
	if cfg.SecureCookie != nil {
		a.secure = *cfg.SecureCookie
	}

	for _, u := range cfg.Users {
		name := strings.ToLower(strings.TrimSpace(u.Username))
		if name == "" {
			continue
		}
		hash := strings.TrimSpace(u.PasswordHash)
		if hash == "" && u.Password != "" {
			h, err := bcrypt.GenerateFromPassword([]byte(u.Password), bcrypt.DefaultCost)
			if err != nil {
				log.Printf("auth: cannot hash password for %q, user disabled: %v", name, err)
				continue
			}
			hash = string(h)
		}
		if hash == "" {
			log.Printf("auth: user %q has no password, disabled", name)
			continue
		}
		a.users[name] = hash
	}

	if cfg.Enabled && len(a.users) == 0 {
		log.Println("auth: WARNING — auth is enabled but no usable users are configured; nobody can log in")
	}
	return a
}

// Enabled reports whether the login requirement is active.
func (a *authenticator) Enabled() bool { return a != nil && a.cfg.Enabled }

// Verify checks a username/password pair. It always runs a bcrypt comparison,
// even for an unknown user, so response time does not reveal which usernames
// exist.
func (a *authenticator) Verify(username, password string) bool {
	name := strings.ToLower(strings.TrimSpace(username))
	hash, ok := a.users[name]
	if !ok {
		// Cost-matched dummy compare against a fixed hash of "not-a-user".
		_ = bcrypt.CompareHashAndPassword(
			[]byte("$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"),
			[]byte(password))
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// throttled reports whether this IP is currently locked out, and records the
// check. Without it the login form is an unlimited password oracle.
func (a *authenticator) throttled(ip string) (bool, time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	e, ok := a.fails[ip]
	if !ok {
		return false, 0
	}
	if time.Now().Before(e.lockedUntil) {
		return true, time.Until(e.lockedUntil)
	}
	return false, 0
}

func (a *authenticator) recordFailure(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	e, ok := a.fails[ip]
	if !ok || now.Sub(e.windowStart) > loginWindow {
		a.fails[ip] = &loginAttempts{count: 1, windowStart: now}
		return
	}
	e.count++
	if e.count >= maxLoginFailures {
		e.lockedUntil = now.Add(loginLockout)
		e.count = 0
		e.windowStart = now
		log.Printf("auth: too many failed logins from %s, locked out for %s", ip, loginLockout)
	}
}

func (a *authenticator) recordSuccess(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.fails, ip)
}

// setSessionCookie writes the session cookie. HttpOnly keeps it away from any
// script on the page; SameSite=Lax stops a cross-site form from riding it.
func (a *authenticator) setSessionCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   a.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (a *authenticator) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   a.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// currentUser returns the logged-in username, or "" when there is no valid
// session.
func (s *Server) currentUser(r *http.Request) string {
	c, err := r.Cookie(sessionCookieName)
	if err != nil || c.Value == "" {
		return ""
	}
	sess := s.storage.GetSession(c.Value)
	if sess == nil {
		return ""
	}
	return sess.Username
}

// requireSession gates a handler behind a login.
//
// An existing API credential (X-API-Key / Bearer / Basic) is accepted too, so
// automation that worked before auth was switched on keeps working.
func (s *Server) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.auth.Enabled() {
			next(w, r)
			return
		}
		if s.currentUser(r) != "" || s.hasValidAPICredential(r) {
			next(w, r)
			return
		}
		// HTML navigations get the login form; API clients get JSON.
		if wantsHTML(r) {
			http.Redirect(w, r, "/login?next="+urlEscape(r.URL.RequestURI()), http.StatusSeeOther)
			return
		}
		s.jsonError(w, "authentication required", http.StatusUnauthorized)
	}
}

// handleLogin renders the form and processes submissions.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.auth.Enabled() {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	// Already signed in — no reason to show the form again.
	if r.Method == http.MethodGet && s.currentUser(r) != "" {
		http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusSeeOther)
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.renderLogin(w, r, "", http.StatusOK)

	case http.MethodPost:
		ip := loginClientIP(r)
		if locked, wait := s.auth.throttled(ip); locked {
			s.renderLogin(w, r,
				"Too many failed attempts. Try again in "+wait.Round(time.Second).String()+".",
				http.StatusTooManyRequests)
			return
		}
		if err := r.ParseForm(); err != nil {
			s.renderLogin(w, r, "Malformed request.", http.StatusBadRequest)
			return
		}
		user := r.PostFormValue("username")
		pass := r.PostFormValue("password")

		if !s.auth.Verify(user, pass) {
			s.auth.recordFailure(ip)
			log.Printf("auth: failed login for %q from %s", user, ip)
			// Deliberately vague: naming which half was wrong confirms usernames.
			s.renderLogin(w, r, "Incorrect username or password.", http.StatusUnauthorized)
			return
		}

		sess, err := s.storage.CreateSession(
			strings.ToLower(strings.TrimSpace(user)), r.UserAgent(), ip, s.config.Auth.SessionTTL)
		if err != nil {
			log.Printf("auth: cannot create session: %v", err)
			s.renderLogin(w, r, "Could not start a session. Try again.", http.StatusInternalServerError)
			return
		}
		s.auth.recordSuccess(ip)
		s.auth.setSessionCookie(w, sess.Token, sess.ExpiresAt)
		log.Printf("auth: %s signed in from %s", sess.Username, ip)
		http.Redirect(w, r, safeNext(r.PostFormValue("next")), http.StatusSeeOther)

	default:
		s.jsonError(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleLogout destroys the session server-side, not just the cookie.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
		s.storage.DeleteSession(c.Value)
	}
	s.auth.clearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// handleAuthStatus lets the page decide whether to show a sign-out control.
func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	s.jsonResponse(w, map[string]any{
		"enabled":       s.auth.Enabled(),
		"authenticated": s.currentUser(r) != "",
		"username":      s.currentUser(r),
		"public_status": s.config.Auth.PublicStatus,
	})
}

func (s *Server) renderLogin(w http.ResponseWriter, r *http.Request, errMsg string, code int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	data := map[string]any{
		"Title": s.config.Title,
		"Logo":  s.config.Logo,
		"Error": errMsg,
		"Next":  safeNext(r.URL.Query().Get("next")),
		"Theme": s.config.Theme,
	}
	if err := s.loginTmpl.Execute(w, data); err != nil {
		log.Printf("auth: render login: %v", err)
	}
}

// purgeSessions sweeps expired sessions so the bucket cannot grow forever.
func (s *Server) purgeSessions(stop <-chan struct{}) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if n := s.storage.PurgeExpiredSessions(); n > 0 {
				log.Printf("auth: purged %d expired session(s)", n)
			}
		}
	}
}

// safeNext keeps post-login redirects on this site. An open redirect here
// would let a phishing link bounce a freshly authenticated user off-site.
func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return "/"
	}
	if strings.Contains(next, "\\") || strings.Contains(next, "\n") || strings.Contains(next, "\r") {
		return "/"
	}
	if next == "/login" || strings.HasPrefix(next, "/login?") {
		return "/"
	}
	return next
}

func urlEscape(s string) string {
	r := strings.NewReplacer("&", "%26", "?", "%3F", "#", "%23", " ", "%20", "\"", "%22")
	return r.Replace(s)
}

// wantsHTML reports whether the caller is a browser navigating, as opposed to
// fetch()/curl, so the response format matches the caller's expectation.
func wantsHTML(r *http.Request) bool {
	if r.Header.Get("X-Requested-With") != "" {
		return false
	}
	if strings.Contains(r.Header.Get("Sec-Fetch-Mode"), "cors") {
		return false
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// hasValidAPICredential reports whether the request carries a correct API key,
// bearer token, or basic-auth pair.
func (s *Server) hasValidAPICredential(r *http.Request) bool {
	cfg := s.config.API
	if k := r.Header.Get("X-API-Key"); k != "" && cfg.Key != "" &&
		subtle.ConstantTimeCompare([]byte(k), []byte(cfg.Key)) == 1 {
		return true
	}
	if auth := r.Header.Get("Authorization"); auth != "" {
		if tok, ok := strings.CutPrefix(auth, "Bearer "); ok && cfg.BearerToken != "" &&
			subtle.ConstantTimeCompare([]byte(tok), []byte(cfg.BearerToken)) == 1 {
			return true
		}
		if cfg.BasicAuth.Enabled {
			if u, p, ok := r.BasicAuth(); ok &&
				subtle.ConstantTimeCompare([]byte(u), []byte(cfg.BasicAuth.Username)) == 1 &&
				subtle.ConstantTimeCompare([]byte(p), []byte(cfg.BasicAuth.Password)) == 1 {
				return true
			}
		}
	}
	return false
}

// loginClientIP picks the key the login throttle counts against.
//
// Behind Cloudflare and traefik, RemoteAddr is the ingress pod, so keying on it
// would let one attacker lock out every user at once. CF-Connecting-IP is set
// by Cloudflare and is the real client for this deployment.
//
// A forwarded header is caller-supplied and therefore spoofable, so an attacker
// who rotates it evades the per-IP lockout. That is an accepted trade: the
// lockout exists to stop casual guessing without turning into a self-inflicted
// denial of service, and bcrypt at default cost is what actually makes offline
// and online guessing expensive.
func loginClientIP(r *http.Request) string {
	if ip := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); ip != "" {
		return ip
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if first, _, ok := strings.Cut(xff, ","); ok {
			return strings.TrimSpace(first)
		}
		return strings.TrimSpace(xff)
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
