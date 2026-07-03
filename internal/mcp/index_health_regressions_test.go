package mcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zzet/gortex/internal/indexer"
)

// TestIndexHealth_SurfacesResolutionRegressions proves the shape-degradation
// guard counter is visible in index_health — so a ratchet the guards caught (and
// self-healed) can never again hide behind an otherwise all-green health report.
func TestIndexHealth_SurfacesResolutionRegressions(t *testing.T) {
	srv, _ := setupTestServer(t)

	payload := srv.buildIndexHealthPayload()
	require.NotNil(t, payload)
	before, ok := payload["resolution_regressions"].(int64)
	require.True(t, ok, "index_health must carry the resolution_regressions counter")

	indexer.RecordResolutionRegression()

	after, _ := srv.buildIndexHealthPayload()["resolution_regressions"].(int64)
	assert.Equal(t, before+1, after, "a recorded regression must surface in index_health")
}
