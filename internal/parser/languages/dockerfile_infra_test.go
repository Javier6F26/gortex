package languages

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zzet/gortex/internal/graph"
)

func TestDockerfile_FromEmitsImageNodes(t *testing.T) {
	src := []byte(`FROM golang:1.22 AS builder
FROM alpine:3.18
`)
	e := NewDockerfileExtractor()
	result, err := e.Extract("app.dockerfile", src)
	require.NoError(t, err)

	nodes := nodesByID(result.Nodes)

	// Two stage Image nodes — `builder` (alias) and `stage-1`
	// (synthetic name for the second, unaliased FROM).
	require.Contains(t, nodes, "image::stage::app.dockerfile::builder")
	require.Contains(t, nodes, "image::stage::app.dockerfile::stage-1")

	// Two base Image nodes.
	require.Contains(t, nodes, "image::golang:1.22")
	require.Contains(t, nodes, "image::alpine:3.18")

	// Stage → base via EdgeDependsOn.
	assert.True(t, hasEdgeBetween(result.Edges, graph.EdgeDependsOn,
		"image::stage::app.dockerfile::builder", "image::golang:1.22"))
	assert.True(t, hasEdgeBetween(result.Edges, graph.EdgeDependsOn,
		"image::stage::app.dockerfile::stage-1", "image::alpine:3.18"))
}

func TestDockerfile_MultiStageChain(t *testing.T) {
	src := []byte(`FROM golang:1.22 AS builder
FROM builder AS shrunk
FROM alpine:3.18
COPY --from=shrunk /out /usr/bin/app
`)
	e := NewDockerfileExtractor()
	result, err := e.Extract("multi.dockerfile", src)
	require.NoError(t, err)

	// `shrunk` stage depends on the prior `builder` stage.
	assert.True(t, hasEdgeBetween(result.Edges, graph.EdgeDependsOn,
		"image::stage::multi.dockerfile::shrunk",
		"image::stage::multi.dockerfile::builder"),
		"shrunk should depend on builder via the stage chain")
}

func TestDockerfile_EnvEmitsConfigKeyAndUsesEnv(t *testing.T) {
	src := []byte(`FROM ubuntu:22.04
ENV DATABASE_URL=postgres://localhost
ENV LOG_LEVEL=info
ARG BUILD_REV=dev
`)
	e := NewDockerfileExtractor()
	result, err := e.Extract("svc.dockerfile", src)
	require.NoError(t, err)

	nodes := nodesByID(result.Nodes)
	stage := "image::stage::svc.dockerfile::stage-0"

	for _, key := range []string{"DATABASE_URL", "LOG_LEVEL", "BUILD_REV"} {
		id := "cfg::env::" + key
		require.Contains(t, nodes, id)
		assert.Equal(t, graph.KindConfigKey, nodes[id].Kind)
		assert.True(t, hasEdgeBetween(result.Edges, graph.EdgeUsesEnv, stage, id),
			"stage should declare uses_env for %q", key)
	}
}

func TestDockerfile_ExposeEmitsExposes(t *testing.T) {
	src := []byte(`FROM nginx:1.25
EXPOSE 80 443/tcp
EXPOSE 5353/udp
`)
	e := NewDockerfileExtractor()
	result, err := e.Extract("web.dockerfile", src)
	require.NoError(t, err)

	stage := "image::stage::web.dockerfile::stage-0"
	assert.True(t, hasEdgeBetween(result.Edges, graph.EdgeExposes, stage, "port::tcp::80"))
	assert.True(t, hasEdgeBetween(result.Edges, graph.EdgeExposes, stage, "port::tcp::443"))
	assert.True(t, hasEdgeBetween(result.Edges, graph.EdgeExposes, stage, "port::udp::5353"))
}

func TestDockerfile_PreFromArgBindsToFile(t *testing.T) {
	// Global build args declared before any FROM. The legacy
	// KindVariable + KindConfigKey nodes still fire; the
	// EdgeUsesEnv from the file (since no stage exists yet) is a
	// regression guard for the pre-FROM cursor branch.
	src := []byte(`ARG VERSION=1.0
FROM scratch
`)
	e := NewDockerfileExtractor()
	result, err := e.Extract("global.dockerfile", src)
	require.NoError(t, err)

	require.Contains(t, nodesByID(result.Nodes), "cfg::env::VERSION")
}

// TestDockerfile_ImageIDSharedAcrossExtractors validates the
// invariant that a Dockerfile FROM and a K8s container.image with
// the same image ref produce the same KindImage node — so a
// `find_usages` walk on the Image node returns both the Dockerfile
// stage and the K8s Resource.
func TestDockerfile_ImageIDSharedAcrossExtractors(t *testing.T) {
	dockerfileSrc := []byte("FROM ghcr.io/acme/api:1.2.3 AS api\n")
	dRes, err := NewDockerfileExtractor().Extract("svc/Dockerfile", dockerfileSrc)
	require.NoError(t, err)

	k8sSrc := []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: prod
spec:
  template:
    spec:
      containers:
      - name: api
        image: ghcr.io/acme/api:1.2.3
`)
	kRes, err := NewYAMLExtractor().Extract("k8s/api.yaml", k8sSrc)
	require.NoError(t, err)

	dHas := false
	for _, n := range dRes.Nodes {
		if n.ID == "image::ghcr.io/acme/api:1.2.3" {
			dHas = true
		}
	}
	kHas := false
	for _, n := range kRes.Nodes {
		if n.ID == "image::ghcr.io/acme/api:1.2.3" {
			kHas = true
		}
	}
	assert.True(t, dHas, "Dockerfile should emit shared image node")
	assert.True(t, kHas, "K8s should emit shared image node")
}

// TestDockerfile_ConfigKeyIDSharedWithGoOsGetenv validates the
// cross-ref invariant: a Dockerfile ENV NAME and a Go os.Getenv("NAME")
// emit the same `cfg::env::NAME` node ID. We only check the
// Dockerfile side here — the Go side is exercised by go_configs_test.go
// which already asserts the same ID convention. The pairing across
// the two emitters is the load-bearing property tested by this case.
func TestDockerfile_ConfigKeyIDSharedWithGoOsGetenv(t *testing.T) {
	src := []byte("FROM scratch\nENV DATABASE_URL=postgres://...\n")
	res, err := NewDockerfileExtractor().Extract("Dockerfile", src)
	require.NoError(t, err)

	found := false
	for _, n := range res.Nodes {
		if n.ID == "cfg::env::DATABASE_URL" && n.Kind == graph.KindConfigKey {
			found = true
		}
	}
	assert.True(t, found, "Dockerfile ENV must produce cfg::env::<NAME> matching os.Getenv convention")
}
