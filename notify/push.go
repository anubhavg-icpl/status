package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/status/storage"
)

// Settings keys under which generated VAPID keys are persisted, so a restart
// does not invalidate every browser subscription.
const (
	settingVAPIDPublic  = "vapid_public_key"
	settingVAPIDPrivate = "vapid_private_key"
)

// PushConfig configures browser/phone Web Push delivery.
type PushConfig struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
	// Subject is the VAPID "sub" claim — a mailto: or https: URL identifying
	// the sender to the push service. Required by the VAPID spec.
	Subject string `yaml:"subject" json:"subject"`
	// PublicKey/PrivateKey are base64url VAPID keys. Leave empty to have the
	// server generate a pair on first boot and persist it in BoltDB.
	PublicKey  string `yaml:"public_key" json:"-"`
	PrivateKey string `yaml:"private_key" json:"-"`
	// TTL is how long the push service should retain an undelivered message.
	TTL int `yaml:"ttl" json:"ttl"`
	// Urgency: very-low | low | normal | high. High wakes a sleeping phone.
	Urgency string `yaml:"urgency" json:"urgency"`
}

// PushPayload is the JSON the service worker receives in its `push` event.
type PushPayload struct {
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Event     string    `json:"event"`
	Severity  string    `json:"severity,omitempty"`
	URL       string    `json:"url,omitempty"`
	Tag       string    `json:"tag,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// PushManager owns VAPID keys and fans notifications out to every registered
// browser endpoint. Endpoints the push service reports as gone (404/410) are
// pruned so the subscription list cannot grow without bound.
type PushManager struct {
	store *storage.Storage
	mu    sync.RWMutex
	cfg   PushConfig
	sem   chan struct{}
}

// NewPushManager loads or generates VAPID keys. It returns a disabled manager
// (never nil-panicking) when push is switched off, so callers need no guards
// beyond a nil check.
func NewPushManager(store *storage.Storage, cfg PushConfig) (*PushManager, error) {
	if cfg.TTL <= 0 {
		cfg.TTL = 24 * 60 * 60 // 24h — an alert older than a day is noise
	}
	if cfg.Urgency == "" {
		cfg.Urgency = string(webpush.UrgencyHigh)
	}
	if cfg.Subject == "" {
		cfg.Subject = "mailto:admin@localhost"
	}

	pm := &PushManager{
		store: store,
		cfg:   cfg,
		sem:   make(chan struct{}, 16),
	}
	if !cfg.Enabled {
		return pm, nil
	}
	if store == nil {
		return pm, fmt.Errorf("web push enabled but storage is unavailable")
	}

	// Config keys win; otherwise reuse persisted keys; otherwise mint a pair.
	if pm.cfg.PublicKey == "" || pm.cfg.PrivateKey == "" {
		pub := store.GetSetting(settingVAPIDPublic)
		priv := store.GetSetting(settingVAPIDPrivate)
		if pub != "" && priv != "" {
			pm.cfg.PublicKey, pm.cfg.PrivateKey = pub, priv
		} else {
			newPriv, newPub, err := webpush.GenerateVAPIDKeys()
			if err != nil {
				return pm, fmt.Errorf("generate VAPID keys: %w", err)
			}
			if err := store.SetSetting(settingVAPIDPublic, newPub); err != nil {
				return pm, fmt.Errorf("persist VAPID public key: %w", err)
			}
			if err := store.SetSetting(settingVAPIDPrivate, newPriv); err != nil {
				return pm, fmt.Errorf("persist VAPID private key: %w", err)
			}
			pm.cfg.PublicKey, pm.cfg.PrivateKey = newPub, newPriv
			log.Println("push: generated a new VAPID key pair")
		}
	}
	return pm, nil
}

// Enabled reports whether push delivery is configured and usable.
func (p *PushManager) Enabled() bool {
	if p == nil {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cfg.Enabled && p.cfg.PublicKey != "" && p.cfg.PrivateKey != ""
}

// PublicKey returns the VAPID application server key the browser needs to
// call pushManager.subscribe(). Safe to expose publicly.
func (p *PushManager) PublicKey() string {
	if p == nil {
		return ""
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cfg.PublicKey
}

// Subscribe registers (or refreshes) a browser endpoint.
func (p *PushManager) Subscribe(sub storage.PushSubscription) (*storage.PushSubscription, error) {
	if !p.Enabled() {
		return nil, fmt.Errorf("web push is not enabled")
	}
	if sub.Endpoint == "" || sub.P256dh == "" || sub.Auth == "" {
		return nil, fmt.Errorf("endpoint, p256dh and auth are all required")
	}
	return p.store.SavePushSubscription(sub)
}

// Unsubscribe drops a browser endpoint. Returns false when it was not known.
func (p *PushManager) Unsubscribe(endpoint string) bool {
	if p == nil || p.store == nil {
		return false
	}
	return p.store.DeletePushSubscription(endpoint)
}

// Count reports how many endpoints are registered.
func (p *PushManager) Count() int {
	if p == nil || p.store == nil {
		return 0
	}
	return p.store.CountPushSubscriptions()
}

// Broadcast delivers a payload to every subscription whose topic filter
// matches the event. It blocks until all sends settle, so callers that must
// not stall should invoke it in a goroutine.
func (p *PushManager) Broadcast(ctx context.Context, payload PushPayload) {
	if !p.Enabled() {
		return
	}
	subs := p.store.ListPushSubscriptions()
	if len(subs) == 0 {
		return
	}
	body, err := json.Marshal(payload)
	if err != nil {
		log.Printf("push: marshal payload: %v", err)
		return
	}

	p.mu.RLock()
	cfg := p.cfg
	p.mu.RUnlock()

	var wg sync.WaitGroup
	for _, sub := range subs {
		if !topicMatches(sub.Topics, payload.Event) {
			continue
		}
		wg.Add(1)
		p.sem <- struct{}{}
		go func(s storage.PushSubscription) {
			defer wg.Done()
			defer func() { <-p.sem }()
			p.sendOne(ctx, cfg, s, body)
		}(sub)
	}
	wg.Wait()
}

func (p *PushManager) sendOne(ctx context.Context, cfg PushConfig, s storage.PushSubscription, body []byte) {
	sub := &webpush.Subscription{
		Endpoint: s.Endpoint,
		Keys: webpush.Keys{
			P256dh: s.P256dh,
			Auth:   s.Auth,
		},
	}
	sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	resp, err := webpush.SendNotificationWithContext(sctx, body, sub, &webpush.Options{
		Subscriber:      cfg.Subject,
		VAPIDPublicKey:  cfg.PublicKey,
		VAPIDPrivateKey: cfg.PrivateKey,
		TTL:             cfg.TTL,
		Urgency:         webpush.Urgency(cfg.Urgency),
	})
	if err != nil {
		log.Printf("push: send to %s failed: %v", shortEndpoint(s.Endpoint), err)
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		// The browser revoked this endpoint. Drop it rather than retry forever.
		if p.store.DeletePushSubscriptionByID(s.ID) {
			log.Printf("push: pruned expired subscription %s", shortEndpoint(s.Endpoint))
		}
	case resp.StatusCode >= 400:
		log.Printf("push: %s returned %d", shortEndpoint(s.Endpoint), resp.StatusCode)
	}
}

// topicMatches reports whether a subscription with the given topic filter
// wants this event. An empty filter means "everything".
func topicMatches(topics []string, event string) bool {
	if len(topics) == 0 {
		return true
	}
	for _, t := range topics {
		if t == "*" || t == event {
			return true
		}
		// "service" matches "service.down", "service.recovered", …
		if len(event) > len(t) && event[:len(t)] == t && event[len(t)] == '.' {
			return true
		}
	}
	return false
}

// shortEndpoint trims a push endpoint down to something safe for a log line —
// the full URL is a bearer-style capability.
func shortEndpoint(ep string) string {
	if len(ep) <= 40 {
		return ep
	}
	return ep[:32] + "…"
}
