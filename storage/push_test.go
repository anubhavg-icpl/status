package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestStorage(t *testing.T) *Storage {
	t.Helper()
	s, err := NewStorage(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSavePushSubscriptionUpsertsByEndpoint(t *testing.T) {
	s := newTestStorage(t)

	first, err := s.SavePushSubscription(PushSubscription{
		Endpoint: "https://fcm.googleapis.com/fcm/send/abc",
		P256dh:   "key1",
		Auth:     "auth1",
	})
	require.NoError(t, err)
	require.Equal(t, 1, s.CountPushSubscriptions())

	// Re-subscribing from the same browser must refresh, not duplicate.
	second, err := s.SavePushSubscription(PushSubscription{
		Endpoint: "https://fcm.googleapis.com/fcm/send/abc",
		P256dh:   "key2",
		Auth:     "auth2",
	})
	require.NoError(t, err)

	assert.Equal(t, 1, s.CountPushSubscriptions(), "same endpoint, same row")
	assert.Equal(t, first.ID, second.ID)
	// Compare with Equal, not ==: the persisted copy loses its monotonic
	// reading on the JSON round-trip while the in-memory one keeps it.
	assert.True(t, first.CreatedAt.Equal(second.CreatedAt), "original registration time is kept")
	assert.False(t, second.LastSeen.Before(first.LastSeen))

	subs := s.ListPushSubscriptions()
	require.Len(t, subs, 1)
	assert.Equal(t, "key2", subs[0].P256dh, "the refreshed keys win")
}

func TestDeletePushSubscription(t *testing.T) {
	s := newTestStorage(t)
	_, err := s.SavePushSubscription(PushSubscription{
		Endpoint: "https://example.com/push/1", P256dh: "k", Auth: "a",
	})
	require.NoError(t, err)

	assert.False(t, s.DeletePushSubscription("https://example.com/push/unknown"))
	assert.True(t, s.DeletePushSubscription("https://example.com/push/1"))
	assert.Equal(t, 0, s.CountPushSubscriptions())
	assert.False(t, s.DeletePushSubscription("https://example.com/push/1"), "already gone")
}

func TestPushSubscriptionIDIsStableAndDistinct(t *testing.T) {
	a := PushSubscriptionID("https://example.com/a")
	assert.Equal(t, a, PushSubscriptionID("https://example.com/a"))
	assert.NotEqual(t, a, PushSubscriptionID("https://example.com/b"))
	assert.Len(t, a, 32, "16 bytes hex-encoded")
}

func TestSettingsRoundTrip(t *testing.T) {
	s := newTestStorage(t)

	assert.Equal(t, "", s.GetSetting("vapid_public_key"), "missing key reads empty")
	require.NoError(t, s.SetSetting("vapid_public_key", "BKL5..."))
	assert.Equal(t, "BKL5...", s.GetSetting("vapid_public_key"))

	require.NoError(t, s.SetSetting("vapid_public_key", "replaced"))
	assert.Equal(t, "replaced", s.GetSetting("vapid_public_key"))
}

func TestListPushSubscriptionsEmpty(t *testing.T) {
	s := newTestStorage(t)
	assert.Empty(t, s.ListPushSubscriptions())
	assert.Equal(t, 0, s.CountPushSubscriptions())
}
