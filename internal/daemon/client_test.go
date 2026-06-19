package daemon

import (
	"errors"
	"fmt"
	"testing"
)

// TestProtocolFallbackEmbedded covers the recoverable-error classification the
// MCP proxy uses to decide between dialing the daemon and running the embedded
// server: a missing daemon OR a protocol-version mismatch both fall back; any
// other error is a real failure.
func TestProtocolFallbackEmbedded(t *testing.T) {
	if !ShouldFallBackToEmbedded(ErrDaemonUnavailable) {
		t.Error("ErrDaemonUnavailable must trigger embedded fallback")
	}
	// Dial wraps a protocol_mismatch ack into ErrProtocolVersionMismatch.
	wrapped := fmt.Errorf("%w: server v1 vs client v2", ErrProtocolVersionMismatch)
	if !ShouldFallBackToEmbedded(wrapped) {
		t.Error("a wrapped ErrProtocolVersionMismatch must trigger embedded fallback")
	}
	if ShouldFallBackToEmbedded(errors.New("permission denied")) {
		t.Error("an unrelated error must NOT trigger fallback (would mask a real bug)")
	}
	if ShouldFallBackToEmbedded(nil) {
		t.Error("nil must not trigger fallback")
	}
}
