// Package telemetry implements opt-in, anonymous usage telemetry for Gortex.
//
// The cardinal rule is opt-in: nothing is recorded, buffered, or sent unless
// consent resolves to enabled, and the default at every layer is off. This
// file holds only the consent decision — the single source of truth every
// other telemetry path consults before doing anything.
package telemetry

import (
	"os"
	"strings"
)

// Source identifies which precedence rung decided consent — surfaced by
// `gortex telemetry status` and useful when a user asks "why is this on/off".
type Source string

const (
	// SourceEnv: the GORTEX_TELEMETRY environment override.
	SourceEnv Source = "env"
	// SourceDoNotTrack: the cross-tool DO_NOT_TRACK standard.
	SourceDoNotTrack Source = "do_not_track"
	// SourceConfig: the persisted telemetry.enabled config value.
	SourceConfig Source = "config"
	// SourceDefault: no signal at any rung — the opt-in default (off).
	SourceDefault Source = "default"
)

// Consent is the resolved telemetry decision plus the rung that produced it.
type Consent struct {
	Enabled bool
	Source  Source
}

// ConsentConfig is the persisted-config view the resolver needs: the explicit
// telemetry.enabled value, or nil when the config says nothing about it (so the
// resolver can fall through to the default rather than treating "unset" as off).
type ConsentConfig struct {
	Enabled *bool
}

// Environment variables consulted by the resolver.
const (
	// EnvTelemetry is Gortex's explicit per-process override.
	EnvTelemetry = "GORTEX_TELEMETRY"
	// EnvDoNotTrack is the cross-tool standard (https://consoledonottrack.com).
	EnvDoNotTrack = "DO_NOT_TRACK"
)

// ResolveConsent decides whether anonymous usage telemetry is enabled, applying
// a fixed four-rung precedence (highest wins). Telemetry is opt-in: with no
// signal at any rung it is off.
//
//  1. GORTEX_TELEMETRY — an explicit per-process override. A recognised off
//     value (0/false/off/no/disable) forces off; an on value (1/true/on/yes/
//     enable) forces on. Highest so a user can always override everything,
//     including a global DO_NOT_TRACK, for one invocation.
//  2. DO_NOT_TRACK — the cross-tool privacy standard. Any value other than
//     unset / "0" / "false" forces off. It can only ever disable, never enable.
//  3. config telemetry.enabled — the persisted user choice.
//  4. default — off.
//
// getenv defaults to os.Getenv when nil; tests inject a fake lookup.
func ResolveConsent(cfg ConsentConfig, getenv func(string) string) Consent {
	if getenv == nil {
		getenv = os.Getenv
	}

	// Rung 1: explicit Gortex env override (can force on or off).
	if v, ok := parseConsentBool(getenv(EnvTelemetry)); ok {
		return Consent{Enabled: v, Source: SourceEnv}
	}

	// Rung 2: DO_NOT_TRACK (off-only signal).
	if doNotTrackAsserted(getenv(EnvDoNotTrack)) {
		return Consent{Enabled: false, Source: SourceDoNotTrack}
	}

	// Rung 3: persisted config.
	if cfg.Enabled != nil {
		return Consent{Enabled: *cfg.Enabled, Source: SourceConfig}
	}

	// Rung 4: opt-in default.
	return Consent{Enabled: false, Source: SourceDefault}
}

// parseConsentBool maps an on/off-ish string to a bool. ok is false for "" or
// an unrecognised value, so the caller falls through to the next rung instead
// of treating noise as a decision.
func parseConsentBool(s string) (val, ok bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "on", "yes", "enable", "enabled":
		return true, true
	case "0", "false", "off", "no", "disable", "disabled":
		return false, true
	default:
		return false, false
	}
}

// doNotTrackAsserted reports whether DO_NOT_TRACK requests no tracking. Per the
// standard a set value that is not "0"/"false" means "do not track"; unset,
// "0", and "false" do not assert it.
func doNotTrackAsserted(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return s != "" && s != "0" && s != "false"
}
