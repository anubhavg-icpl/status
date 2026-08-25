package storage

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Session is a logged-in browser session. Sessions live server-side so a
// logout, a password change or an operator revoking access takes effect
// immediately — a signed stateless cookie could not be withdrawn early.
type Session struct {
	Token     string    `json:"token"`
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	UserAgent string    `json:"user_agent,omitempty"`
	IP        string    `json:"ip,omitempty"`
}

// Expired reports whether the session is past its lifetime.
func (s Session) Expired() bool { return time.Now().After(s.ExpiresAt) }

// NewSessionToken mints 32 bytes of cryptographic randomness, base64url encoded.
// This value is the whole credential, so it must never be derived from anything
// guessable such as the username or a timestamp.
func NewSessionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// CreateSession stores a new session and returns it.
func (s *Storage) CreateSession(username, userAgent, ip string, ttl time.Duration) (*Session, error) {
	token, err := NewSessionToken()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	sess := Session{
		Token:     token,
		Username:  username,
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
		UserAgent: truncate(userAgent, 180),
		IP:        ip,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	err = s.db.Update(func(tx *bolt.Tx) error {
		data, err := json.Marshal(sess)
		if err != nil {
			return err
		}
		return tx.Bucket(bucketSessions).Put([]byte(token), data)
	})
	if err != nil {
		return nil, err
	}
	return &sess, nil
}

// GetSession looks up a session by token and deletes it if it has expired.
// Returns nil when the token is unknown or no longer valid.
func (s *Storage) GetSession(token string) *Session {
	if token == "" {
		return nil
	}
	s.mu.RLock()
	var sess Session
	found := false
	_ = s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketSessions).Get([]byte(token))
		if v == nil {
			return nil
		}
		if json.Unmarshal(v, &sess) == nil {
			found = true
		}
		return nil
	})
	s.mu.RUnlock()

	if !found {
		return nil
	}
	if sess.Expired() {
		s.DeleteSession(token)
		return nil
	}
	// Constant-time compare so a lookup cannot be used as a timing oracle for
	// partial token guesses.
	if subtle.ConstantTimeCompare([]byte(sess.Token), []byte(token)) != 1 {
		return nil
	}
	return &sess
}

// DeleteSession removes a session (logout).
func (s *Storage) DeleteSession(token string) bool {
	if token == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	found := false
	_ = s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketSessions)
		if b.Get([]byte(token)) == nil {
			return nil
		}
		found = true
		return b.Delete([]byte(token))
	})
	return found
}

// DeleteSessionsForUser revokes every session belonging to one user, so a
// password change or a disabled account logs out all their browsers at once.
func (s *Storage) DeleteSessionsForUser(username string) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	var doomed [][]byte
	_ = s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketSessions)
		_ = b.ForEach(func(k, v []byte) error {
			var sess Session
			if json.Unmarshal(v, &sess) == nil && sess.Username == username {
				doomed = append(doomed, append([]byte(nil), k...))
			}
			return nil
		})
		for _, k := range doomed {
			if err := b.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
	return len(doomed)
}

// PurgeExpiredSessions drops timed-out sessions. Without it the bucket grows
// forever, since an abandoned browser never calls logout.
func (s *Storage) PurgeExpiredSessions() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	var doomed [][]byte
	_ = s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketSessions)
		_ = b.ForEach(func(k, v []byte) error {
			var sess Session
			if json.Unmarshal(v, &sess) == nil && sess.Expired() {
				doomed = append(doomed, append([]byte(nil), k...))
			}
			return nil
		})
		for _, k := range doomed {
			if err := b.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
	return len(doomed)
}

// CountSessions returns the number of stored sessions, expired ones included.
func (s *Storage) CountSessions() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	_ = s.db.View(func(tx *bolt.Tx) error {
		n = tx.Bucket(bucketSessions).Stats().KeyN
		return nil
	})
	return n
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
