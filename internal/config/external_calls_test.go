package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExternalCallSynthesis_AbsentIsDefaultOff asserts that a config with
// no `synthesize_external_calls` key leaves the pointer nil — which the
// tri-state resolver reads as "external-call synthesis OFF" — so the
// full-graph synthesis stays opt-in and imposes no per-index cost.
func TestExternalCallSynthesis_AbsentIsDefaultOff(t *testing.T) {
	cfg, err := Load(writeConfig(t, "index:\n  workers: 2\n"))
	require.NoError(t, err)

	assert.Nil(t, cfg.Index.SynthesizeExternalCalls,
		"an absent key must leave the pointer nil (not false)")
	assert.False(t, cfg.Index.ExternalCallSynthesisEnabledOrDefault(),
		"a nil flag must resolve to OFF (opt-in)")
}

// TestExternalCallSynthesis_ExplicitlyDisabled asserts an explicit
// opt-out is distinguishable from "absent" and turns synthesis off.
func TestExternalCallSynthesis_ExplicitlyDisabled(t *testing.T) {
	cfg, err := Load(writeConfig(t, "index:\n  synthesize_external_calls: false\n"))
	require.NoError(t, err)

	require.NotNil(t, cfg.Index.SynthesizeExternalCalls, "an explicit value must round-trip as a non-nil pointer")
	assert.False(t, *cfg.Index.SynthesizeExternalCalls)
	assert.False(t, cfg.Index.ExternalCallSynthesisEnabledOrDefault())
}

// TestExternalCallSynthesis_ExplicitlyEnabled asserts `true` round-trips.
func TestExternalCallSynthesis_ExplicitlyEnabled(t *testing.T) {
	cfg, err := Load(writeConfig(t, "index:\n  synthesize_external_calls: true\n"))
	require.NoError(t, err)

	require.NotNil(t, cfg.Index.SynthesizeExternalCalls)
	assert.True(t, *cfg.Index.SynthesizeExternalCalls)
	assert.True(t, cfg.Index.ExternalCallSynthesisEnabledOrDefault())
}
