package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/status/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

func hashOf(t *testing.T, pw string) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
	require.NoError(t, err)
	return string(h)
}

func TestAuthenticatorVerifiesHashedPassword(t *testing.T) {
	a := newAuthenticator(config.AuthConfig{
		Enabled: true,
		Users:   []config.AuthUser{{Username: "Admin", PasswordHash: hashOf(t, "s3cret")}},
	})
	assert.True(t, a.Verify("admin", "s3cret"), "username match is case-insensitive")
	assert.True(t, a.Verify("ADMIN", "s3cret"))
	assert.False(t, a.Verify("admin", "wrong"))
	assert.False(t, a.Verify("nobody", "s3cret"))
	assert.False(t, a.Verify("admin", ""))
}

func TestAuthenticatorHashesPlaintextBootstrapPassword(t *testing.T) {
	a := newAuthenticator(config.AuthConfig{
		Enabled: true,
		Users:   []config.AuthUser{{Username: "ops", Password: "bootstrap-me"}},
	})
	assert.True(t, a.Verify("ops", "bootstrap-me"))
	assert.NotEqual(t, "bootstrap-me", a.users["ops"], "the plaintext must not be stored")
	assert.Contains(t, a.users["ops"], "$2a$", "stored as a bcrypt hash")
}

func TestAuthenticatorDropsUsersWithoutAPassword(t *testing.T) {
	a := newAuthenticator(config.AuthConfig{
		Enabled: true,
		Users: []config.AuthUser{
			{Username: "ghost"},
			{Username: "real", PasswordHash: hashOf(t, "pw")},
		},
	})
	assert.NotContains(t, a.users, "ghost", "a passwordless user must never be admitted")
	assert.False(t, a.Verify("ghost", ""))
	assert.True(t, a.Verify("real", "pw"))
}

func TestLoginThrottleLocksOutAfterRepeatedFailures(t *testing.T) {
	a := newAuthenticator(config.AuthConfig{Enabled: true})

	locked, _ := a.throttled("1.2.3.4")
	require.False(t, locked)

	for i := 0; i < maxLoginFailures; i++ {
		a.recordFailure("1.2.3.4")
	}
	locked, wait := a.throttled("1.2.3.4")
	assert.True(t, locked, "the login form must not be an unlimited password oracle")
	assert.Greater(t, wait, time.Duration(0))

	other, _ := a.throttled("5.6.7.8")
	assert.False(t, other, "lockout is per-IP, not global")
}

func TestLoginThrottleClearsOnSuccess(t *testing.T) {
	a := newAuthenticator(config.AuthConfig{Enabled: true})
	for i := 0; i < maxLoginFailures-1; i++ {
		a.recordFailure("1.2.3.4")
	}
	a.recordSuccess("1.2.3.4")
	a.recordFailure("1.2.3.4")
	locked, _ := a.throttled("1.2.3.4")
	assert.False(t, locked, "a good login resets the counter")
}

func TestLoginClientIPPrefersCloudflareHeader(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	r.RemoteAddr = "10.42.0.7:5555" // the traefik pod
	assert.Equal(t, "10.42.0.7", loginClientIP(r), "no proxy headers: fall back to RemoteAddr")

	r.Header.Set("X-Forwarded-For", "203.0.113.9, 10.42.0.7")
	assert.Equal(t, "203.0.113.9", loginClientIP(r), "first XFF hop is the client")

	r.Header.Set("CF-Connecting-IP", "198.51.100.5")
	assert.Equal(t, "198.51.100.5", loginClientIP(r), "Cloudflare's header wins")
}

func TestSafeNextBlocksOpenRedirect(t *testing.T) {
	cases := map[string]string{
		"/history":             "/history",
		"/incidents/abc":       "/incidents/abc",
		"":                     "/",
		"https://evil.com":     "/",
		"//evil.com":           "/",
		"/\\evil.com":          "/",
		"/login":               "/",
		"/login?next=/x":       "/",
		"/ok\nSet-Cookie: x=1": "/",
	}
	for in, want := range cases {
		assert.Equal(t, want, safeNext(in), "safeNext(%q)", in)
	}
}

func TestWantsHTMLDistinguishesBrowserFromFetch(t *testing.T) {
	nav := httptest.NewRequest(http.MethodGet, "/", nil)
	nav.Header.Set("Accept", "text/html,application/xhtml+xml")
	assert.True(t, wantsHTML(nav))

	api := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	api.Header.Set("Accept", "application/json")
	assert.False(t, api.Header.Get("Accept") == "", "sanity")
	assert.False(t, wantsHTML(api))

	xhr := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	xhr.Header.Set("Accept", "text/html")
	xhr.Header.Set("X-Requested-With", "XMLHttpRequest")
	assert.False(t, wantsHTML(xhr), "an XHR must get JSON, not a login page")
}

func TestAuthDisabledVerifiesNothing(t *testing.T) {
	var a *authenticator
	assert.False(t, a.Enabled())
	b := newAuthenticator(config.AuthConfig{Enabled: false})
	assert.False(t, b.Enabled())
}

func TestSecureCookieFollowsBaseURLScheme(t *testing.T) {
	https := config.DefaultConfig()
	https.BaseURL = "https://status.example.com"
	https.Auth.SecureCookie = nil
	https.ApplyAuthDefaultsForTest()
	require.NotNil(t, https.Auth.SecureCookie)
	assert.True(t, *https.Auth.SecureCookie, "an https page must set Secure or the token can be stripped")

	plain := config.DefaultConfig()
	plain.BaseURL = "http://localhost:8080"
	plain.Auth.SecureCookie = nil
	plain.ApplyAuthDefaultsForTest()
	require.NotNil(t, plain.Auth.SecureCookie)
	assert.False(t, *plain.Auth.SecureCookie, "Secure on plain http would make the cookie never send")
}
