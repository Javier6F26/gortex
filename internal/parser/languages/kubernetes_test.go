package languages

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zzet/gortex/internal/graph"
)

// nodesByID indexes a slice of nodes by ID. The K8s extractor emits
// duplicate config_key nodes when multiple containers declare the
// same env var; this is intentional (the graph ingester dedupes), so
// tests treat IDs as a set rather than a multiset.
func nodesByID(ns []*graph.Node) map[string]*graph.Node {
	m := make(map[string]*graph.Node, len(ns))
	for _, n := range ns {
		m[n.ID] = n
	}
	return m
}

// hasEdgeBetween returns true when at least one edge of the given
// kind links from→to. Distinct from the package-local `hasEdge`
// helper in go_dataflow_test.go (different signature).
func hasEdgeBetween(es []*graph.Edge, kind graph.EdgeKind, from, to string) bool {
	for _, e := range es {
		if e.Kind == kind && e.From == from && e.To == to {
			return true
		}
	}
	return false
}

func TestK8s_DeploymentEmitsResourceImageAndEnv(t *testing.T) {
	src := []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: prod
  labels:
    app: api
spec:
  template:
    spec:
      containers:
      - name: api
        image: ghcr.io/acme/api:1.2.3
        ports:
        - containerPort: 8080
          protocol: TCP
        env:
        - name: DATABASE_URL
          value: postgres://...
        - name: JWT_SECRET
          valueFrom:
            secretKeyRef:
              name: api-secrets
              key: jwt
        - name: FEATURE_FLAGS
          valueFrom:
            configMapKeyRef:
              name: api-config
              key: features
        envFrom:
        - configMapRef:
            name: shared-config
        - secretRef:
            name: shared-secrets
`)
	e := NewYAMLExtractor()
	result, err := e.Extract("k8s/api.yaml", src)
	require.NoError(t, err)

	nodes := nodesByID(result.Nodes)
	resID := "k8s::Deployment::prod::api"
	require.Contains(t, nodes, resID)
	assert.Equal(t, graph.KindResource, nodes[resID].Kind)
	assert.Equal(t, "Deployment/api", nodes[resID].QualName)

	imageID := "image::ghcr.io/acme/api:1.2.3"
	require.Contains(t, nodes, imageID, "should emit Image node for container.image")
	assert.Equal(t, graph.KindImage, nodes[imageID].Kind)

	// Resource → Image via EdgeDependsOn.
	assert.True(t, hasEdgeBetween(result.Edges, graph.EdgeDependsOn, resID, imageID))

	// Env vars surface as cfg::env::<NAME> KindConfigKey nodes.
	for _, key := range []string{"DATABASE_URL", "JWT_SECRET", "FEATURE_FLAGS"} {
		id := "cfg::env::" + key
		require.Contains(t, nodes, id, "missing env config_key %q", key)
		assert.Equal(t, graph.KindConfigKey, nodes[id].Kind)
		assert.True(t, hasEdgeBetween(result.Edges, graph.EdgeUsesEnv, resID, id),
			"missing EdgeUsesEnv to %q", id)
	}

	// EdgeConfigures wires valueFrom + envFrom to source resources.
	assert.True(t, hasEdgeBetween(result.Edges, graph.EdgeConfigures, resID,
		"k8s::ConfigMap::prod::api-config"))
	assert.True(t, hasEdgeBetween(result.Edges, graph.EdgeConfigures, resID,
		"k8s::Secret::prod::api-secrets"))
	assert.True(t, hasEdgeBetween(result.Edges, graph.EdgeConfigures, resID,
		"k8s::ConfigMap::prod::shared-config"))
	assert.True(t, hasEdgeBetween(result.Edges, graph.EdgeConfigures, resID,
		"k8s::Secret::prod::shared-secrets"))

	// EdgeExposes surfaces ports.
	exposes := edgesOfKind(result.Edges, graph.EdgeExposes)
	require.GreaterOrEqual(t, len(exposes), 1)
	assert.Equal(t, "port::tcp::8080", exposes[0].To)
}

func TestK8s_VolumesEmitMounts(t *testing.T) {
	src := []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: prod
spec:
  template:
    spec:
      containers:
      - name: web
        image: nginx:1.25
      volumes:
      - name: cfg
        configMap:
          name: web-config
      - name: tls
        secret:
          secretName: web-tls
      - name: data
        persistentVolumeClaim:
          claimName: web-data
`)
	e := NewYAMLExtractor()
	result, err := e.Extract("k8s/web.yaml", src)
	require.NoError(t, err)

	res := "k8s::Deployment::prod::web"
	assert.True(t, hasEdgeBetween(result.Edges, graph.EdgeMounts, res,
		"k8s::ConfigMap::prod::web-config"))
	assert.True(t, hasEdgeBetween(result.Edges, graph.EdgeMounts, res,
		"k8s::Secret::prod::web-tls"))
	assert.True(t, hasEdgeBetween(result.Edges, graph.EdgeMounts, res,
		"k8s::PersistentVolumeClaim::prod::web-data"))
}

func TestK8s_ServiceExposesPorts(t *testing.T) {
	src := []byte(`apiVersion: v1
kind: Service
metadata:
  name: api
  namespace: prod
spec:
  ports:
  - name: http
    port: 80
    targetPort: 8080
  - name: grpc
    port: 50051
    protocol: TCP
`)
	e := NewYAMLExtractor()
	result, err := e.Extract("k8s/svc.yaml", src)
	require.NoError(t, err)

	res := "k8s::Service::prod::api"
	assert.True(t, hasEdgeBetween(result.Edges, graph.EdgeExposes, res, "port::tcp::80"))
	assert.True(t, hasEdgeBetween(result.Edges, graph.EdgeExposes, res, "port::tcp::50051"))
}

func TestK8s_IngressDependsOnService(t *testing.T) {
	src := []byte(`apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: web
  namespace: prod
spec:
  rules:
  - host: api.example.com
    http:
      paths:
      - path: /v1
        pathType: Prefix
        backend:
          service:
            name: api
            port:
              number: 80
      - path: /admin
        pathType: Prefix
        backend:
          service:
            name: admin
            port:
              number: 8080
`)
	e := NewYAMLExtractor()
	result, err := e.Extract("k8s/ing.yaml", src)
	require.NoError(t, err)

	res := "k8s::Ingress::prod::web"
	assert.True(t, hasEdgeBetween(result.Edges, graph.EdgeDependsOn, res, "k8s::Service::prod::api"))
	assert.True(t, hasEdgeBetween(result.Edges, graph.EdgeDependsOn, res, "k8s::Service::prod::admin"))
}

func TestK8s_ConfigMapAndSecretEmitConfigKeys(t *testing.T) {
	src := []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: api-config
  namespace: prod
data:
  features: "alpha,beta"
  log_level: info
---
apiVersion: v1
kind: Secret
metadata:
  name: api-secrets
  namespace: prod
data:
  jwt: c2VjcmV0
stringData:
  api-key: literal
`)
	e := NewYAMLExtractor()
	result, err := e.Extract("k8s/cm.yaml", src)
	require.NoError(t, err)

	nodes := nodesByID(result.Nodes)
	require.Contains(t, nodes, "cfg::k8s_cm::api-config::features")
	require.Contains(t, nodes, "cfg::k8s_cm::api-config::log_level")
	require.Contains(t, nodes, "cfg::k8s_secret::api-secrets::jwt")
	require.Contains(t, nodes, "cfg::k8s_secret::api-secrets::api-key")

	cmRes := "k8s::ConfigMap::prod::api-config"
	assert.True(t, hasEdgeBetween(result.Edges, graph.EdgeDefines, cmRes,
		"cfg::k8s_cm::api-config::features"))
}

func TestK8s_CronJobReachesPodSpec(t *testing.T) {
	src := []byte(`apiVersion: batch/v1
kind: CronJob
metadata:
  name: nightly
spec:
  schedule: "0 0 * * *"
  jobTemplate:
    spec:
      template:
        spec:
          containers:
          - name: run
            image: ghcr.io/acme/job:latest
            env:
            - name: TZ
              value: UTC
`)
	e := NewYAMLExtractor()
	result, err := e.Extract("k8s/cron.yaml", src)
	require.NoError(t, err)

	nodes := nodesByID(result.Nodes)
	res := "k8s::CronJob::_default::nightly"
	require.Contains(t, nodes, res)
	require.Contains(t, nodes, "image::ghcr.io/acme/job:latest")
	require.Contains(t, nodes, "cfg::env::TZ")
	assert.True(t, hasEdgeBetween(result.Edges, graph.EdgeUsesEnv, res, "cfg::env::TZ"))
}

func TestK8s_NonK8sYAMLFallsThroughToGeneric(t *testing.T) {
	src := []byte(`name: my-app
version: "1.0"
services:
  web:
    image: nginx
`)
	e := NewYAMLExtractor()
	result, err := e.Extract("docker-compose.yml", src)
	require.NoError(t, err)

	// Generic walker should still emit KindVariable for top-level keys.
	vars := nodesOfKind(result.Nodes, graph.KindVariable)
	assert.GreaterOrEqual(t, len(vars), 3)

	// And no Resource/Image/ConfigKey nodes since this isn't K8s.
	assert.Empty(t, nodesOfKind(result.Nodes, graph.KindResource))
}
