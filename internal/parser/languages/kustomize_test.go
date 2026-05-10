package languages

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zzet/gortex/internal/graph"
)

func TestKustomize_OverlayWithBasesPatchesAndGenerators(t *testing.T) {
	src := []byte(`apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
namespace: staging
namePrefix: stg-
resources:
- ../base
- service.yaml
- ingress.yaml
patches:
- path: deployment-replicas.yaml
- path: service-port.yaml
configMapGenerator:
- name: feature-flags
  literals:
  - ENABLE_BETA=true
secretGenerator:
- name: jwt
  literals:
  - secret=hunter2
`)
	e := NewYAMLExtractor()
	result, err := e.Extract("k8s/overlays/staging/kustomization.yaml", src)
	require.NoError(t, err)

	nodes := nodesByID(result.Nodes)
	overlayID := "kustomize::k8s/overlays/staging"
	require.Contains(t, nodes, overlayID)
	assert.Equal(t, graph.KindKustomization, nodes[overlayID].Kind)

	// Base overlay (../base relative to overlays/staging) → k8s/overlays/base.
	baseID := "kustomize::k8s/overlays/base"
	assert.True(t, hasEdgeBetween(result.Edges, graph.EdgeDependsOn, overlayID, baseID),
		"overlay should depend on its base")

	// Resource files become EdgeReferences targets.
	assert.True(t, hasEdgeBetween(result.Edges, graph.EdgeReferences, overlayID,
		"k8s/overlays/staging/service.yaml"))
	assert.True(t, hasEdgeBetween(result.Edges, graph.EdgeReferences, overlayID,
		"k8s/overlays/staging/ingress.yaml"))

	// Patches.
	assert.True(t, hasEdgeBetween(result.Edges, graph.EdgeReferences, overlayID,
		"k8s/overlays/staging/deployment-replicas.yaml"))

	// Generators.
	assert.True(t, hasEdgeBetween(result.Edges, graph.EdgeDependsOn, overlayID,
		"k8s::ConfigMap::_default::feature-flags"))
	assert.True(t, hasEdgeBetween(result.Edges, graph.EdgeDependsOn, overlayID,
		"k8s::Secret::_default::jwt"))
}

func TestKustomize_BareKustomizationFile(t *testing.T) {
	src := []byte(`resources:
- deployment.yaml
`)
	e := NewYAMLExtractor()
	result, err := e.Extract("svc/Kustomization", src)
	require.NoError(t, err)

	overlayID := "kustomize::svc"
	require.Contains(t, nodesByID(result.Nodes), overlayID,
		"Kustomization basename should dispatch into the kustomize path")
}
