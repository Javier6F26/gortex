package cfg

import (
	"math/bits"
	"sort"
)

// Definition is one (statement, variable) write site. ID is the
// definition's bit position in the analysis bitsets.
type Definition struct {
	ID   int    `json:"id"`
	Stmt int    `json:"stmt"`
	Var  string `json:"var"`
}

// UseChain links one variable read to every definition that can
// reach it along some control-flow path. Defs holds statement
// indices, ascending.
type UseChain struct {
	Stmt int    `json:"stmt"`
	Var  string `json:"var"`
	Defs []int  `json:"defs"`
}

// ReachingResult is the fixpoint output: the definition table, the
// per-block IN/OUT sets (definition IDs), and the statement-granular
// def→use chains.
type ReachingResult struct {
	Defs   []Definition
	Chains []UseChain
	In     [][]int
	Out    [][]int
}

// ChainsFor returns the chains attached to one statement.
func (r *ReachingResult) ChainsFor(stmt int) []UseChain {
	var out []UseChain
	for _, c := range r.Chains {
		if c.Stmt == stmt {
			out = append(out, c)
		}
	}
	return out
}

// ReachingDefinitions runs the classic GEN/KILL monotone fixpoint
// over the CFG:
//
//	IN[b]  = ∪ OUT[p] for p ∈ preds(b)
//	OUT[b] = GEN[b] ∪ (IN[b] − KILL[b])
//
// then replays each block's statements against its IN set to link
// every use to the definitions reaching it. Bitsets keep the
// per-block transfer functions O(defs/64).
func (c *CFG) ReachingDefinitions() *ReachingResult {
	res := &ReachingResult{}

	// 1. Number every definition and group them by variable.
	defsByVar := map[string][]int{}
	defID := map[[2]interface{}]int{} // (stmt, var) → def ID; stmts dedupe vars already
	for _, st := range c.Stmts {
		for _, v := range st.Defs {
			id := len(res.Defs)
			res.Defs = append(res.Defs, Definition{ID: id, Stmt: st.Index, Var: v})
			defsByVar[v] = append(defsByVar[v], id)
			defID[[2]interface{}{st.Index, v}] = id
		}
	}
	nDefs := len(res.Defs)
	nBlocks := len(c.Blocks)
	words := (nDefs + 63) / 64

	newSet := func() bitset { return make(bitset, words) }
	allDefsOf := func(v string) bitset {
		s := newSet()
		for _, id := range defsByVar[v] {
			s.set(id)
		}
		return s
	}

	// 2. Per-block GEN (downward-exposed defs) and KILL (every def of
	// a variable the block writes).
	gen := make([]bitset, nBlocks)
	kill := make([]bitset, nBlocks)
	for i, bl := range c.Blocks {
		g, k := newSet(), newSet()
		last := map[string]int{}
		for _, st := range bl.Stmts {
			for _, v := range st.Defs {
				k.or(allDefsOf(v))
				last[v] = defID[[2]interface{}{st.Index, v}]
			}
		}
		for _, id := range last {
			g.set(id)
		}
		gen[i], kill[i] = g, k
	}

	// 3. Predecessor / successor lists.
	preds := make([][]int, nBlocks)
	succs := make([][]int, nBlocks)
	for _, e := range c.Edges {
		preds[e.To] = append(preds[e.To], e.From)
		succs[e.From] = append(succs[e.From], e.To)
	}

	// 4. Worklist fixpoint.
	in := make([]bitset, nBlocks)
	out := make([]bitset, nBlocks)
	for i := range c.Blocks {
		in[i], out[i] = newSet(), newSet()
		out[i].or(gen[i])
	}
	work := make([]int, 0, nBlocks)
	inWork := make([]bool, nBlocks)
	for i := 0; i < nBlocks; i++ {
		work = append(work, i)
		inWork[i] = true
	}
	for len(work) > 0 {
		b := work[0]
		work = work[1:]
		inWork[b] = false

		newIn := newSet()
		for _, p := range preds[b] {
			newIn.or(out[p])
		}
		in[b] = newIn
		newOut := newIn.clone()
		newOut.andNot(kill[b])
		newOut.or(gen[b])
		if !newOut.equal(out[b]) {
			out[b] = newOut
			for _, s := range succs[b] {
				if !inWork[s] {
					work = append(work, s)
					inWork[s] = true
				}
			}
		}
	}

	// 5. Statement-granular replay: thread the live set through each
	// block, linking uses before applying the statement's defs.
	for bi, bl := range c.Blocks {
		live := in[bi].clone()
		for _, st := range bl.Stmts {
			for _, v := range st.Uses {
				ids := defsByVar[v]
				if len(ids) == 0 {
					continue
				}
				var reach []int
				for _, id := range ids {
					if live.get(id) {
						reach = append(reach, res.Defs[id].Stmt)
					}
				}
				if len(reach) == 0 {
					continue
				}
				sort.Ints(reach)
				res.Chains = append(res.Chains, UseChain{Stmt: st.Index, Var: v, Defs: reach})
			}
			for _, v := range st.Defs {
				live.andNot(allDefsOf(v))
				live.set(defID[[2]interface{}{st.Index, v}])
			}
		}
	}
	sort.SliceStable(res.Chains, func(i, j int) bool {
		if res.Chains[i].Stmt != res.Chains[j].Stmt {
			return res.Chains[i].Stmt < res.Chains[j].Stmt
		}
		return res.Chains[i].Var < res.Chains[j].Var
	})

	// 6. Export IN/OUT as sorted definition-ID lists.
	res.In = make([][]int, nBlocks)
	res.Out = make([][]int, nBlocks)
	for i := 0; i < nBlocks; i++ {
		res.In[i] = in[i].ids()
		res.Out[i] = out[i].ids()
	}
	return res
}

// bitset is a fixed-width bit vector over definition IDs.
type bitset []uint64

func (s bitset) set(i int) { s[i/64] |= 1 << (uint(i) % 64) }
func (s bitset) get(i int) bool {
	return s[i/64]&(1<<(uint(i)%64)) != 0
}

func (s bitset) or(o bitset) {
	for i := range s {
		s[i] |= o[i]
	}
}

func (s bitset) andNot(o bitset) {
	for i := range s {
		s[i] &^= o[i]
	}
}

func (s bitset) clone() bitset {
	c := make(bitset, len(s))
	copy(c, s)
	return c
}

func (s bitset) equal(o bitset) bool {
	for i := range s {
		if s[i] != o[i] {
			return false
		}
	}
	return true
}

// ids enumerates the set bits ascending.
func (s bitset) ids() []int {
	var out []int
	for w, word := range s {
		for word != 0 {
			out = append(out, w*64+bits.TrailingZeros64(word))
			word &= word - 1
		}
	}
	return out
}
