package svc

import (
	"testing"

	"github.com/zzet/gortex/internal/llm"
)

// TestService_Provider exercises the accessor's nil-safety: a nil service
// and an unconfigured service both report no provider, so callers (the
// wiki enhancer) can gate on it without a panic.
func TestService_Provider(t *testing.T) {
	var nilSvc *Service
	if nilSvc.Provider() != nil {
		t.Error("nil service must return a nil provider")
	}

	s := NewService(llm.Config{}, llm.MockBackend{})
	if s.Provider() != nil {
		t.Error("a service with no configured provider must return nil")
	}
	if s.Enabled() {
		t.Error("a service with no provider must not be Enabled()")
	}
}
