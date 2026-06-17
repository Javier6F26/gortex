package store_sqlite

import (
	"bytes"
	"database/sql"
	"encoding/gob"
	"encoding/json"

	"github.com/zzet/gortex/internal/contracts"
	"github.com/zzet/gortex/internal/graph"
)

// Node / edge Meta is a map[string]any persisted in the `meta` column.
// It is stored as JSON, not gob: JSON needs no per-call type-engine
// compilation (the gob hot path recompiled its decoder on every edge,
// which dominated cold-load CPU and allocation), and a JSON document is
// human-readable and free of any custom binary versioning.
//
// JSON has one numeric type, so a naive json.Unmarshal into a
// map[string]any widens every number to float64 and every []T to []any,
// silently corrupting the readers that type-assert .(int) / .(float64) /
// .([]string) / .(*contracts.Shape). decodeMeta therefore routes the
// document through metaWire — a typed DTO whose fields parse each known
// key as its exact Go type — and normalises the open tail (Extra plus
// nested maps) with a small key-type table. The in-memory map a caller
// receives is byte-for-byte type-identical to what the old gob path
// produced, so no reader changes.
//
// Existing on-disk stores still hold gob blobs; decodeMeta sniffs the
// leading byte ('{' => JSON) and falls back to gob for legacy rows, which
// migrate to JSON on their next write. No schema migration is required.

// metaWire is the decode-side DTO. Scalar fields are pointers so an absent
// key (nil) is distinguished from a present zero value — comma-ok readers
// rely on that distinction. Slices, maps and Shape are already nil-able.
type metaWire struct {
	// Symbol-shape keys stamped by language extractors (node).
	Signature  *string `json:"signature,omitempty"`
	Visibility *string `json:"visibility,omitempty"`
	Doc        *string `json:"doc,omitempty"`
	External   *bool   `json:"external,omitempty"`

	// Analyzer / contract scalar keys (node).
	Complexity  *int     `json:"complexity,omitempty"`
	LoopDepth   *int     `json:"loop_depth,omitempty"`
	ParseErrors *int     `json:"parse_errors,omitempty"`
	Position    *int     `json:"position,omitempty"`
	Line        *int     `json:"line,omitempty"`
	Confidence  *float64 `json:"confidence,omitempty"`
	CoveragePct *float64 `json:"coverage_pct,omitempty"`

	// Contract structural keys (node).
	Shape            *contracts.Shape `json:"shape,omitempty"`
	ResponseEnvelope []map[string]any `json:"response_envelope,omitempty"`
	PathParamNames   []string         `json:"path_param_names,omitempty"`
	QueryParams      []string         `json:"query_params,omitempty"`
	StatusCodes      []string         `json:"status_codes,omitempty"`

	// Edge scalar keys.
	CandidateCount *int     `json:"candidate_count,omitempty"`
	Similarity     *float64 `json:"similarity,omitempty"`
	Score          *float64 `json:"score,omitempty"`
	Count          *int     `json:"count,omitempty"`
	CloneTokens    *int     `json:"clone_tokens,omitempty"`

	// Nested enrichment maps (sidecar-primary; the meta map is the
	// un-migrated / in-memory fallback). Decoded as plain maps then
	// normalised via the key-type table so their integer children come
	// back as int / int64 rather than float64.
	Churn        map[string]any `json:"churn,omitempty"`
	Coverage     map[string]any `json:"coverage,omitempty"`
	LastAuthored map[string]any `json:"last_authored,omitempty"`
	ContractMeta map[string]any `json:"contract_meta,omitempty"`

	// Extra captures every key not named above (the open / plugin /
	// per-language tail, overwhelmingly strings and bools).
	Extra map[string]any `json:"-"`
}

// metaWireKnownKeys are the JSON keys consumed by metaWire's typed fields;
// everything else is captured into Extra.
var metaWireKnownKeys = []string{
	"signature", "visibility", "doc", "external",
	"complexity", "loop_depth", "parse_errors", "position", "line",
	"confidence", "coverage_pct",
	"shape", "response_envelope", "path_param_names", "query_params", "status_codes",
	"candidate_count", "similarity", "score", "count", "clone_tokens",
	"churn", "coverage", "last_authored", "contract_meta",
}

// metaFloatKeys are keys whose numeric value must stay float64 even when it
// happens to be integral (e.g. confidence 1.0 marshals as "1"); without
// this they would normalise to int and break a .(float64) reader.
var metaFloatKeys = map[string]bool{
	"confidence": true, "coverage_pct": true, "score": true,
	"similarity": true, "churn_rate": true, "rate": true,
}

// metaInt64Keys are keys whose numeric value must be int64 (unix
// timestamps), matching readers that assert .(int64).
var metaInt64Keys = map[string]bool{
	"timestamp": true, "ts": true,
}

// metaStringSliceKeys are keys whose array value must be []string (JSON
// arrays decode to []any); readers assert .([]string).
var metaStringSliceKeys = map[string]bool{
	"path_param_names": true, "query_params": true, "status_codes": true,
	"notes": true, "methods": true, "arg_names": true, "repos": true,
}

// metaMapSliceKeys are keys whose array value must be []map[string]any.
var metaMapSliceKeys = map[string]bool{
	"response_envelope": true,
}

// UnmarshalJSON decodes the typed fields and captures every other key into
// Extra (with UseNumber so the tail keeps int/float fidelity).
func (w *metaWire) UnmarshalJSON(b []byte) error {
	type alias metaWire
	if err := json.Unmarshal(b, (*alias)(w)); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var raw map[string]any
	if err := dec.Decode(&raw); err != nil {
		return err
	}
	for _, k := range metaWireKnownKeys {
		delete(raw, k)
	}
	if len(raw) > 0 {
		w.Extra = make(map[string]any, len(raw))
		for k, v := range raw {
			w.Extra[k] = normalizeMetaValue(k, v)
		}
	}
	return nil
}

// toMap rebuilds the in-memory map[string]any with exact Go types.
func (w *metaWire) toMap() map[string]any {
	m := make(map[string]any, len(metaWireKnownKeys)+len(w.Extra))
	putString(m, "signature", w.Signature)
	putString(m, "visibility", w.Visibility)
	putString(m, "doc", w.Doc)
	putBool(m, "external", w.External)
	putInt(m, "complexity", w.Complexity)
	putInt(m, "loop_depth", w.LoopDepth)
	putInt(m, "parse_errors", w.ParseErrors)
	putInt(m, "position", w.Position)
	putInt(m, "line", w.Line)
	putFloat(m, "confidence", w.Confidence)
	putFloat(m, "coverage_pct", w.CoveragePct)
	if w.Shape != nil {
		m["shape"] = w.Shape
	}
	if w.ResponseEnvelope != nil {
		m["response_envelope"] = w.ResponseEnvelope
	}
	if w.PathParamNames != nil {
		m["path_param_names"] = w.PathParamNames
	}
	if w.QueryParams != nil {
		m["query_params"] = w.QueryParams
	}
	if w.StatusCodes != nil {
		m["status_codes"] = w.StatusCodes
	}
	putInt(m, "candidate_count", w.CandidateCount)
	putFloat(m, "similarity", w.Similarity)
	putFloat(m, "score", w.Score)
	putInt(m, "count", w.Count)
	putInt(m, "clone_tokens", w.CloneTokens)
	putNestedMap(m, "churn", w.Churn)
	putNestedMap(m, "coverage", w.Coverage)
	putNestedMap(m, "last_authored", w.LastAuthored)
	putNestedMap(m, "contract_meta", w.ContractMeta)
	for k, v := range w.Extra {
		m[k] = v
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

func putString(m map[string]any, k string, v *string) {
	if v != nil {
		m[k] = *v
	}
}

func putBool(m map[string]any, k string, v *bool) {
	if v != nil {
		m[k] = *v
	}
}

func putInt(m map[string]any, k string, v *int) {
	if v != nil {
		m[k] = *v
	}
}

func putFloat(m map[string]any, k string, v *float64) {
	if v != nil {
		m[k] = *v
	}
}

// putNestedMap normalises a nested enrichment map (decoded by the standard
// json path, so its numbers are float64) into exact Go types.
func putNestedMap(m map[string]any, k string, nested map[string]any) {
	if nested == nil {
		return
	}
	out := make(map[string]any, len(nested))
	for nk, nv := range nested {
		out[nk] = normalizeMetaValue(nk, nv)
	}
	m[k] = out
}

// normalizeMetaValue coerces a json-decoded value to the exact Go type the
// readers expect, recursing through nested maps and slices. It accepts both
// json.Number (the Extra path uses UseNumber) and float64 (the typed-field
// path decodes nested maps with standard json), so it is correct for both.
func normalizeMetaValue(key string, v any) any {
	switch vv := v.(type) {
	case json.Number:
		return normalizeNumber(key, numberToFloat(vv), &vv)
	case float64:
		return normalizeNumber(key, vv, nil)
	case []any:
		return normalizeSlice(key, vv)
	case map[string]any:
		out := make(map[string]any, len(vv))
		for nk, nv := range vv {
			out[nk] = normalizeMetaValue(nk, nv)
		}
		return out
	default:
		return v
	}
}

func numberToFloat(n json.Number) float64 {
	f, _ := n.Float64()
	return f
}

// normalizeNumber picks the Go numeric type for key. num is the float view;
// jn (may be nil) is the original json.Number for exact integer recovery.
func normalizeNumber(key string, num float64, jn *json.Number) any {
	if metaFloatKeys[key] {
		return num
	}
	if metaInt64Keys[key] {
		if jn != nil {
			if i, err := jn.Int64(); err == nil {
				return i
			}
		}
		return int64(num)
	}
	if num == float64(int64(num)) {
		if jn != nil {
			if i, err := jn.Int64(); err == nil {
				return int(i)
			}
		}
		return int(num)
	}
	return num
}

func normalizeSlice(key string, s []any) any {
	if metaStringSliceKeys[key] {
		out := make([]string, 0, len(s))
		for _, e := range s {
			if str, ok := e.(string); ok {
				out = append(out, str)
			}
		}
		return out
	}
	if metaMapSliceKeys[key] {
		out := make([]map[string]any, 0, len(s))
		for _, e := range s {
			if mm, ok := e.(map[string]any); ok {
				norm := make(map[string]any, len(mm))
				for nk, nv := range mm {
					norm[nk] = normalizeMetaValue(nk, nv)
				}
				out = append(out, norm)
			}
		}
		return out
	}
	out := make([]any, len(s))
	for i, e := range s {
		out[i] = normalizeMetaValue(key, e)
	}
	return out
}

// encodeMeta serialises Meta to JSON. nil / empty Meta stores as NULL.
func encodeMeta(m map[string]any) ([]byte, error) {
	if len(m) == 0 {
		return nil, nil
	}
	return json.Marshal(m)
}

// decodeMeta reads a meta blob. New rows are JSON (routed through metaWire
// for exact types); legacy rows are gob and decode through the fallback.
func decodeMeta(b []byte) (map[string]any, error) {
	if len(b) == 0 {
		return nil, nil
	}
	if isJSONObject(b) {
		var w metaWire
		if err := json.Unmarshal(b, &w); err != nil {
			// A gob blob whose first byte is '{' would land here; fall
			// back rather than fail the row.
			return decodeMetaGob(b)
		}
		return w.toMap(), nil
	}
	return decodeMetaGob(b)
}

// isJSONObject reports whether b looks like a JSON object (the only shape
// encodeMeta ever produces). Leading whitespace is tolerated.
func isJSONObject(b []byte) bool {
	for _, c := range b {
		switch c {
		case ' ', '\t', '\n', '\r':
			continue
		case '{':
			return true
		default:
			return false
		}
	}
	return false
}

func decodeMetaGob(b []byte) (map[string]any, error) {
	var m map[string]any
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(&m); err != nil {
		return nil, err
	}
	return m, nil
}

// -- promoted node columns ------------------------------------------------
//
// signature / visibility / doc / external are universal, hot-read node
// keys. They are lifted into dedicated nullable columns: stripped from the
// JSON blob on write (extractPromotedMeta) and restored into Meta on read
// (restorePromotedMeta), so the in-memory map is unchanged while the keys
// become queryable and the common blob shrinks.

var promotedMetaColumns = []struct {
	name string
	ddl  string
}{
	{"signature", "signature TEXT"},
	{"visibility", "visibility TEXT"},
	{"doc", "doc TEXT"},
	{"external", "external INTEGER"},
}

// ensureNodeColumns adds the promoted columns to a nodes table created
// before they existed. A fresh DB already has them from the DDL, so this is
// a no-op; an older DB is altered in place (ADD COLUMN defaults to NULL).
func ensureNodeColumns(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(nodes)`)
	if err != nil {
		return err
	}
	existing := make(map[string]bool)
	for rows.Next() {
		var (
			cid, notnull, pk int
			name, ctype      string
			dflt             sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			_ = rows.Close()
			return err
		}
		existing[name] = true
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	for _, c := range promotedMetaColumns {
		if existing[c.name] {
			continue
		}
		if _, err := db.Exec(`ALTER TABLE nodes ADD COLUMN ` + c.ddl); err != nil {
			return err
		}
	}
	return nil
}

// extractPromotedMeta splits the promoted keys out of m into typed column
// values and returns the remaining map destined for the JSON blob. m is
// not mutated; a copy is made only when a promoted key is present and has
// the expected type (otherwise the value stays in the blob).
func extractPromotedMeta(m map[string]any) (sig, vis, doc sql.NullString, ext sql.NullBool, rest map[string]any) {
	rest = m
	if len(m) == 0 {
		return
	}
	has := false
	for _, c := range promotedMetaColumns {
		if _, ok := m[c.name]; ok {
			has = true
			break
		}
	}
	if !has {
		return
	}
	rest = make(map[string]any, len(m))
	for k, v := range m {
		switch k {
		case "signature":
			if s, ok := v.(string); ok {
				sig = sql.NullString{String: s, Valid: true}
				continue
			}
		case "visibility":
			if s, ok := v.(string); ok {
				vis = sql.NullString{String: s, Valid: true}
				continue
			}
		case "doc":
			if s, ok := v.(string); ok {
				doc = sql.NullString{String: s, Valid: true}
				continue
			}
		case "external":
			if b, ok := v.(bool); ok {
				ext = sql.NullBool{Bool: b, Valid: true}
				continue
			}
		}
		rest[k] = v
	}
	return
}

// restorePromotedMeta writes the non-NULL promoted columns back into the
// node's Meta. A NULL column is left alone so a legacy gob row's blob value
// survives.
func restorePromotedMeta(n *graph.Node, sig, vis, doc sql.NullString, ext sql.NullBool) {
	if !sig.Valid && !vis.Valid && !doc.Valid && !ext.Valid {
		return
	}
	if n.Meta == nil {
		n.Meta = make(map[string]any, 4)
	}
	if sig.Valid {
		n.Meta["signature"] = sig.String
	}
	if vis.Valid {
		n.Meta["visibility"] = vis.String
	}
	if doc.Valid {
		n.Meta["doc"] = doc.String
	}
	if ext.Valid {
		n.Meta["external"] = ext.Bool
	}
}
