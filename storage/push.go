package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	bolt "go.etcd.io/bbolt"
)

// PushSubscription is a browser Web Push endpoint registered by a visitor.
// The keys are the raw base64url values the Push API hands to JavaScript;
// webpush-go consumes them verbatim.
type PushSubscription struct {
	ID        string    `json:"id"` // sha256(endpoint) — stable, no PII in the key
	Endpoint  string    `json:"endpoint"`
	P256dh    string    `json:"p256dh"`
	Auth      string    `json:"auth"`
	UserAgent string    `json:"user_agent,omitempty"`
	Topics    []string  `json:"topics,omitempty"` // empty = every event
	CreatedAt time.Time `json:"created_at"`
	LastSeen  time.Time `json:"last_seen"`
}

// PushSubscriptionID derives the storage key for an endpoint URL.
func PushSubscriptionID(endpoint string) string {
	sum := sha256.Sum256([]byte(endpoint))
	return hex.EncodeToString(sum[:16])
}

// SavePushSubscription upserts a subscription keyed by its endpoint.
// Re-subscribing from the same browser refreshes rather than duplicates.
func (s *Storage) SavePushSubscription(sub PushSubscription) (*PushSubscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sub.ID = PushSubscriptionID(sub.Endpoint)
	now := time.Now()
	sub.LastSeen = now

	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketPushSubs)
		if existing := b.Get([]byte(sub.ID)); existing != nil {
			var prev PushSubscription
			if json.Unmarshal(existing, &prev) == nil && !prev.CreatedAt.IsZero() {
				sub.CreatedAt = prev.CreatedAt
			}
		}
		if sub.CreatedAt.IsZero() {
			sub.CreatedAt = now
		}
		data, err := json.Marshal(sub)
		if err != nil {
			return err
		}
		return b.Put([]byte(sub.ID), data)
	})
	if err != nil {
		return nil, err
	}
	return &sub, nil
}

// DeletePushSubscription removes a subscription by endpoint URL.
// Returns false when the endpoint was not registered.
func (s *Storage) DeletePushSubscription(endpoint string) bool {
	return s.DeletePushSubscriptionByID(PushSubscriptionID(endpoint))
}

// DeletePushSubscriptionByID removes a subscription by its derived ID.
func (s *Storage) DeletePushSubscriptionByID(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	found := false
	_ = s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketPushSubs)
		if b.Get([]byte(id)) == nil {
			return nil
		}
		found = true
		return b.Delete([]byte(id))
	})
	return found
}

// ListPushSubscriptions returns every registered subscription.
func (s *Storage) ListPushSubscriptions() []PushSubscription {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []PushSubscription
	_ = s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketPushSubs).ForEach(func(_, v []byte) error {
			var sub PushSubscription
			if err := json.Unmarshal(v, &sub); err != nil {
				return nil // skip corrupt row rather than abort the fan-out
			}
			out = append(out, sub)
			return nil
		})
	})
	return out
}

// CountPushSubscriptions returns how many endpoints are registered.
func (s *Storage) CountPushSubscriptions() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	n := 0
	_ = s.db.View(func(tx *bolt.Tx) error {
		n = tx.Bucket(bucketPushSubs).Stats().KeyN
		return nil
	})
	return n
}

// GetSetting reads a persisted server-side setting (e.g. generated VAPID keys).
func (s *Storage) GetSetting(key string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var val string
	_ = s.db.View(func(tx *bolt.Tx) error {
		if v := tx.Bucket(bucketSettings).Get([]byte(key)); v != nil {
			val = string(v)
		}
		return nil
	})
	return val
}

// SetSetting persists a server-side setting.
func (s *Storage) SetSetting(key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketSettings).Put([]byte(key), []byte(value))
	})
}
