package telemetry

import "testing"

func TestResolveConsent(t *testing.T) {
	cases := []struct {
		name       string
		cfg        ConsentConfig
		env        map[string]string
		wantOn     bool
		wantSource Source
	}{
		// Rung 4 — default is off (opt-in).
		{"default off", ConsentConfig{}, nil, false, SourceDefault},

		// Rung 3 — config decides when no env signal.
		{"config on", ConsentConfig{Enabled: new(true)}, nil, true, SourceConfig},
		{"config off", ConsentConfig{Enabled: new(false)}, nil, false, SourceConfig},

		// Rung 2 — DO_NOT_TRACK forces off.
		{"dnt=1 off", ConsentConfig{}, map[string]string{"DO_NOT_TRACK": "1"}, false, SourceDoNotTrack},
		{"dnt=true off", ConsentConfig{}, map[string]string{"DO_NOT_TRACK": "true"}, false, SourceDoNotTrack},
		{"dnt beats config-on", ConsentConfig{Enabled: new(true)}, map[string]string{"DO_NOT_TRACK": "1"}, false, SourceDoNotTrack},
		{"dnt=0 not asserted, config wins", ConsentConfig{Enabled: new(true)}, map[string]string{"DO_NOT_TRACK": "0"}, true, SourceConfig},
		{"dnt=false not asserted, default", ConsentConfig{}, map[string]string{"DO_NOT_TRACK": "false"}, false, SourceDefault},

		// Rung 1 — explicit Gortex env override wins over everything.
		{"env on beats default", ConsentConfig{}, map[string]string{"GORTEX_TELEMETRY": "on"}, true, SourceEnv},
		{"env off beats config-on", ConsentConfig{Enabled: new(true)}, map[string]string{"GORTEX_TELEMETRY": "off"}, false, SourceEnv},
		{"env on beats dnt", ConsentConfig{}, map[string]string{"GORTEX_TELEMETRY": "1", "DO_NOT_TRACK": "1"}, true, SourceEnv},
		{"env unrecognised falls through to dnt", ConsentConfig{}, map[string]string{"GORTEX_TELEMETRY": "maybe", "DO_NOT_TRACK": "1"}, false, SourceDoNotTrack},
		{"env empty falls through to config", ConsentConfig{Enabled: new(true)}, map[string]string{"GORTEX_TELEMETRY": ""}, true, SourceConfig},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			getenv := func(k string) string { return c.env[k] }
			got := ResolveConsent(c.cfg, getenv)
			if got.Enabled != c.wantOn {
				t.Errorf("Enabled = %v, want %v", got.Enabled, c.wantOn)
			}
			if got.Source != c.wantSource {
				t.Errorf("Source = %q, want %q", got.Source, c.wantSource)
			}
		})
	}
}

func TestResolveConsentDefaultsToOsGetenv(t *testing.T) {
	// A nil getenv must not panic — it falls back to os.Getenv. In the test
	// environment neither var is set, so consent is the default (off).
	t.Setenv("GORTEX_TELEMETRY", "")
	t.Setenv("DO_NOT_TRACK", "")
	got := ResolveConsent(ConsentConfig{}, nil)
	if got.Enabled || got.Source != SourceDefault {
		t.Errorf("nil getenv: got %+v, want {false default}", got)
	}
}

func TestParseConsentBool(t *testing.T) {
	on := []string{"1", "true", "TRUE", "on", "Yes", " enable ", "enabled"}
	off := []string{"0", "false", "Off", "no", "disable", "DISABLED"}
	none := []string{"", "maybe", "2", "x"}
	for _, s := range on {
		if v, ok := parseConsentBool(s); !ok || !v {
			t.Errorf("parseConsentBool(%q) = (%v,%v), want (true,true)", s, v, ok)
		}
	}
	for _, s := range off {
		if v, ok := parseConsentBool(s); !ok || v {
			t.Errorf("parseConsentBool(%q) = (%v,%v), want (false,true)", s, v, ok)
		}
	}
	for _, s := range none {
		if _, ok := parseConsentBool(s); ok {
			t.Errorf("parseConsentBool(%q) ok = true, want false", s)
		}
	}
}
