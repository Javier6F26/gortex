package languages

import (
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

// lsExtractGo runs the Go extractor over src and returns the emitted
// nodes, so a test can read the loop-region signals StampLoopSignals
// stamps on a function/method node's Meta.
func lsExtractGo(t *testing.T, src string) []*graph.Node {
	t.Helper()
	res, err := NewGoExtractor().Extract("signals.go", []byte(src))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	return res.Nodes
}

func lsNodeByName(t *testing.T, nodes []*graph.Node, name string) *graph.Node {
	t.Helper()
	for _, n := range nodes {
		if n != nil && n.Name == name {
			return n
		}
	}
	t.Fatalf("node %q not found", name)
	return nil
}

func lsMetaInt(n *graph.Node, key string) (int, bool) {
	if n == nil || n.Meta == nil {
		return 0, false
	}
	v, ok := n.Meta[key]
	if !ok {
		return 0, false
	}
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	}
	return 0, false
}

func lsMetaBool(n *graph.Node, key string) (val, present bool) {
	if n == nil || n.Meta == nil {
		return false, false
	}
	v, ok := n.Meta[key]
	if !ok {
		return false, false
	}
	b, _ := v.(bool)
	return b, true
}

// TestLoopSignals_MaxAccessDepth pins the documented counting: the deepest
// member-access chain length is the number of identifier segments in it —
// a.b.c.d.e is 5 (four selector hops plus the base operand).
func TestLoopSignals_MaxAccessDepth(t *testing.T) {
	nodes := lsExtractGo(t, `package p

func deep() {
	_ = a.b.c.d.e
}
`)
	got, ok := lsMetaInt(lsNodeByName(t, nodes, "deep"), "max_access_depth")
	if !ok {
		t.Fatal("max_access_depth not stamped")
	}
	if got != 5 {
		t.Fatalf("max_access_depth = %d, want 5 (segments of a.b.c.d.e)", got)
	}
}

// TestLoopSignals_LinearScanInLoop proves loop-region precision for a
// linear-scan call: the same Contains call is flagged inside a loop and
// not flagged outside one.
func TestLoopSignals_LinearScanInLoop(t *testing.T) {
	nodes := lsExtractGo(t, `package p

func scanInLoop(xs []int, v int) bool {
	for _, x := range xs {
		_ = x
		if slices.Contains(xs, v) {
			return true
		}
	}
	return false
}

func scanNoLoop(xs []int, v int) bool {
	return slices.Contains(xs, v)
}
`)
	if v, present := lsMetaBool(lsNodeByName(t, nodes, "scanInLoop"), "linear_scan_in_loop"); !present || !v {
		t.Fatalf("scanInLoop: linear_scan_in_loop present=%v val=%v, want true", present, v)
	}
	if _, present := lsMetaBool(lsNodeByName(t, nodes, "scanNoLoop"), "linear_scan_in_loop"); present {
		t.Fatal("scanNoLoop: linear_scan_in_loop must not be flagged outside a loop")
	}
}

// TestLoopSignals_AllocInLoop proves loop-region precision for an
// allocation: an append inside a loop is flagged; a composite literal and
// append outside a loop are not.
func TestLoopSignals_AllocInLoop(t *testing.T) {
	nodes := lsExtractGo(t, `package p

func allocInLoop(n int) []int {
	var xs []int
	for i := 0; i < n; i++ {
		xs = append(xs, i)
	}
	return xs
}

func allocNoLoop() []int {
	xs := []int{}
	xs = append(xs, 1)
	return xs
}
`)
	if v, present := lsMetaBool(lsNodeByName(t, nodes, "allocInLoop"), "alloc_in_loop"); !present || !v {
		t.Fatalf("allocInLoop: alloc_in_loop present=%v val=%v, want true", present, v)
	}
	if _, present := lsMetaBool(lsNodeByName(t, nodes, "allocNoLoop"), "alloc_in_loop"); present {
		t.Fatal("allocNoLoop: alloc_in_loop must not be flagged outside a loop")
	}
}

// TestLoopSignals_RecursionInLoop proves the self-recursion-in-loop signal
// distinguishes a self-call inside a loop (flagged) from a self-call
// outside a loop and a call to a different function inside a loop (both not
// flagged).
func TestLoopSignals_RecursionInLoop(t *testing.T) {
	nodes := lsExtractGo(t, `package p

func recurInLoop(n int) {
	for i := 0; i < n; i++ {
		recurInLoop(i)
	}
}

func recurNoLoop(n int) {
	if n > 0 {
		recurNoLoop(n - 1)
	}
}

func callsOtherInLoop(n int) {
	for i := 0; i < n; i++ {
		other(i)
	}
}
`)
	if v, present := lsMetaBool(lsNodeByName(t, nodes, "recurInLoop"), "recursion_in_loop"); !present || !v {
		t.Fatalf("recurInLoop: recursion_in_loop present=%v val=%v, want true", present, v)
	}
	if _, present := lsMetaBool(lsNodeByName(t, nodes, "recurNoLoop"), "recursion_in_loop"); present {
		t.Fatal("recurNoLoop: recursion_in_loop must not be flagged outside a loop")
	}
	if _, present := lsMetaBool(lsNodeByName(t, nodes, "callsOtherInLoop"), "recursion_in_loop"); present {
		t.Fatal("callsOtherInLoop: a call to a different function must not be flagged as recursion")
	}
}
