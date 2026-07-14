package daemon

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A follow-mode daemon refuses every mutating control RPC with a typed
// follow_mode error and never reaches the controller; status still works.
func TestDaemon_FollowRefusesMutatingControl(t *testing.T) {
	ctrl := &fakeController{}
	srv, socket := newDaemon(t, ctrl)
	srv.Follow = true

	c, err := DialTo(socket, Handshake{Mode: ModeControl, ClientName: "cli"})
	require.NoError(t, err)
	defer c.Close()

	for _, kind := range []string{ControlTrack, ControlUntrack, ControlReload, ControlProxy} {
		resp, err := c.Control(kind, TrackParams{Path: "/tmp/x", Name: "x"})
		require.NoError(t, err, "%s transport error", kind)
		assert.False(t, resp.OK, "%s should be refused", kind)
		assert.Equal(t, ErrFollowMode, resp.ErrorCode, "%s should return follow_mode error code", kind)
	}

	// The controller must not have been touched by any refused RPC.
	ctrl.mu.Lock()
	assert.Empty(t, ctrl.trackCalls, "track reached the controller despite follow mode")
	assert.Empty(t, ctrl.untrackCalls, "untrack reached the controller despite follow mode")
	ctrl.mu.Unlock()

	// Status still works and reports follow mode.
	statusResp, err := c.Control(ControlStatus, nil)
	require.NoError(t, err)
	require.True(t, statusResp.OK, "status should work in follow mode")
}
