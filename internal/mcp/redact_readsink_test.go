package mcp

import (
	"strings"
	"testing"
)

func TestHandleReadFile_Redact(t *testing.T) {
	s := &Server{} // nil configManager → redaction enabled by default

	const secret = "token: ghp_0123456789abcdefghijklmnopqrstuvwxyz\n"

	// A config-leaf yaml: the secret value is withheld, the key survives.
	out, did := s.maybeRedactConfigLeaf("yaml", "config/app.yaml", false, secret)
	if !did {
		t.Fatalf("expected redaction of a config-leaf secret, got none: %q", out)
	}
	if strings.Contains(out, "ghp_0123456789abcdefghijklmnopqrstuvwxyz") {
		t.Errorf("secret value survived redaction: %q", out)
	}
	if !strings.Contains(out, "token:") {
		t.Errorf("benign key framing was dropped: %q", out)
	}

	// allow_secrets bypasses redaction entirely.
	if raw, did := s.maybeRedactConfigLeaf("yaml", "config/app.yaml", true, secret); did || raw != secret {
		t.Errorf("allow_secrets should serve verbatim: did=%v out=%q", did, raw)
	}

	// A non-config-leaf file (source code) is never redacted.
	const code = "apiKey := \"ghp_0123456789abcdefghijklmnopqrstuvwxyz\"\n"
	if got, did := s.maybeRedactConfigLeaf("go", "main.go", false, code); did || got != code {
		t.Errorf("source code should not be redacted: did=%v", did)
	}
}

func TestHandleGetSymbolSource_Redact(t *testing.T) {
	s := &Server{} // nil configManager → redaction enabled by default

	// A config-leaf value served as a symbol's source (a populated .env key)
	// has its secret-shaped value withheld by default.
	const src = "stripe_key: sk-0123456789abcdefghij0123456789\n"
	out, did := s.maybeRedactConfigLeaf("dotenv", ".env", false, src)
	if !did {
		t.Fatalf("expected redaction of a config-leaf symbol value, got none: %q", out)
	}
	if strings.Contains(out, "sk-0123456789abcdefghij0123456789") {
		t.Errorf("secret value survived redaction: %q", out)
	}

	// allow_secrets serves the value verbatim.
	if raw, did := s.maybeRedactConfigLeaf("dotenv", ".env", true, src); did || raw != src {
		t.Errorf("allow_secrets should serve verbatim: did=%v out=%q", did, raw)
	}
}

func TestSmartContext_Redact(t *testing.T) {
	s := &Server{} // nil configManager → redaction enabled by default

	// smart_context always withholds config-leaf secrets from an embedded
	// source snippet (the manifest and flat embed paths both pass
	// allowSecrets=false — the explicit override lives on the read tools).
	const src = "db:\n  access_key: AKIAIOSFODNN7EXAMPLE\n"
	out, did := s.maybeRedactConfigLeaf("yaml", "deploy/secrets.yaml", false, src)
	if !did {
		t.Fatalf("smart_context should withhold a config-leaf secret, got none: %q", out)
	}
	if strings.Contains(out, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("secret value survived redaction in smart_context: %q", out)
	}
	if !strings.Contains(out, "access_key:") {
		t.Errorf("benign key framing was dropped: %q", out)
	}
}
