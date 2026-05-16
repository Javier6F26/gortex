package clones

import (
	"hash/fnv"
	"sort"
)

// Item pairs a graph node ID with its MinHash signature — the input
// unit for the LSH detection pass.
type Item struct {
	ID  string
	Sig Signature
}

// Pair is a detected clone relationship between two symbols, carrying
// the estimated Jaccard similarity of their bodies. A is always the
// lexicographically smaller ID so a pair has one canonical form.
type Pair struct {
	A          string
	B          string
	Similarity float64
}

// Cluster is a connected component of the clone graph — a set of
// symbols that are all transitively near-duplicates of one another.
type Cluster struct {
	Members       []string
	Size          int
	AvgSimilarity float64
}

// Index is an LSH banding index over MinHash signatures. Signatures
// are split into Bands bands of Rows rows; two signatures land in the
// same bucket of some band iff those Rows slots are identical, which
// makes them a candidate pair worth an exact-similarity check.
type Index struct {
	bands [Bands]map[uint64][]string
	sigs  map[string]Signature

	// Skipped-bucket counters set by the most recent CandidatePairs
	// call. Reads are safe only when no concurrent CandidatePairs is
	// running on the same Index. Exposed via SkippedBuckets so callers
	// (DetectPairs) can log how much fan-out the cap dropped without
	// changing the exported CandidatePairs signature.
	lastSkippedBuckets     int
	lastSkippedBucketItems int
}

// SkippedBuckets returns (bucket_count, total_items_in_skipped_buckets)
// for the most recent CandidatePairs call. Both values are 0 when the
// cap never tripped — i.e. every bucket was small enough that the
// pairwise expansion was performed in full.
func (ix *Index) SkippedBuckets() (int, int) {
	return ix.lastSkippedBuckets, ix.lastSkippedBucketItems
}

// NewIndex returns an empty LSH index.
func NewIndex() *Index {
	ix := &Index{sigs: make(map[string]Signature)}
	for b := range ix.bands {
		ix.bands[b] = make(map[uint64][]string)
	}
	return ix
}

// Add inserts one signed item into the index. Adding the same ID twice
// keeps the last signature but double-banks the band buckets; callers
// should add each ID once.
func (ix *Index) Add(id string, sig Signature) {
	ix.sigs[id] = sig
	for b := range Bands {
		key := bandKey(b, sig)
		ix.bands[b][key] = append(ix.bands[b][key], id)
	}
}

// bandKey hashes the Rows MinHash slots of band b into a bucket key.
// The band index is folded into the hash so identical row values in
// different bands cannot collide into the same logical bucket.
func bandKey(band int, sig Signature) uint64 {
	h := fnv.New64a()
	var buf [8]byte
	put := func(v uint32) {
		buf[0] = byte(v)
		buf[1] = byte(v >> 8)
		buf[2] = byte(v >> 16)
		buf[3] = byte(v >> 24)
		h.Write(buf[:4])
	}
	put(uint32(band) + 1)
	for r := range Rows {
		put(sig[band*Rows+r])
	}
	return h.Sum64()
}

// maxBucketSize is the per-band-bucket fan-out cap. Buckets larger
// than this are skipped entirely: a bucket carrying N items emits
// N·(N-1)/2 candidate pairs, which for N=2000 is 2M pairs per band ×
// Bands = ~64M pairs from one bucket alone. At repo-set scale the
// large buckets are dominated by template / boilerplate signatures
// (empty getters, single-line lambdas, react component shells) whose
// pairwise comparisons are noise rather than signal — every "real"
// clone of practical interest still has ≥1 small-bucket band hit
// because MinHash banding is the AND of multiple bands.
//
// Without this cap, the warmup phase that runs detectClonesAndEmitEdges
// against a 200k-function multi-repo graph stalled for 20+ minutes
// inside CandidatePairs (observed in production profile #4 stall).
const maxBucketSize = 256

// CandidatePairs returns every unordered pair of IDs that collide in at
// least one band bucket. Each pair is returned once, in canonical
// (A < B) form. This is the candidate set the exact Jaccard filter
// runs over — it is a superset of the true clone pairs.
//
// Memory note: the dedup uses a uint64-keyed map (FNV-1a hash of the
// canonicalised pair) instead of map[[2]string]struct{}. For 10M
// candidates the string-pair version held ~600 MB of map state; the
// uint64 form is ~120 MB. The 64-bit hash space has a negligible false-
// positive rate at this candidate volume, and a missed duplicate just
// means one redundant Jaccard estimate, which is cheap.
func (ix *Index) CandidatePairs() []Pair {
	seen := make(map[uint64]struct{})
	var pairs []Pair
	var skippedBuckets, skippedBucketItems int
	for b := range Bands {
		for _, ids := range ix.bands[b] {
			if len(ids) < 2 {
				continue
			}
			if len(ids) > maxBucketSize {
				skippedBuckets++
				skippedBucketItems += len(ids)
				continue
			}
			for i := range ids {
				for j := i + 1; j < len(ids); j++ {
					a, c := ids[i], ids[j]
					if a == c {
						continue
					}
					if a > c {
						a, c = c, a
					}
					key := pairKey(a, c)
					if _, ok := seen[key]; ok {
						continue
					}
					seen[key] = struct{}{}
					pairs = append(pairs, Pair{A: a, B: c})
				}
			}
		}
	}
	// Stash the bucket-skip telemetry so callers (DetectPairs) can
	// surface it via the logger without changing this function's
	// signature, which is exported.
	ix.lastSkippedBuckets = skippedBuckets
	ix.lastSkippedBucketItems = skippedBucketItems
	return pairs
}

// pairKey produces a 64-bit hash of a canonicalised (a < c) ID pair,
// using FNV-1a in two passes seeded by the offset basis. Inline and
// allocation-free so the hot-loop dedup stays fast.
func pairKey(a, c string) uint64 {
	const (
		offset uint64 = 14695981039346656037
		prime  uint64 = 1099511628211
	)
	h := offset
	for i := 0; i < len(a); i++ {
		h ^= uint64(a[i])
		h *= prime
	}
	// Sentinel byte between the two halves so the hashes of ("ab","c")
	// and ("a","bc") diverge.
	h ^= 0x1f
	h *= prime
	for i := 0; i < len(c); i++ {
		h ^= uint64(c[i])
		h *= prime
	}
	return h
}

// DetectPairs runs the full LSH detection pass: it bands every item,
// gathers candidate pairs, then keeps only those whose exact estimated
// Jaccard similarity is at or above threshold. Results are sorted by
// descending similarity, then by ID, so output is deterministic.
//
// A threshold ≤ 0 falls back to DefaultThreshold.
func DetectPairs(items []Item, threshold float64) []Pair {
	pairs, _, _ := DetectPairsWithStats(items, threshold)
	return pairs
}

// DetectPairsWithStats is DetectPairs plus the per-bucket-cap
// telemetry from the underlying Index.CandidatePairs run. Callers that
// want to know how much fan-out the cap dropped (warmup-time
// orchestrator logging) use this; everything else can stay on
// DetectPairs and ignore the counters.
func DetectPairsWithStats(items []Item, threshold float64) (pairs []Pair, skippedBuckets, skippedBucketItems int) {
	if threshold <= 0 {
		threshold = DefaultThreshold
	}
	ix := NewIndex()
	for _, it := range items {
		ix.Add(it.ID, it.Sig)
	}
	candidates := ix.CandidatePairs()
	skippedBuckets, skippedBucketItems = ix.SkippedBuckets()
	out := make([]Pair, 0, len(candidates))
	for _, p := range candidates {
		sa, oka := ix.sigs[p.A]
		sb, okb := ix.sigs[p.B]
		if !oka || !okb {
			continue
		}
		sim := EstimateJaccard(sa, sb)
		if sim < threshold {
			continue
		}
		p.Similarity = sim
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Similarity != out[j].Similarity {
			return out[i].Similarity > out[j].Similarity
		}
		if out[i].A != out[j].A {
			return out[i].A < out[j].A
		}
		return out[i].B < out[j].B
	})
	return out, skippedBuckets, skippedBucketItems
}

// ClusterPairs groups detected pairs into connected components via
// union-find. Each returned cluster lists its members sorted, its size,
// and the average similarity over the detected pairs that fall inside
// it. Clusters are sorted by descending size, then by first member.
func ClusterPairs(pairs []Pair) []Cluster {
	parent := make(map[string]string)
	find := func(x string) string {
		root := x
		for parent[root] != root {
			root = parent[root]
		}
		// path compression
		for parent[x] != root {
			parent[x], x = root, parent[x]
		}
		return root
	}
	add := func(x string) {
		if _, ok := parent[x]; !ok {
			parent[x] = x
		}
	}
	union := func(a, b string) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}
	for _, p := range pairs {
		add(p.A)
		add(p.B)
		union(p.A, p.B)
	}

	members := make(map[string][]string)
	for id := range parent {
		root := find(id)
		members[root] = append(members[root], id)
	}
	simSum := make(map[string]float64)
	simCnt := make(map[string]int)
	for _, p := range pairs {
		root := find(p.A)
		simSum[root] += p.Similarity
		simCnt[root]++
	}

	clusters := make([]Cluster, 0, len(members))
	for root, ids := range members {
		sort.Strings(ids)
		avg := 0.0
		if simCnt[root] > 0 {
			avg = simSum[root] / float64(simCnt[root])
		}
		clusters = append(clusters, Cluster{
			Members:       ids,
			Size:          len(ids),
			AvgSimilarity: avg,
		})
	}
	sort.Slice(clusters, func(i, j int) bool {
		if clusters[i].Size != clusters[j].Size {
			return clusters[i].Size > clusters[j].Size
		}
		return clusters[i].Members[0] < clusters[j].Members[0]
	})
	return clusters
}
