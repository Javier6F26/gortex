package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/zzet/gortex/pkg/wire"
)

// encodeAsGCX selects the best GCX encoding for the given canonical
// JSON value. The benchmark recognises three common MCP wrapper
// shapes produced by Gortex handlers:
//
//   - {results: [...], total: N, truncated: bool}  → rows from
//     "results", meta carries totals.
//   - {symbols: [...], total: N, etag: "..."}      → rows from
//     "symbols".
//   - {nodes: [...], edges: [...], ...}            → two-section
//     payload (one section per array).
//
// Anything else falls through to wire.EncodeAny so the benchmark
// still produces a valid GCX payload — the per-case score just
// reflects the generic fallback. This models what a well-chosen
// hand-tuned encoder would emit without duplicating the full
// internal/mcp encoder surface here.
func encodeAsGCX(tool string, value any) ([]byte, error) {
	if m, ok := value.(map[string]any); ok {
		// Detect common single-collection wrappers.
		if rows, ok := m["results"].([]any); ok {
			return encodeFlatRows(tool, rows, meta(m, "total", "truncated"))
		}
		if rows, ok := m["symbols"].([]any); ok {
			return encodeFlatRows(tool, rows, meta(m, "total", "etag"))
		}
		if nodes, okN := m["nodes"].([]any); okN {
			return encodeNodesAndEdges(tool, nodes, m["edges"], meta(m, "total_nodes", "total_edges", "truncated"))
		}
		if edges, okE := m["edges"].([]any); okE && !hasKey(m, "nodes") {
			// Edges-only subgraph (find_usages). Emit one section.
			return encodeFlatRows(tool, edges, meta(m, "total_edges", "truncated"))
		}
		if rows, ok := m["implementations"].([]any); ok {
			return encodeFlatRows(tool, rows, meta(m, "total"))
		}
		// Multi-section wrapper: every value is a list of objects
		// (e.g. get_repo_outline: languages / communities / hotspots
		// / most_imported / entry_points). Emit one GCX section per
		// key with stable ordering.
		if looksMultiSection(m) {
			return encodeMultiSection(tool, m)
		}
	}
	var buf bytes.Buffer
	if err := wire.EncodeAny(&buf, tool, value); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func hasKey(m map[string]any, k string) bool { _, ok := m[k]; return ok }

// looksMultiSection reports whether m is a "sectioned" wrapper — a
// map where every value is either a list of objects or a list of
// scalars, and there are at least two such keys. Nested scalar
// records (fan_in / counts / etag) break the pattern so we keep the
// check strict.
func looksMultiSection(m map[string]any) bool {
	listyKeys := 0
	for _, v := range m {
		switch x := v.(type) {
		case []any:
			listyKeys++
			_ = x
		default:
			// Scalars disqualify the wrapper — a real multi-section
			// payload would carry metadata in the headers, not as
			// sibling scalar keys. This also filters out graph_stats
			// (which is mostly scalar cells).
			return false
		}
	}
	return listyKeys >= 2
}

func encodeMultiSection(tool string, m map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		rows, _ := m[k].([]any)
		if len(rows) == 0 {
			// Preserve the section even when empty so decoders can
			// still iterate the known sub-tables.
			hdr := wire.Header{Tool: tool + "." + k, Fields: []string{"value"}, Meta: map[string]string{"rows": "0"}}
			_ = wire.NewEncoder(&buf, hdr).Close()
			continue
		}
		// Scalar list → single-column "value" field; object list →
		// flat rows with union-of-keys fields.
		if _, scalar := rows[0].(map[string]any); scalar {
			body, err := encodeFlatRows(tool+"."+k, rows, nil)
			if err != nil {
				return nil, err
			}
			buf.Write(body)
			continue
		}
		hdr := wire.Header{Tool: tool + "." + k, Fields: []string{"value"}, Meta: map[string]string{"rows": fmt.Sprintf("%d", len(rows))}}
		enc := wire.NewEncoder(&buf, hdr)
		for _, v := range rows {
			if err := enc.WriteRow(renderScalar(v)); err != nil {
				return nil, err
			}
		}
		if err := enc.Close(); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

// encodeFlatRows writes a list-of-objects as a single GCX section
// with the union of keys as fields. Mirrors what the real
// encodeSearchSymbols / encodeBatchSymbols / encodeFileSummary
// encoders in internal/mcp produce — uniform row shape, no nested
// JSON cells for the shared scalar keys.
func encodeFlatRows(tool string, rows []any, meta map[string]string) ([]byte, error) {
	fields := unionKeys(rows)
	var buf bytes.Buffer
	hdr := wire.Header{Tool: tool, Fields: fields, Meta: map[string]string{
		"rows": fmt.Sprintf("%d", len(rows)),
	}}
	for k, v := range meta {
		hdr.Meta[k] = v
	}
	enc := wire.NewEncoder(&buf, hdr)
	for _, r := range rows {
		obj, ok := r.(map[string]any)
		if !ok {
			if err := enc.WriteRow(fmt.Sprint(r)); err != nil {
				return nil, err
			}
			continue
		}
		values := make([]any, len(fields))
		for i, f := range fields {
			values[i] = renderScalar(obj[f])
		}
		if err := enc.WriteRow(values...); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), enc.Close()
}

// encodeNodesAndEdges emits a node section followed by an edge section
// using the benchmark's single-shot writer. Mirrors the real
// encodeSubGraph in internal/mcp.
func encodeNodesAndEdges(tool string, nodes []any, edgesAny any, meta map[string]string) ([]byte, error) {
	var buf bytes.Buffer
	nodeFields := unionKeys(nodes)
	nHdr := wire.Header{Tool: tool + ".nodes", Fields: nodeFields, Meta: map[string]string{
		"rows": fmt.Sprintf("%d", len(nodes)),
	}}
	for k, v := range meta {
		nHdr.Meta[k] = v
	}
	nEnc := wire.NewEncoder(&buf, nHdr)
	for _, n := range nodes {
		obj, ok := n.(map[string]any)
		if !ok {
			continue
		}
		values := make([]any, len(nodeFields))
		for i, f := range nodeFields {
			values[i] = renderScalar(obj[f])
		}
		if err := nEnc.WriteRow(values...); err != nil {
			return nil, err
		}
	}
	if err := nEnc.Close(); err != nil {
		return nil, err
	}
	edges, _ := edgesAny.([]any)
	edgeFields := unionKeys(edges)
	eHdr := wire.Header{Tool: tool + ".edges", Fields: edgeFields, Meta: map[string]string{
		"rows": fmt.Sprintf("%d", len(edges)),
	}}
	eEnc := wire.NewEncoder(&buf, eHdr)
	for _, e := range edges {
		obj, ok := e.(map[string]any)
		if !ok {
			continue
		}
		values := make([]any, len(edgeFields))
		for i, f := range edgeFields {
			values[i] = renderScalar(obj[f])
		}
		if err := eEnc.WriteRow(values...); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), eEnc.Close()
}

func unionKeys(rows []any) []string {
	seen := map[string]struct{}{}
	for _, r := range rows {
		if obj, ok := r.(map[string]any); ok {
			for k := range obj {
				seen[k] = struct{}{}
			}
		}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func meta(m map[string]any, keys ...string) map[string]string {
	out := map[string]string{}
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			out[k] = fmt.Sprint(v)
		}
	}
	return out
}

// renderScalar flattens a nested value into a GCX cell — scalars as
// their natural string form, collections as compact JSON (matching
// the internal/mcp encoders' behaviour for heterogeneous fields such
// as providers / consumers / callers).
func renderScalar(v any) any {
	switch v.(type) {
	case nil:
		return ""
	case map[string]any, []any:
		// Nested collection — caller must not rely on cell
		// homogeneity. Serialise to compact JSON so the cell
		// stays on one physical line but the decoder can parse
		// it back when desired.
		b, err := marshalCompact(v)
		if err != nil {
			return fmt.Sprint(v)
		}
		return b
	default:
		return v
	}
}

func marshalCompact(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}
