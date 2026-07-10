package languages

import (
	"bytes"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
	sitter "github.com/zzet/gortex/internal/parser/tsitter"
	"github.com/zzet/gortex/internal/parser/tsitter/yaml"
)

// YAMLExtractor extracts YAML files into graph nodes and edges.
// It focuses on top-level keys as KindVariable.
type YAMLExtractor struct {
	lang *sitter.Language
}

func NewYAMLExtractor() *YAMLExtractor {
	return &YAMLExtractor{lang: yaml.GetLanguage()}
}

func (e *YAMLExtractor) Language() string { return "yaml" }
func (e *YAMLExtractor) Extensions() []string {
	// `.yaml` / `.yml` cover the bulk of YAML files (including
	// `kustomization.yaml`). `Kustomization` is the bare-basename
	// form Kustomize accepts when no extension is desired — it
	// must be registered as a basename so the registry routes it
	// to the YAML extractor (which then dispatches into the
	// kustomize path inside Extract).
	return []string{".yaml", ".yml", "Kustomization"}
}

func (e *YAMLExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
	tree, err := parser.ParseFile(src, e.lang)
	if err != nil {
		return nil, err
	}
	defer tree.Close()

	root := tree.RootNode()
	result := &parser.ExtractionResult{}

	fileNode := &graph.Node{
		ID: filePath, Kind: graph.KindFile, Name: filePath,
		FilePath: filePath, StartLine: 1, EndLine: int(root.EndPoint().Row) + 1,
		Language: "yaml",
	}
	result.Nodes = append(result.Nodes, fileNode)

	// Helm render manifests (templates/*.yaml) embed Go-template
	// include/template directives. Scan them up front and emit EdgeCalls
	// into the chart's named templates — this augments, it does not
	// replace, the normal dispatch below (the file still gets its
	// resource / config-key nodes). Conservative: only when an
	// include/template directive is actually present.
	if bytes.Contains(src, []byte("{{")) &&
		(bytes.Contains(src, []byte("include")) || bytes.Contains(src, []byte("template"))) {
		helmTemplateCallsFromYAML(filePath, fileNode.ID, src, result)
	}

	// Specialised YAML dispatch. Order matters:
	//   1. Kustomize files have a fixed basename — short-circuit.
	//   2. K8s manifests are detected by content (apiVersion+kind).
	//   3. dbt schema / properties files are detected by content
	//      fingerprint (models/sources/seeds/snapshots + columns).
	//   4. Otherwise fall through to the generic top-level-keys
	//      walker so plain config YAMLs still index.
	if isKustomizationFile(filePath) {
		extractKustomizeYAML(filePath, fileNode.ID, src, result)
		return result, nil
	}
	if extractKubernetesYAML(filePath, fileNode.ID, src, result) {
		return result, nil
	}
	if extractDbtSchemaYAML(filePath, fileNode.ID, src, result) {
		return result, nil
	}
	if extractSymfonyServicesYAML(filePath, fileNode.ID, src, result) {
		return result, nil
	}
	if extractAnsibleYAML(filePath, fileNode.ID, src, result) {
		return result, nil
	}

	// Walk only top-level block_mapping_pair nodes.
	e.extractTopLevelKeys(root, src, filePath, fileNode.ID, result)

	return result, nil
}

func (e *YAMLExtractor) extractTopLevelKeys(root *sitter.Node, src []byte, filePath, fileID string, result *parser.ExtractionResult) {
	// YAML tree-sitter grammar: stream -> document -> block_node -> block_mapping -> block_mapping_pair
	// We walk looking for block_mapping_pair nodes at the top-level mapping only.
	seen := make(map[string]bool)
	e.walkTopLevel(src, filePath, fileID, result, seen, root, 0)
}

// walkTopLevel walks the tree-sitter AST looking for top-level
// block_mapping_pair keys. When a pair's value is a block_sequence, it
// descends into sequence items and extracts each as a node.
func (e *YAMLExtractor) walkTopLevel(src []byte, filePath, fileID string, result *parser.ExtractionResult, seen map[string]bool, node *sitter.Node, depth int) {
	if node == nil {
		return
	}

	nodeType := node.Type()

	switch {
	case nodeType == "block_mapping_pair" && depth <= 5:
		// Extract the key. Then check if the value is a block_sequence
		// and descend to extract items.
		keyNode := node.ChildByFieldName("key")
		if keyNode == nil && node.ChildCount() > 0 {
			keyNode = node.Child(0)
		}
		if keyNode == nil {
			return
		}
		keyName := keyNode.Content(src)
		if keyName == "" || seen[keyName] {
			return
		}
		seen[keyName] = true

		// Emit the top-level key as KindVariable.
		keyID := filePath + "::" + keyName
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: keyID, Kind: graph.KindVariable, Name: keyName,
			FilePath: filePath, StartLine: int(node.StartPoint().Row) + 1, EndLine: int(node.EndPoint().Row) + 1,
			Language: "yaml",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileID, To: keyID, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: int(node.StartPoint().Row) + 1,
		})

		// If this pair has a block_sequence value (possibly wrapped in
		// a block_node), extract its items.
		if seqNode := findNestedSequence(node.ChildByFieldName("value")); seqNode != nil {
			e.extractSequenceItems(seqNode, src, filePath, keyID, keyName, result)
		}

	case nodeType == "block_sequence":
		// Top-level block_sequences (e.g., Ansible plays).
		// Extract each item relative to the file.
		e.extractSequenceItems(node, src, filePath, fileID, "", result)

	default:
		for i, _nc := 0, int(node.ChildCount()); i < _nc; i++ {
			if child := node.Child(i); child != nil {
				e.walkTopLevel(src, filePath, fileID, result, seen, child, depth+1)
			}
		}
	}
}

// extractSequenceItems walks block_sequence_item children and emits a
// KindVariable or KindDoc per item. parentID and parentKey are used to
// build the item node ID: parentID + "::" + itemName.
func (e *YAMLExtractor) extractSequenceItems(seqNode *sitter.Node, src []byte, filePath, parentID, parentKey string, result *parser.ExtractionResult) {
	for i, _nc := 0, int(seqNode.ChildCount()); i < _nc; i++ {
		item := seqNode.Child(i)
		if item == nil || item.Type() != "block_sequence_item" {
			continue
		}

		// Check if the item contains a block_mapping (complex item) or
		// is a plain scalar (simple item like "- main").
		mapNode := findChildOfType(item, "block_mapping")
		if mapNode != nil {
			e.extractMappingItem(mapNode, src, filePath, parentID, parentKey, i, result)
			continue
		}

		// Plain scalar item: "- value"
		scalar := findAnyScalarContent(item, src)
		if scalar == "" {
			continue
		}
		name := scalar
		if parentKey != "" {
			name = parentKey + "::" + scalar
		}
		id := filePath + "::" + name
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindVariable, Name: scalar,
			FilePath: filePath, StartLine: int(item.StartPoint().Row) + 1, EndLine: int(item.EndPoint().Row) + 1,
			Language: "yaml",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: parentID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: int(item.StartPoint().Row) + 1,
		})
	}
}

// extractMappingItem extracts a block_mapping inside a sequence item as a
// KindDoc node with metadata. The first "name" pair becomes the primary
// identifier; all pairs become metadata.
func (e *YAMLExtractor) extractMappingItem(mapNode *sitter.Node, src []byte, filePath, parentID, parentKey string, index int, result *parser.ExtractionResult) {
	// Extract all key-value pairs from this mapping.
	var pairs []struct{ key, val string }
	pairNodes := collectChildPairs(mapNode)
	for _, pn := range pairNodes {
		k := pairKeyContent(pn, src)
		v := pairValueContent(pn, src)
		if k != "" {
			pairs = append(pairs, struct{ key, val string }{k, v})
		}
	}
	if len(pairs) == 0 {
		return
	}

	// Use the first "name" pair as the item identifier; fall back to
	// the first key or an index-based name.
	itemName := ""
	for _, p := range pairs {
		if p.key == "name" && p.val != "" {
			itemName = p.val
			break
		}
	}
	if itemName == "" {
		itemName = pairs[0].val
	}
	if itemName == "" {
		itemName = pairs[0].key + "-" + itoaSmall(index)
	}

	// Build metadata from all pairs.
	meta := map[string]any{}
	for _, p := range pairs {
		if p.key == "repo" || p.key == "url" {
			meta[p.key+"_url"] = p.val
		} else if p.key != "name" {
			meta[p.key] = p.val
		}
	}
	if parentKey != "" {
		meta["parent_key"] = parentKey
	}

	nodeName := parentKey + "::" + itemName
	id := filePath + "::" + nodeName
	result.Nodes = append(result.Nodes, &graph.Node{
		ID: id, Kind: graph.KindVariable, Name: itemName,
		FilePath: filePath, StartLine: int(mapNode.StartPoint().Row) + 1, EndLine: int(mapNode.EndPoint().Row) + 1,
		Language: "yaml", Meta: meta,
	})
	result.Edges = append(result.Edges, &graph.Edge{
		From: parentID, To: id, Kind: graph.EdgeDefines,
		FilePath: filePath, Line: int(mapNode.StartPoint().Row) + 1,
	})
}

// --- helpers ---------------------------------------------------------

// findChildOfType returns the first descendant of node with the given
// tree-sitter node type, unwrapping up to 2 levels of block_node, or nil.
func findChildOfType(node *sitter.Node, typ string) *sitter.Node {
	if node == nil {
		return nil
	}
	if node.Type() == typ {
		return node
	}
	// Check direct children.
	for i, _nc := 0, int(node.ChildCount()); i < _nc; i++ {
		if child := node.Child(i); child != nil {
			if child.Type() == typ {
				return child
			}
			// Unwrap one level of block_node.
			if child.Type() == "block_node" {
				for j, _jc := 0, int(child.ChildCount()); j < _jc; j++ {
					grandchild := child.Child(j)
					if grandchild != nil && grandchild.Type() == typ {
						return grandchild
					}
				}
			}
		}
	}
	return nil
}

// findAnyScalarContent walks a node returning the content of the first
// named child that looks like a plain scalar (node type != "block_mapping").
func findAnyScalarContent(node *sitter.Node, src []byte) string {
	for i, _nc := 0, int(node.NamedChildCount()); i < _nc; i++ {
		child := node.NamedChild(i)
		if child == nil {
			continue
		}
		ct := child.Type()
		if ct != "block_mapping" && ct != "block_sequence" && ct != "block_mapping_pair" {
			return child.Content(src)
		}
	}
	return ""
}

// findNestedSequence returns the first block_sequence descendant of
// node, unwrapping intermediate block_node layers if present.
func findNestedSequence(node *sitter.Node) *sitter.Node {
	if node == nil {
		return nil
	}
	if node.Type() == "block_sequence" {
		return node
	}
	if node.Type() == "block_node" {
		for i, _nc := 0, int(node.ChildCount()); i < _nc; i++ {
			if child := node.Child(i); child != nil {
				if seq := findNestedSequence(child); seq != nil {
					return seq
				}
			}
		}
	}
	return nil
}

// collectChildPairs returns every block_mapping_pair child of a
// block_mapping node.
func collectChildPairs(mapNode *sitter.Node) []*sitter.Node {
	var out []*sitter.Node
	for i, _nc := 0, int(mapNode.ChildCount()); i < _nc; i++ {
		if child := mapNode.Child(i); child != nil && child.Type() == "block_mapping_pair" {
			out = append(out, child)
		}
	}
	return out
}

// pairKeyContent extracts the key text from a block_mapping_pair.
func pairKeyContent(pair *sitter.Node, src []byte) string {
	keyNode := pair.ChildByFieldName("key")
	if keyNode == nil && pair.ChildCount() > 0 {
		keyNode = pair.Child(0)
	}
	if keyNode != nil {
		return keyNode.Content(src)
	}
	return ""
}

// pairValueContent extracts the value text from a block_mapping_pair.
func pairValueContent(pair *sitter.Node, src []byte) string {
	valNode := pair.ChildByFieldName("value")
	if valNode != nil {
		return valNode.Content(src)
	}
	return ""
}
