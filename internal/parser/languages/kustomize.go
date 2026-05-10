package languages

import (
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// extractKustomizeYAML emits a KindKustomization node for the
// kustomization.yaml at filePath plus EdgeDependsOn / EdgeReferences
// edges for every base, component, resource, patch, and ConfigMap /
// Secret generator declared inside it.
//
// IDs:
//
//   - The overlay node itself: `kustomize::<dir>` where dir is the
//     directory containing the kustomization, normalised with `/`.
//   - Bases / components: `kustomize::<resolved-dir>` (sibling
//     overlays). The resolved-dir is computed by joining the
//     overlay's dir with the base path; relative `..` segments are
//     preserved by filepath.Clean.
//   - Resource files: the resource path is left as-is and used as
//     the EdgeReferences target; the same string ID is what the
//     YAML / K8s extractor produces for the file node, so the edge
//     lands on a real node when the file is indexed.
//   - ConfigMap / Secret generators: synthetic K8s Resource IDs
//     `k8s::ConfigMap::_default::<name>` etc., matching what the K8s
//     manifest path would have produced if the same generator were
//     materialised. EdgeDependsOn ties the overlay to each.
//
// Returns true when the file was a kustomization (i.e. parses as a
// mapping document); false otherwise so the caller can fall through.
func extractKustomizeYAML(filePath, fileID string, src []byte, result *parser.ExtractionResult) bool {
	dec := yaml.NewDecoder(strings.NewReader(string(src)))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		return false
	}
	root := documentMapping(&doc)
	if root == nil {
		return false
	}
	dir := filepath.ToSlash(filepath.Dir(filePath))
	overlayID := kustomizationID(dir)
	overlayMeta := map[string]any{
		"dir": dir,
	}
	// Capture descriptive fields when present.
	if v := scalarOf(mappingGet(root, "namespace")); v != "" {
		overlayMeta["namespace"] = v
	}
	if v := scalarOf(mappingGet(root, "namePrefix")); v != "" {
		overlayMeta["name_prefix"] = v
	}
	if v := scalarOf(mappingGet(root, "nameSuffix")); v != "" {
		overlayMeta["name_suffix"] = v
	}
	result.Nodes = append(result.Nodes, &graph.Node{
		ID: overlayID, Kind: graph.KindKustomization, Name: dir,
		FilePath: filePath, StartLine: 1, EndLine: 1,
		Language: "yaml",
		Meta:     overlayMeta,
	})
	result.Edges = append(result.Edges, &graph.Edge{
		From: fileID, To: overlayID, Kind: graph.EdgeDefines,
		FilePath: filePath, Line: 1,
	})

	// Bases / components: each entry is a path to another overlay
	// directory or kustomization file.
	for _, key := range []string{"bases", "components"} {
		seq := mappingGet(root, key)
		for _, item := range sequenceItems(seq) {
			path := strings.TrimSpace(item.Value)
			if path == "" {
				continue
			}
			line := item.Line
			if line <= 0 {
				line = 1
			}
			target := kustomizationID(resolveKustomizePath(dir, path))
			result.Edges = append(result.Edges, &graph.Edge{
				From: overlayID, To: target, Kind: graph.EdgeDependsOn,
				FilePath: filePath, Line: line,
				Meta: map[string]any{"link": key},
			})
		}
	}

	// Resources: paths to YAML manifests (or sibling overlays — both
	// shapes are accepted by Kustomize, so we emit EdgeReferences
	// for either case and let the consumer follow whichever target
	// resolves).
	for _, item := range sequenceItems(mappingGet(root, "resources")) {
		path := strings.TrimSpace(item.Value)
		if path == "" {
			continue
		}
		line := item.Line
		if line <= 0 {
			line = 1
		}
		resolved := resolveKustomizePath(dir, path)
		// Try as a file first; if the path looks like a directory
		// (no extension) treat it as another overlay.
		ext := strings.ToLower(filepath.Ext(resolved))
		switch ext {
		case ".yaml", ".yml", ".json":
			result.Edges = append(result.Edges, &graph.Edge{
				From: overlayID, To: resolved, Kind: graph.EdgeReferences,
				FilePath: filePath, Line: line,
				Meta: map[string]any{"link": "resource_file"},
			})
		default:
			result.Edges = append(result.Edges, &graph.Edge{
				From: overlayID, To: kustomizationID(resolved),
				Kind:     graph.EdgeDependsOn,
				FilePath: filePath, Line: line,
				Meta: map[string]any{"link": "resource_overlay"},
			})
		}
	}

	// patches / patchesStrategicMerge / patchesJson6902 — each
	// references a YAML file that overlays a sibling resource.
	for _, key := range []string{"patches", "patchesStrategicMerge", "patchesJson6902"} {
		for _, item := range sequenceItems(mappingGet(root, key)) {
			// Each entry is either a scalar path or a mapping
			// with a "path" field.
			path := ""
			line := item.Line
			switch item.Kind {
			case yaml.ScalarNode:
				path = strings.TrimSpace(item.Value)
			case yaml.MappingNode:
				path = strings.TrimSpace(scalarOf(mappingGet(item, "path")))
			}
			if path == "" {
				continue
			}
			if line <= 0 {
				line = 1
			}
			resolved := resolveKustomizePath(dir, path)
			result.Edges = append(result.Edges, &graph.Edge{
				From: overlayID, To: resolved, Kind: graph.EdgeReferences,
				FilePath: filePath, Line: line,
				Meta: map[string]any{"link": key},
			})
		}
	}

	// configMapGenerator / secretGenerator — each entry materialises
	// a synthetic Resource node so consumers can see "this overlay
	// produces ConfigMap X."
	emitGenerator := func(seq *yaml.Node, kind string) {
		for _, item := range sequenceItems(seq) {
			name := scalarOf(mappingGet(item, "name"))
			if name == "" {
				continue
			}
			ns := scalarOf(mappingGet(item, "namespace"))
			if ns == "" {
				ns = "_default"
			}
			line := item.Line
			if line <= 0 {
				line = 1
			}
			target := k8sResourceID(kind, ns, name)
			result.Edges = append(result.Edges, &graph.Edge{
				From: overlayID, To: target, Kind: graph.EdgeDependsOn,
				FilePath: filePath, Line: line,
				Meta: map[string]any{"link": "generator"},
			})
		}
	}
	emitGenerator(mappingGet(root, "configMapGenerator"), "ConfigMap")
	emitGenerator(mappingGet(root, "secretGenerator"), "Secret")

	return true
}

// kustomizationID is the canonical KindKustomization node ID. The
// directory string is normalised with forward slashes so cross-
// platform repos converge on a single ID.
func kustomizationID(dir string) string {
	return "kustomize::" + filepath.ToSlash(dir)
}

// resolveKustomizePath joins a relative path declared inside a
// kustomization.yaml with the directory holding that kustomization.
// Absolute paths are returned untouched. The result is normalised
// with forward slashes.
func resolveKustomizePath(baseDir, p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		return filepath.ToSlash(filepath.Clean(p))
	}
	return filepath.ToSlash(filepath.Clean(filepath.Join(baseDir, p)))
}

// isKustomizationFile returns true when the basename matches one
// of the canonical kustomize.yaml / kustomization.yml / Kustomization
// names. The YAML extractor uses this to dispatch into the kustomize
// path before falling through to the generic top-level-keys walker.
func isKustomizationFile(filePath string) bool {
	base := filepath.Base(filePath)
	switch base {
	case "kustomization.yaml", "kustomization.yml", "Kustomization":
		return true
	}
	return false
}
