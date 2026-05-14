package llm

// RerankCandidate is one entry the caller asks the LLM to consider in
// Service.RerankSymbols. ID is opaque to the model — the model only
// sees it as an identifier string to echo back in the new order — so
// callers can use whatever stable handle their graph layer provides
// (typically graph.Node.ID).
type RerankCandidate struct {
	ID        string
	Name      string
	Signature string
	Path      string
}

// ExpandResult is the output of Service.ExpandQuery. Terms are
// additional identifier-style search terms the caller should OR with
// the original query before BM25. Original is the trimmed input.
// Cached reports whether the result came from the in-memory LRU.
type ExpandResult struct {
	Original string
	Terms    []string
	Cached   bool
}

// RerankResult is the output of Service.RerankSymbols. Order is a
// permutation of the candidate IDs from the input — IDs the model
// dropped are appended in their original input order so the caller
// never loses candidates. Cached reports whether the result came from
// the in-memory LRU.
type RerankResult struct {
	Order  []string
	Cached bool
}

// VerifyCandidate is one entry the caller asks the LLM to read +
// verify against the query in Service.VerifyRelevance. The prompt
// includes the function body — the model is meant to read what the
// code actually DOES, not infer relevance from the name alone. Body
// should be pre-truncated (a single noisy candidate can blow the
// assist context). Callers carry independent contextual signal that
// distinguishes "same operation on different data" cases — e.g. a
// hashing function called only from a diagnostic-publish path is
// almost certainly not password hashing.
type VerifyCandidate struct {
	ID        string
	Name      string
	Signature string
	Body      string
	Callers   []CallerInfo
}

// CallerInfo is a compact reference to one caller of a verify
// candidate. Name + Signature is usually enough to disambiguate
// "what kind of data flows into this function" without dragging in
// the full caller body.
type CallerInfo struct {
	Name      string
	Signature string
}

// VerifyResult is the output of Service.VerifyRelevance. Keep is the
// subset of input IDs whose body the model judged genuinely related
// to the query, in the model's preferred order. Empty is a valid and
// load-bearing result — the model is allowed to say "nothing here
// matches" and the caller should treat that as honest negative
// evidence rather than fall back to BM25.
type VerifyResult struct {
	Keep   []string
	Cached bool
}
