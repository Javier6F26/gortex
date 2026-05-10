package languages

import (
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// extractKubernetesYAML scans a YAML stream for Kubernetes manifests
// (documents that have both `apiVersion:` and `kind:` keys) and emits
// KindResource / KindImage / KindConfigKey nodes plus the
// infrastructure edges (EdgeConfigures, EdgeMounts, EdgeExposes,
// EdgeDependsOn, EdgeUsesEnv).
//
// Returns true when at least one manifest was successfully extracted.
// The caller (YAMLExtractor.Extract) uses this signal to skip the
// generic top-level-keys fallback for K8s files — those keys would
// pollute search results without adding information beyond what the
// dedicated graph already captures. When the file has no manifests
// (or yaml parsing fails) the function returns false so the caller
// can run the generic path.
func extractKubernetesYAML(filePath, fileID string, src []byte, result *parser.ExtractionResult) bool {
	dec := yaml.NewDecoder(strings.NewReader(string(src)))
	emitted := false
	for {
		var doc yaml.Node
		if err := dec.Decode(&doc); err != nil {
			break
		}
		root := documentMapping(&doc)
		if root == nil {
			continue
		}
		apiVersion := scalarOf(mappingGet(root, "apiVersion"))
		kind := scalarOf(mappingGet(root, "kind"))
		if apiVersion == "" || kind == "" {
			continue
		}
		extractK8sDoc(filePath, fileID, root, apiVersion, kind, result)
		emitted = true
	}
	return emitted
}

// extractK8sDoc emits the graph fragment for a single K8s document.
func extractK8sDoc(filePath, fileID string, root *yaml.Node, apiVersion, kind string, result *parser.ExtractionResult) {
	meta := mappingGet(root, "metadata")
	name := scalarOf(mappingGet(meta, "name"))
	namespace := scalarOf(mappingGet(meta, "namespace"))
	if namespace == "" {
		namespace = "_default"
	}
	if name == "" {
		// Some manifests (e.g. List, Kustomization-as-resource)
		// genuinely lack a metadata.name. Synthesise a stable
		// fallback so the node ID stays unique.
		name = fmt.Sprintf("__noname_%d", root.Line)
	}
	resID := k8sResourceID(kind, namespace, name)
	resLine := root.Line
	if resLine <= 0 {
		resLine = 1
	}
	resMeta := map[string]any{
		"api_version": apiVersion,
		"k8s_kind":    kind,
		"namespace":   namespace,
	}
	if labels := mappingGet(meta, "labels"); labels != nil && labels.Kind == yaml.MappingNode {
		labelMap := make(map[string]string, len(labels.Content)/2)
		for i := 0; i+1 < len(labels.Content); i += 2 {
			k := labels.Content[i].Value
			v := labels.Content[i+1].Value
			if k != "" {
				labelMap[k] = v
			}
		}
		if len(labelMap) > 0 {
			resMeta["labels"] = labelMap
		}
	}
	result.Nodes = append(result.Nodes, &graph.Node{
		ID: resID, Kind: graph.KindResource, Name: name,
		QualName: kind + "/" + name,
		FilePath: filePath, StartLine: resLine, EndLine: resLine,
		Language: "yaml",
		Meta:     resMeta,
	})
	result.Edges = append(result.Edges, &graph.Edge{
		From: fileID, To: resID, Kind: graph.EdgeDefines,
		FilePath: filePath, Line: resLine,
	})

	switch kind {
	case "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job", "Pod":
		extractK8sPodSpec(filePath, resID, namespace, podSpecOf(root, kind), result)
	case "CronJob":
		// CronJob: spec.jobTemplate.spec.template.spec
		spec := mappingGet(root, "spec")
		jt := mappingGet(spec, "jobTemplate")
		jtSpec := mappingGet(jt, "spec")
		template := mappingGet(jtSpec, "template")
		extractK8sPodSpec(filePath, resID, namespace, mappingGet(template, "spec"), result)
	case "Service":
		extractK8sServicePorts(filePath, resID, mappingGet(root, "spec"), result)
	case "Ingress":
		extractK8sIngressBackends(filePath, resID, namespace, mappingGet(root, "spec"), result)
	case "ConfigMap":
		extractK8sConfigMapData(filePath, resID, name, mappingGet(root, "data"), "k8s_cm", result)
	case "Secret":
		extractK8sConfigMapData(filePath, resID, name, mappingGet(root, "data"), "k8s_secret", result)
		extractK8sConfigMapData(filePath, resID, name, mappingGet(root, "stringData"), "k8s_secret", result)
	}
}

// podSpecOf returns the PodSpec node for a workload manifest. For Pod
// the spec lives at `.spec`; for the rest it's at `.spec.template.spec`.
func podSpecOf(root *yaml.Node, kind string) *yaml.Node {
	spec := mappingGet(root, "spec")
	if spec == nil {
		return nil
	}
	if kind == "Pod" {
		return spec
	}
	template := mappingGet(spec, "template")
	return mappingGet(template, "spec")
}

// extractK8sPodSpec walks containers and volumes of a PodSpec.
// resID is the workload Resource node ID. namespace is propagated to
// referenced ConfigMap/Secret/PVC names so we can build cross-resource
// IDs.
func extractK8sPodSpec(filePath, resID, namespace string, podSpec *yaml.Node, result *parser.ExtractionResult) {
	if podSpec == nil {
		return
	}
	containers := append(sequenceItems(mappingGet(podSpec, "containers")),
		sequenceItems(mappingGet(podSpec, "initContainers"))...)
	volumes := mappingGet(podSpec, "volumes")
	for _, c := range containers {
		extractK8sContainer(filePath, resID, namespace, c, result)
	}
	if volumes != nil {
		for _, v := range sequenceItems(volumes) {
			extractK8sVolume(filePath, resID, namespace, v, result)
		}
	}
}

func extractK8sContainer(filePath, resID, namespace string, container *yaml.Node, result *parser.ExtractionResult) {
	if container == nil {
		return
	}
	line := container.Line
	if line <= 0 {
		line = 1
	}
	// container.image — link Resource → Image.
	if image := scalarOf(mappingGet(container, "image")); image != "" {
		imageID := imageNodeID(image)
		ref, tag := splitImageRef(image)
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: imageID, Kind: graph.KindImage, Name: image,
			FilePath: filePath, StartLine: line, EndLine: line,
			Language: "yaml",
			Meta: map[string]any{
				"role": "base",
				"ref":  ref,
				"tag":  tag,
			},
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: resID, To: imageID, Kind: graph.EdgeDependsOn,
			FilePath: filePath, Line: line,
		})
	}

	// container.env[] — direct env vars.
	for _, env := range sequenceItems(mappingGet(container, "env")) {
		envName := scalarOf(mappingGet(env, "name"))
		if envName == "" {
			continue
		}
		envLine := env.Line
		if envLine <= 0 {
			envLine = line
		}
		keyID := configKeyEnvID(envName)
		emitConfigKeyNode(result, keyID, envName, "env", "k8s",
			filePath, envLine)
		result.Edges = append(result.Edges, &graph.Edge{
			From: resID, To: keyID, Kind: graph.EdgeUsesEnv,
			FilePath: filePath, Line: envLine,
			Meta: map[string]any{"scope": "runtime"},
		})
		// valueFrom: configMapKeyRef / secretKeyRef → EdgeConfigures
		// to the source resource AND a typed config_key node for
		// the source key.
		if vf := mappingGet(env, "valueFrom"); vf != nil {
			if cmRef := mappingGet(vf, "configMapKeyRef"); cmRef != nil {
				cmName := scalarOf(mappingGet(cmRef, "name"))
				cmKey := scalarOf(mappingGet(cmRef, "key"))
				if cmName != "" {
					target := k8sResourceID("ConfigMap", namespace, cmName)
					result.Edges = append(result.Edges, &graph.Edge{
						From: resID, To: target, Kind: graph.EdgeConfigures,
						FilePath: filePath, Line: envLine,
						Meta: map[string]any{
							"via":  "configMapKeyRef",
							"key":  cmKey,
							"into": envName,
						},
					})
				}
			}
			if secRef := mappingGet(vf, "secretKeyRef"); secRef != nil {
				secName := scalarOf(mappingGet(secRef, "name"))
				secKey := scalarOf(mappingGet(secRef, "key"))
				if secName != "" {
					target := k8sResourceID("Secret", namespace, secName)
					result.Edges = append(result.Edges, &graph.Edge{
						From: resID, To: target, Kind: graph.EdgeConfigures,
						FilePath: filePath, Line: envLine,
						Meta: map[string]any{
							"via":  "secretKeyRef",
							"key":  secKey,
							"into": envName,
						},
					})
				}
			}
		}
	}

	// container.envFrom[] — bulk env from ConfigMap or Secret.
	for _, envFrom := range sequenceItems(mappingGet(container, "envFrom")) {
		envLine := envFrom.Line
		if envLine <= 0 {
			envLine = line
		}
		if cmRef := mappingGet(envFrom, "configMapRef"); cmRef != nil {
			if cmName := scalarOf(mappingGet(cmRef, "name")); cmName != "" {
				target := k8sResourceID("ConfigMap", namespace, cmName)
				result.Edges = append(result.Edges, &graph.Edge{
					From: resID, To: target, Kind: graph.EdgeConfigures,
					FilePath: filePath, Line: envLine,
					Meta: map[string]any{"via": "envFrom.configMapRef"},
				})
			}
		}
		if secRef := mappingGet(envFrom, "secretRef"); secRef != nil {
			if secName := scalarOf(mappingGet(secRef, "name")); secName != "" {
				target := k8sResourceID("Secret", namespace, secName)
				result.Edges = append(result.Edges, &graph.Edge{
					From: resID, To: target, Kind: graph.EdgeConfigures,
					FilePath: filePath, Line: envLine,
					Meta: map[string]any{"via": "envFrom.secretRef"},
				})
			}
		}
	}

	// container.ports[] — EdgeExposes.
	for _, p := range sequenceItems(mappingGet(container, "ports")) {
		port := mappingScalarInt(mappingGet(p, "containerPort"))
		if port == 0 {
			continue
		}
		proto := strings.ToLower(scalarOf(mappingGet(p, "protocol")))
		if proto == "" {
			proto = "tcp"
		}
		pLine := p.Line
		if pLine <= 0 {
			pLine = line
		}
		result.Edges = append(result.Edges, &graph.Edge{
			From: resID, To: portTargetID(proto, port),
			Kind:     graph.EdgeExposes,
			FilePath: filePath, Line: pLine,
			Meta: map[string]any{"proto": proto, "port": port},
		})
	}
}

// extractK8sVolume walks a single PodSpec.spec.volumes[] entry and
// emits EdgeMounts from the Resource to the volume source's
// referenced ConfigMap / Secret / PVC.
func extractK8sVolume(filePath, resID, namespace string, vol *yaml.Node, result *parser.ExtractionResult) {
	if vol == nil {
		return
	}
	line := vol.Line
	if line <= 0 {
		line = 1
	}
	if cm := mappingGet(vol, "configMap"); cm != nil {
		if cmName := scalarOf(mappingGet(cm, "name")); cmName != "" {
			target := k8sResourceID("ConfigMap", namespace, cmName)
			result.Edges = append(result.Edges, &graph.Edge{
				From: resID, To: target, Kind: graph.EdgeMounts,
				FilePath: filePath, Line: line,
				Meta: map[string]any{"via": "volume.configMap"},
			})
		}
	}
	if sec := mappingGet(vol, "secret"); sec != nil {
		if secName := scalarOf(mappingGet(sec, "secretName")); secName != "" {
			target := k8sResourceID("Secret", namespace, secName)
			result.Edges = append(result.Edges, &graph.Edge{
				From: resID, To: target, Kind: graph.EdgeMounts,
				FilePath: filePath, Line: line,
				Meta: map[string]any{"via": "volume.secret"},
			})
		}
	}
	if pvc := mappingGet(vol, "persistentVolumeClaim"); pvc != nil {
		if pvcName := scalarOf(mappingGet(pvc, "claimName")); pvcName != "" {
			target := k8sResourceID("PersistentVolumeClaim", namespace, pvcName)
			result.Edges = append(result.Edges, &graph.Edge{
				From: resID, To: target, Kind: graph.EdgeMounts,
				FilePath: filePath, Line: line,
				Meta: map[string]any{"via": "volume.pvc"},
			})
		}
	}
}

func extractK8sServicePorts(filePath, resID string, spec *yaml.Node, result *parser.ExtractionResult) {
	if spec == nil {
		return
	}
	for _, p := range sequenceItems(mappingGet(spec, "ports")) {
		port := mappingScalarInt(mappingGet(p, "port"))
		if port == 0 {
			continue
		}
		proto := strings.ToLower(scalarOf(mappingGet(p, "protocol")))
		if proto == "" {
			proto = "tcp"
		}
		pLine := p.Line
		if pLine <= 0 {
			pLine = 1
		}
		result.Edges = append(result.Edges, &graph.Edge{
			From: resID, To: portTargetID(proto, port),
			Kind:     graph.EdgeExposes,
			FilePath: filePath, Line: pLine,
			Meta: map[string]any{"proto": proto, "port": port},
		})
	}
}

// extractK8sIngressBackends links an Ingress to each backing Service
// referenced from its rules. Both v1 (.backend.service.name) and v1
// path entries are supported; older v1beta1 shapes
// (.backend.serviceName) are handled too.
func extractK8sIngressBackends(filePath, resID, namespace string, spec *yaml.Node, result *parser.ExtractionResult) {
	if spec == nil {
		return
	}
	emitBackend := func(backend *yaml.Node, line int) {
		if backend == nil {
			return
		}
		// v1: backend.service.name
		if svc := mappingGet(backend, "service"); svc != nil {
			if svcName := scalarOf(mappingGet(svc, "name")); svcName != "" {
				target := k8sResourceID("Service", namespace, svcName)
				result.Edges = append(result.Edges, &graph.Edge{
					From: resID, To: target, Kind: graph.EdgeDependsOn,
					FilePath: filePath, Line: line,
					Meta: map[string]any{"via": "ingress.service"},
				})
			}
		}
		// v1beta1: backend.serviceName
		if svcName := scalarOf(mappingGet(backend, "serviceName")); svcName != "" {
			target := k8sResourceID("Service", namespace, svcName)
			result.Edges = append(result.Edges, &graph.Edge{
				From: resID, To: target, Kind: graph.EdgeDependsOn,
				FilePath: filePath, Line: line,
				Meta: map[string]any{"via": "ingress.serviceName"},
			})
		}
	}
	// Default backend.
	if def := mappingGet(spec, "defaultBackend"); def != nil {
		emitBackend(def, def.Line)
	}
	for _, rule := range sequenceItems(mappingGet(spec, "rules")) {
		http := mappingGet(rule, "http")
		for _, p := range sequenceItems(mappingGet(http, "paths")) {
			emitBackend(mappingGet(p, "backend"), p.Line)
		}
	}
}

// extractK8sConfigMapData materialises one KindConfigKey per data-map
// key. source ∈ "k8s_cm" | "k8s_secret".
func extractK8sConfigMapData(filePath, resID, resName string, data *yaml.Node, source string, result *parser.ExtractionResult) {
	if data == nil || data.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(data.Content); i += 2 {
		k := data.Content[i]
		if k == nil {
			continue
		}
		key := k.Value
		if key == "" {
			continue
		}
		line := k.Line
		if line <= 0 {
			line = 1
		}
		id := "cfg::" + source + "::" + resName + "::" + key
		emitConfigKeyNode(result, id, key, source, "k8s",
			filePath, line)
		result.Edges = append(result.Edges, &graph.Edge{
			From: resID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: line,
		})
	}
}

func emitConfigKeyNode(result *parser.ExtractionResult, id, name, source, origin string, filePath string, line int) {
	result.Nodes = append(result.Nodes, &graph.Node{
		ID: id, Kind: graph.KindConfigKey, Name: name,
		FilePath: filePath, StartLine: line, EndLine: line,
		Language: "yaml",
		Meta: map[string]any{
			"source": source,
			"origin": origin,
		},
	})
}

// k8sResourceID is the canonical ID for a Kubernetes Resource node.
// It is shared by every site that needs to refer to the same
// resource across documents — the workload pointing at a ConfigMap
// (envFrom) and the ConfigMap document itself produce the same
// target ID, so the cross-doc edge always lands on a single node.
func k8sResourceID(kind, namespace, name string) string {
	if namespace == "" {
		namespace = "_default"
	}
	return "k8s::" + kind + "::" + namespace + "::" + name
}

// ----- yaml.Node helpers ------------------------------------------------

// documentMapping returns the top-level MappingNode for a parsed YAML
// document. yaml.v3 wraps each Decode result in a DocumentNode whose
// single content child is the actual root.
func documentMapping(doc *yaml.Node) *yaml.Node {
	if doc == nil {
		return nil
	}
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		doc = doc.Content[0]
	}
	if doc == nil || doc.Kind != yaml.MappingNode {
		return nil
	}
	return doc
}

// mappingGet returns the value node for the given key inside a
// MappingNode. Returns nil when the parent isn't a mapping or the
// key is absent.
func mappingGet(parent *yaml.Node, key string) *yaml.Node {
	if parent == nil || parent.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(parent.Content); i += 2 {
		k := parent.Content[i]
		if k != nil && k.Value == key {
			return parent.Content[i+1]
		}
	}
	return nil
}

// scalarOf returns the .Value of a scalar node, or "" when nil/non-scalar.
func scalarOf(n *yaml.Node) string {
	if n == nil || n.Kind != yaml.ScalarNode {
		return ""
	}
	return n.Value
}

// sequenceItems returns the items of a SequenceNode, or nil for non-sequences.
func sequenceItems(n *yaml.Node) []*yaml.Node {
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	out := make([]*yaml.Node, 0, len(n.Content))
	for _, c := range n.Content {
		if c != nil {
			out = append(out, c)
		}
	}
	return out
}

// mappingScalarInt parses a scalar yaml.Node as an integer. Returns 0
// when the node is missing, non-scalar, or non-numeric. Used for
// parsing port numbers, replica counts, etc.
func mappingScalarInt(n *yaml.Node) int {
	if n == nil || n.Kind != yaml.ScalarNode {
		return 0
	}
	if v, err := strconv.Atoi(strings.TrimSpace(n.Value)); err == nil {
		return v
	}
	return 0
}
