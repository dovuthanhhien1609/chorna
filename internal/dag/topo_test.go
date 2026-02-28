package dag

import (
	"errors"
	"sort"
	"testing"
)

// buildDAG is a convenience builder for topo tests.
// tasks is a list of IDs; edges is pairs [from, to].
func buildDAG(t *testing.T, tasks []string, edges [][2]string) *DAG {
	t.Helper()
	d := New()
	for _, id := range tasks {
		mustAdd(t, d, makeTask(id))
	}
	for _, e := range edges {
		mustEdge(t, d, e[0], e[1])
	}
	return d
}

// precedes reports whether `a` appears before `b` in order.
func precedes(order []string, a, b string) bool {
	ai, bi := -1, -1
	for i, id := range order {
		if id == a {
			ai = i
		}
		if id == b {
			bi = i
		}
	}
	return ai >= 0 && bi >= 0 && ai < bi
}

// --- Basic correctness ---

func TestTopoSort_SingleNode(t *testing.T) {
	d := buildDAG(t, []string{"A"}, nil)
	res, err := d.TopologicalSort()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Order) != 1 || res.Order[0] != "A" {
		t.Errorf("Order = %v, want [A]", res.Order)
	}
	if len(res.Layers) != 1 || len(res.Layers[0]) != 1 {
		t.Errorf("Layers = %v, want [[A]]", res.Layers)
	}
}

func TestTopoSort_LinearChain(t *testing.T) {
	// A → B → C → D
	d := buildDAG(t,
		[]string{"A", "B", "C", "D"},
		[][2]string{{"A", "B"}, {"B", "C"}, {"C", "D"}},
	)
	res, err := d.TopologicalSort()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Ordering constraints.
	for _, pair := range [][2]string{{"A", "B"}, {"B", "C"}, {"C", "D"}} {
		if !precedes(res.Order, pair[0], pair[1]) {
			t.Errorf("expected %s before %s in order %v", pair[0], pair[1], res.Order)
		}
	}
	// A linear chain must have 4 layers (one node per layer).
	if len(res.Layers) != 4 {
		t.Errorf("Layers count = %d, want 4", len(res.Layers))
	}
}

func TestTopoSort_DiamondShape(t *testing.T) {
	//   A
	//  / \
	// B   C
	//  \ /
	//   D
	d := buildDAG(t,
		[]string{"A", "B", "C", "D"},
		[][2]string{{"A", "B"}, {"A", "C"}, {"B", "D"}, {"C", "D"}},
	)
	res, err := d.TopologicalSort()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// A must come first, D must come last.
	if !precedes(res.Order, "A", "B") || !precedes(res.Order, "A", "C") {
		t.Errorf("A must precede B and C in %v", res.Order)
	}
	if !precedes(res.Order, "B", "D") || !precedes(res.Order, "C", "D") {
		t.Errorf("B and C must precede D in %v", res.Order)
	}
	// Layers: [A], [B, C], [D]
	if len(res.Layers) != 3 {
		t.Errorf("Layers count = %d, want 3", len(res.Layers))
	}
	// Layer 1 (B and C) may arrive in any order — sort before comparing.
	layer1 := make([]string, len(res.Layers[1]))
	copy(layer1, res.Layers[1])
	sort.Strings(layer1)
	if len(layer1) != 2 || layer1[0] != "B" || layer1[1] != "C" {
		t.Errorf("layer[1] = %v, want [B C]", res.Layers[1])
	}
}

func TestTopoSort_NoDependencies(t *testing.T) {
	// All 4 tasks are independent — valid ordering is any permutation.
	d := buildDAG(t, []string{"A", "B", "C", "D"}, nil)
	res, err := d.TopologicalSort()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Order) != 4 {
		t.Errorf("Order length = %d, want 4", len(res.Order))
	}
	// All tasks in a single layer.
	if len(res.Layers) != 1 || len(res.Layers[0]) != 4 {
		t.Errorf("Layers = %v, want one layer of 4", res.Layers)
	}
}

func TestTopoSort_WideAndDeep(t *testing.T) {
	// Two independent chains that merge at the end:
	//   A1 → A2 → A3
	//   B1 → B2 → B3
	//   A3, B3 → C (final merge)
	tasks := []string{"A1", "A2", "A3", "B1", "B2", "B3", "C"}
	edges := [][2]string{
		{"A1", "A2"}, {"A2", "A3"},
		{"B1", "B2"}, {"B2", "B3"},
		{"A3", "C"}, {"B3", "C"},
	}
	d := buildDAG(t, tasks, edges)
	res, err := d.TopologicalSort()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Ordering invariants.
	for _, pair := range [][2]string{
		{"A1", "A2"}, {"A2", "A3"}, {"A3", "C"},
		{"B1", "B2"}, {"B2", "B3"}, {"B3", "C"},
	} {
		if !precedes(res.Order, pair[0], pair[1]) {
			t.Errorf("%s must precede %s in %v", pair[0], pair[1], res.Order)
		}
	}
}

// --- Cycle detection ---

func TestTopoSort_DirectCycle(t *testing.T) {
	// A → B → A  (direct cycle)
	d := buildDAG(t,
		[]string{"A", "B"},
		[][2]string{{"A", "B"}, {"B", "A"}},
	)
	_, err := d.TopologicalSort()
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}
	var cycleErr *ErrCycle
	if !errors.As(err, &cycleErr) {
		t.Errorf("error type = %T, want *ErrCycle", err)
	}
}

func TestTopoSort_IndirectCycle(t *testing.T) {
	// A → B → C → A  (indirect cycle through three nodes)
	d := buildDAG(t,
		[]string{"A", "B", "C"},
		[][2]string{{"A", "B"}, {"B", "C"}, {"C", "A"}},
	)
	_, err := d.TopologicalSort()
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}
}

func TestTopoSort_SelfLoop(t *testing.T) {
	// A → A  (self-loop is the simplest cycle)
	d := New()
	mustAdd(t, d, makeTask("A"))
	// Manually insert self-loop (bypassing AddEdge since both from==to is valid graph notation).
	d.dependents["A"] = append(d.dependents["A"], "A")
	d.inDegree["A"]++

	_, err := d.TopologicalSort()
	if err == nil {
		t.Fatal("expected cycle error for self-loop, got nil")
	}
}

// --- Idempotency ---

func TestTopoSort_IsNonMutating(t *testing.T) {
	// Calling TopologicalSort twice must return equivalent results
	// and must not alter the DAG's internal in-degree counters.
	d := buildDAG(t,
		[]string{"A", "B", "C"},
		[][2]string{{"A", "B"}, {"A", "C"}},
	)

	// First sort.
	res1, err := d.TopologicalSort()
	if err != nil {
		t.Fatalf("first sort failed: %v", err)
	}
	// Second sort — in-degrees must be unchanged.
	res2, err := d.TopologicalSort()
	if err != nil {
		t.Fatalf("second sort failed: %v", err)
	}
	if len(res1.Order) != len(res2.Order) {
		t.Errorf("results differ between calls: %v vs %v", res1.Order, res2.Order)
	}
	// In-degree of A must still be 0 (not decremented by sort).
	if deg := d.InDegree("A"); deg != 0 {
		t.Errorf("InDegree(A) after two sorts = %d, want 0", deg)
	}
	if deg := d.InDegree("B"); deg != 1 {
		t.Errorf("InDegree(B) after two sorts = %d, want 1", deg)
	}
}
