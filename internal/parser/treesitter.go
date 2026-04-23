package parser

import (
	"fmt"

	sitter "github.com/odvcencio/gotreesitter"
)

// parseTimeoutMicros bounds a single parse attempt. The odvcencio runtime
// honours it via SetTimeoutMicros; we surface the same 5s budget the old
// smacker-based wrapper used to apply via context.WithTimeout.
const parseTimeoutMicros uint64 = 5_000_000

// Tree wraps sitter.Tree with a Close() alias that maps to Release(). The
// adapters call defer tree.Close() as a carry-over from the CGO-era API;
// keeping the spelling stable avoids a churn of 30+ adapter edits when the
// underlying runtime has no explicit close (it is GC-managed).
type Tree struct {
	*sitter.Tree
}

// Close releases the underlying tree. Safe to call on a nil wrapper.
func (t *Tree) Close() {
	if t == nil || t.Tree == nil {
		return
	}
	t.Release()
}

// CapturedNode holds information about a single captured tree-sitter node.
type CapturedNode struct {
	Text      string
	StartLine int // 0-based (tree-sitter native)
	EndLine   int // 0-based
	StartCol  int
	EndCol    int
	Node      *sitter.Node
}

// QueryResult represents a single match from a tree-sitter query.
type QueryResult struct {
	Captures map[string]*CapturedNode
}

// ParseFile parses source bytes with the given language and returns the tree.
// The caller should call tree.Close() when done (pure-Go runtime is
// GC-managed; Close is a no-op-safe alias for Release).
func ParseFile(src []byte, lang *sitter.Language) (*Tree, error) {
	p := sitter.NewParser(lang)
	p.SetTimeoutMicros(parseTimeoutMicros)

	tree, err := p.Parse(src)
	if err != nil {
		return nil, fmt.Errorf("tree-sitter parse: %w", err)
	}
	return &Tree{Tree: tree}, nil
}

// RunQuery executes a tree-sitter S-expression query against a node and
// returns all matches with their captures.
//
// The odvcencio engine returns a single QueryMatch per top-level pattern
// instantiation, even when an inner sub-pattern repeats (e.g. a pattern
// like `(import_declaration (import_spec_list (import_spec …)))` yields
// one match with N captures of the same name when the source has N
// import specs). Extractors written against the canonical tree-sitter
// behaviour (one match per repeated leaf) break on this.
//
// RunQuery normalises this: when a match's captures contain duplicate
// names, it splits them into per-subtree QueryResults by grouping
// captures that share the same immediate AST parent. Matches with no
// duplicate names pass through unchanged — the usual single-match case.
func RunQuery(pattern string, lang *sitter.Language, node *sitter.Node, src []byte) ([]QueryResult, error) {
	q, err := sitter.NewQuery(pattern, lang)
	if err != nil {
		return nil, fmt.Errorf("tree-sitter query compile: %w", err)
	}

	cursor := q.Exec(node, lang, src)

	var results []QueryResult
	for {
		match, ok := cursor.NextMatch()
		if !ok {
			break
		}
		if len(match.Captures) == 0 {
			continue
		}
		results = append(results, expandMatch(match.Captures, src)...)
	}
	return results, nil
}

// expandMatch converts a QueryMatch's capture list into one or more
// QueryResults. It splits by occurrence-index when any capture name is
// duplicated within the match; otherwise it returns a single result.
//
// For a pattern that captures a repeated sub-structure (e.g. N methods
// inside a class with captures `method.name` and `method.def`), odvcencio
// emits a single match with 2N captures. This function pairs them up by
// occurrence order so each method gets its own result. Singleton
// captures (e.g. an outer `class.name`) are replicated into every split
// result so callers can read the anchor regardless of which sub-match
// they inspect.
func expandMatch(caps []sitter.QueryCapture, src []byte) []QueryResult {
	if len(caps) == 0 {
		return nil
	}

	// Count each capture name and collect captures per name in order.
	counts := make(map[string]int, len(caps))
	perName := make(map[string][]sitter.QueryCapture, len(caps))
	order := make([]string, 0, len(caps))
	maxCount := 0
	for _, c := range caps {
		if c.Node == nil {
			continue
		}
		if _, seen := perName[c.Name]; !seen {
			order = append(order, c.Name)
		}
		counts[c.Name]++
		perName[c.Name] = append(perName[c.Name], c)
		if counts[c.Name] > maxCount {
			maxCount = counts[c.Name]
		}
	}

	// Fast path: no duplicate names → single result.
	if maxCount <= 1 {
		qr := QueryResult{Captures: make(map[string]*CapturedNode, len(caps))}
		for _, c := range caps {
			if c.Node == nil {
				continue
			}
			qr.Captures[c.Name] = capturedFrom(c, src)
		}
		return []QueryResult{qr}
	}

	// Split into maxCount groups. For each capture name:
	//   - If it occurs once → replicate into every group (outer anchor).
	//   - If it occurs exactly maxCount times → the i-th occurrence
	//     belongs to group i (per-subtree capture).
	//   - Otherwise (1 < k < maxCount) → fall back to placing the i-th
	//     occurrence in group i, leaving tail groups without this name.
	results := make([]QueryResult, maxCount)
	for i := range results {
		results[i].Captures = make(map[string]*CapturedNode)
	}
	for _, name := range order {
		list := perName[name]
		switch {
		case len(list) == 1:
			cn := capturedFrom(list[0], src)
			for i := range results {
				results[i].Captures[name] = cn
			}
		default:
			for i, c := range list {
				if i >= maxCount {
					break
				}
				results[i].Captures[name] = capturedFrom(c, src)
			}
		}
	}
	return results
}

func capturedFrom(c sitter.QueryCapture, src []byte) *CapturedNode {
	return &CapturedNode{
		Text:      c.Text(src),
		StartLine: int(c.Node.StartPoint().Row),
		EndLine:   int(c.Node.EndPoint().Row),
		StartCol:  int(c.Node.StartPoint().Column),
		EndCol:    int(c.Node.EndPoint().Column),
		Node:      c.Node,
	}
}

// NodeText extracts the text content of a tree-sitter node from source bytes.
func NodeText(node *sitter.Node, src []byte) string {
	return node.Text(src)
}

// NodeType returns the grammar-symbol name of a node. The odvcencio runtime
// requires the Language to resolve node.Type(), so callers route through
// this shim rather than calling node.Type() directly — keeping the blast
// radius of future runtime API changes inside this file.
func NodeType(node *sitter.Node, lang *sitter.Language) string {
	if node == nil {
		return ""
	}
	return node.Type(lang)
}
