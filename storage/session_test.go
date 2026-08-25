package storage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionRoundTrip(t *testing.T) {
	s := newTestStorage(t)

	sess, err := s.CreateSession("admin", "curl/8", "1.2.3.4", time.Hour)
	require.NoError(t, err)
	assert.NotEmpty(t, sess.Token)
	assert.Equal(t, "admin", sess.Username)

	got := s.GetSession(sess.Token)
	require.NotNil(t, got)
	assert.Equal(t, "admin", got.Username)
	assert.Equal(t, "1.2.3.4", got.IP)
}

func TestSessionTokensAreUnpredictable(t *testing.T) {
	s := newTestStorage(t)
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		sess, err := s.CreateSession("u", "", "", time.Hour)
		require.NoError(t, err)
		assert.False(t, seen[sess.Token], "token collision")
		assert.GreaterOrEqual(t, len(sess.Token), 43, "32 bytes base64url")
		seen[sess.Token] = true
	}
}

func TestExpiredSessionIsRejectedAndRemoved(t *testing.T) {
	s := newTestStorage(t)
	sess, err := s.CreateSession("admin", "", "", -time.Minute) // already expired
	require.NoError(t, err)

	assert.Nil(t, s.GetSession(sess.Token), "an expired session must not authenticate")
	assert.Equal(t, 0, s.CountSessions(), "and must be cleaned up on read")
}

func TestUnknownTokenIsRejected(t *testing.T) {
	s := newTestStorage(t)
	assert.Nil(t, s.GetSession("not-a-real-token"))
	assert.Nil(t, s.GetSession(""))
}

func TestDeleteSessionIsLogout(t *testing.T) {
	s := newTestStorage(t)
	sess, err := s.CreateSession("admin", "", "", time.Hour)
	require.NoError(t, err)

	assert.True(t, s.DeleteSession(sess.Token))
	assert.Nil(t, s.GetSession(sess.Token), "logout must invalidate server-side, not just drop the cookie")
	assert.False(t, s.DeleteSession(sess.Token), "already gone")
}

func TestDeleteSessionsForUserRevokesEveryBrowser(t *testing.T) {
	s := newTestStorage(t)
	a1, _ := s.CreateSession("admin", "chrome", "", time.Hour)
	a2, _ := s.CreateSession("admin", "phone", "", time.Hour)
	other, _ := s.CreateSession("ops", "", "", time.Hour)

	assert.Equal(t, 2, s.DeleteSessionsForUser("admin"))
	assert.Nil(t, s.GetSession(a1.Token))
	assert.Nil(t, s.GetSession(a2.Token))
	assert.NotNil(t, s.GetSession(other.Token), "other users are untouched")
}

func TestPurgeExpiredSessions(t *testing.T) {
	s := newTestStorage(t)
	live, _ := s.CreateSession("a", "", "", time.Hour)
	_, _ = s.CreateSession("b", "", "", -time.Hour)
	_, _ = s.CreateSession("c", "", "", -time.Hour)

	assert.Equal(t, 2, s.PurgeExpiredSessions())
	assert.Equal(t, 1, s.CountSessions())
	assert.NotNil(t, s.GetSession(live.Token))
}
