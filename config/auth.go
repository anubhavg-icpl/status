package config

import (
	"os"
	"strings"
	"time"
)

// AuthConfig gates the status page behind a login.
//
// This exists because the page renders internal topology — node names, images,
// namespaces, failing workloads. On any host reachable from outside the
// cluster that is not information to hand to anonymous visitors.
type AuthConfig struct {
	// Enabled turns the login requirement on. Off by default so existing
	// deployments are unaffected by an upgrade.
	Enabled bool `yaml:"enabled" json:"enabled"`
	// SessionTTL is how long a login lasts before it must be repeated.
	SessionTTL time.Duration `yaml:"session_ttl" json:"session_ttl"`
	// PublicStatus keeps the service list and incidents readable without a
	// login, while the Kubernetes cluster view still requires one. Use it for
	// a customer-facing page that should not leak infrastructure detail.
	PublicStatus bool `yaml:"public_status" json:"public_status"`
	// SecureCookie forces the Secure flag on the session cookie. Auto-detected
	// from a https base_url when left unset; set it explicitly behind a proxy
	// that terminates TLS.
	SecureCookie *bool `yaml:"secure_cookie" json:"secure_cookie"`
	// Users allowed to sign in.
	Users []AuthUser `yaml:"users" json:"-"`
}

// AuthUser is one login. Store a bcrypt hash, never a plaintext password —
// Password exists only for bootstrapping and is hashed at startup.
type AuthUser struct {
	Username     string `yaml:"username" json:"username"`
	PasswordHash string `yaml:"password_hash" json:"-"`
	// Password is a convenience for first boot. It is hashed in memory on load
	// and never persisted, but it does sit in the config file in plaintext, so
	// prefer password_hash or the STATUS_AUTH_PASSWORD env var.
	Password string `yaml:"password" json:"-"`
}

func defaultAuth() AuthConfig {
	return AuthConfig{
		Enabled:    false,
		SessionTTL: 12 * time.Hour,
	}
}

func (c *Config) applyAuthDefaults() {
	if c.Auth.SessionTTL <= 0 {
		c.Auth.SessionTTL = defaultAuth().SessionTTL
	}
	// A session cookie for an https page must carry Secure, or a downgrade
	// attack can strip it off and read the token in the clear.
	if c.Auth.SecureCookie == nil {
		secure := strings.HasPrefix(strings.ToLower(c.BaseURL), "https://")
		c.Auth.SecureCookie = &secure
	}
}

// applyAuthEnv lets the first admin be created without editing a config file.
// STATUS_AUTH_PASSWORD_HASH is preferred; STATUS_AUTH_PASSWORD is hashed at
// startup for convenience.
func (c *Config) applyAuthEnv() {
	user := os.Getenv("STATUS_AUTH_USER")
	hash := os.Getenv("STATUS_AUTH_PASSWORD_HASH")
	plain := os.Getenv("STATUS_AUTH_PASSWORD")

	if v, ok := os.LookupEnv("STATUS_AUTH_ENABLED"); ok {
		c.Auth.Enabled = truthyEnv(v)
	}
	if v, ok := os.LookupEnv("STATUS_AUTH_PUBLIC_STATUS"); ok {
		c.Auth.PublicStatus = truthyEnv(v)
	}
	if user == "" || (hash == "" && plain == "") {
		return
	}

	// Env replaces any same-named user from the file rather than adding a
	// duplicate, so rotating the password via env actually takes effect.
	out := c.Auth.Users[:0]
	for _, u := range c.Auth.Users {
		if !strings.EqualFold(u.Username, user) {
			out = append(out, u)
		}
	}
	c.Auth.Users = append(out, AuthUser{
		Username:     user,
		PasswordHash: hash,
		Password:     plain,
	})
	c.Auth.Enabled = true
}

func truthyEnv(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "t", "true", "yes", "y", "on", "enabled":
		return true
	}
	return false
}

// ApplyAuthDefaultsForTest exposes applyAuthDefaults so other packages can
// verify the derived cookie policy without duplicating the rule.
func (c *Config) ApplyAuthDefaultsForTest() { c.applyAuthDefaults() }
