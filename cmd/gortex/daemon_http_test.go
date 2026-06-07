package main

import "testing"

// TestIsLocalhostBind covers loopback detection including host:port forms,
// so a normal loopback bind like 127.0.0.1:7411 is not mistaken for an
// externally reachable address.
func TestIsLocalhostBind(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1":      true,
		"::1":            true,
		"localhost":      true,
		"127.0.0.1:7411": true,
		"localhost:7411": true,
		"[::1]:7411":     true,
		"127.0.0.2:80":   true, // 127.0.0.0/8 is loopback
		"0.0.0.0:7411":   false,
		":7411":          false, // wildcard bind = all interfaces
		"0.0.0.0":        false,
		"192.168.1.5:80": false,
		"example.com:443": false,
	}
	for bind, want := range cases {
		if got := isLocalhostBind(bind); got != want {
			t.Errorf("isLocalhostBind(%q) = %v, want %v", bind, got, want)
		}
	}
}

// TestHTTPTokenRequirement asserts a non-localhost --http bind without a
// token is refused, while a localhost bind or a tokened bind is allowed.
func TestHTTPTokenRequirement(t *testing.T) {
	if err := httpTokenRequirementError("0.0.0.0:0", ""); err == nil {
		t.Error("non-localhost bind without a token must be refused")
	}
	if err := httpTokenRequirementError("0.0.0.0:0", "tok"); err != nil {
		t.Errorf("non-localhost bind with a token must be allowed; got %v", err)
	}
	if err := httpTokenRequirementError("127.0.0.1:7411", ""); err != nil {
		t.Errorf("localhost bind without a token must be allowed; got %v", err)
	}
	if err := httpTokenRequirementError(":7411", ""); err == nil {
		t.Error("wildcard bind without a token must be refused")
	}
}
