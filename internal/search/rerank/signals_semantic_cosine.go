package rerank

import (
	"math"
	"strings"

	"github.com/zzet/gortex/internal/graph"
)

// SemanticCosineSignal scores a candidate by the cosine similarity
// between the query embedding (Context.QueryVec) and an on-the-fly
// embedding of a compact text assembled from the candidate node —
// name, qualified name, file path, and (when present) signature and
// doc head. Unlike SemanticSignal, which reads a rank from a full
// vector-index channel, this signal needs no ANN index and no
// index-time vector build: it re-embeds only the BM25 top-N candidates
// the reranker already holds, with the always-available in-process
// static word-vector provider the MCP server wires into
// Context.EmbedText.
//
// It is the intent-query (natural-language "T3") weapon: BM25 alone
// cannot connect "decode bson request body" to BindBody in
// binding/bson.go, but the averaged word vectors put "bson"/"body" near
// the candidate's path + name tokens, so a target sitting at BM25 rank
// 8-20 is lifted into the top-5. The per-class weight table damps it on
// identifier/path queries (where exact tokens are the reliable signal)
// and gives it full weight on concept queries.
type SemanticCosineSignal struct{}

func (SemanticCosineSignal) Name() string { return SignalSemanticCosine }

func (SemanticCosineSignal) Contribute(_ string, c *Candidate, ctx *Context) float64 {
	if ctx.EmbedText == nil || len(ctx.QueryVec) == 0 || c == nil || c.Node == nil {
		return 0
	}
	text := candidateEmbedText(c.Node)
	if text == "" {
		return 0
	}
	vec := ctx.EmbedText(text)
	if len(vec) != len(ctx.QueryVec) {
		return 0
	}
	sim := cosineSim(ctx.QueryVec, vec)
	if sim <= 0 {
		return 0
	}
	if sim > 1 {
		sim = 1
	}
	return sim
}

// maxSemanticFieldRunes caps how much of a single free-text field
// (signature, doc head) feeds the candidate embedding. Averaged word
// vectors dilute as the token count grows, and a very long body would
// also trip the vector index's >512-token reliability limit, so each
// field is truncated to a short, high-signal head.
const maxSemanticFieldRunes = 200

// candidateEmbedText assembles the compact text embedded for a
// candidate. Order is deliberate — the highest-signal identifiers
// (name, receiver/package via QualName, path tokens) come first, then
// the structural signature and a doc head when the extractor stamped
// one. The shared embedding tokenizer splits camelCase / snake_case and
// path separators, so raw fields can be concatenated verbatim.
func candidateEmbedText(n *graph.Node) string {
	parts := make([]string, 0, 5)
	if n.Name != "" {
		parts = append(parts, n.Name)
	}
	if n.QualName != "" && n.QualName != n.Name {
		parts = append(parts, n.QualName)
	}
	if n.FilePath != "" {
		parts = append(parts, n.FilePath)
	}
	if n.Meta != nil {
		if sig, ok := n.Meta["signature"].(string); ok && sig != "" {
			parts = append(parts, truncateRunes(sig, maxSemanticFieldRunes))
		}
		if d := metaDocHead(n.Meta); d != "" {
			parts = append(parts, truncateRunes(d, maxSemanticFieldRunes))
		}
	}
	return strings.Join(parts, " ")
}

// metaDocHead extracts a short leading doc/comment string from a node's
// Meta under any of the keys extractors use, returning "" when none is
// present. Only a leading fragment is taken — the embedding averages
// word vectors, so the first sentence carries the topic and the rest
// only dilutes.
func metaDocHead(meta map[string]any) string {
	for _, k := range []string{"doc", "docstring", "comment", "summary"} {
		if v, ok := meta[k].(string); ok {
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		}
	}
	return ""
}

// truncateRunes returns the first max runes of s (rune-safe), or s when
// it is already shorter.
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	n := 0
	for i := range s {
		if n == max {
			return s[:i]
		}
		n++
	}
	return s
}

// cosineSim returns the cosine of the angle between a and b in [-1, 1].
// The provider normalises its vectors, so the denominator is ~1, but
// the full form is computed defensively (a zero vector or a caller-
// supplied unnormalised vector both yield a safe 0).
func cosineSim(a, b []float32) float64 {
	if len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		av, bv := float64(a[i]), float64(b[i])
		dot += av * bv
		na += av * av
		nb += bv * bv
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
