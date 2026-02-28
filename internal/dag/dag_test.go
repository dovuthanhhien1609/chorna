package dag

import (
	"errors"
	"testing"

	"github.com/hien/chorna/internal/task"
)

// helpers

func makeTask(id string) *task.Task {
	return task.New(id, "wf-test", nil, 0, 0)
}

func mustAdd(t *testing.T, d *DAG, tk *task.Task) {
	t.Helper()
	if err := d.AddTask(tk); err != nil {
		t.Fatalf("AddTask(%q) failed: %v", tk.ID, err)
	}
}

func mustEdge(t *testing.T, d *DAG, from, to string) {
	t.Helper()
	if err := d.AddEdge(from, to); err != nil {
		t.Fatalf("AddEdge(%q → %q) failed: %v", from, to, err)
	}
}

// --- AddTask ---

func TestAddTask_Success(t *testing.T) {
	d := New()
	tk := makeTask("A")
	mustAdd(t, d, tk)

	got, ok := d.Task("A")
	if !ok || got != tk {
		t.Errorf("Task(%q) = (%v, %v), want (%v, true)", "A", got, ok, tk)
	}
	if d.Size() != 1 {
		t.Errorf("Size() = %d, want 1", d.Size())
	}
}

func TestAddTask_DuplicateReturnsError(t *testing.T) {
	d := New()
	mustAdd(t, d, makeTask("A"))
	if err := d.AddTask(makeTask("A")); err == nil {
		t.Error("expected error for duplicate task ID, got nil")
	}
}

func TestAddTask_InitialInDegreeIsZero(t *testing.T) {
	d := New()
	mustAdd(t, d, makeTask("A"))
	if deg := d.InDegree("A"); deg != 0 {
		t.Errorf("InDegree after AddTask = %d, want 0", deg)
	}
}

// --- AddEdge ---

func TestAddEdge_IncreasesInDegree(t *testing.T) {
	d := New()
	mustAdd(t, d, makeTask("A"))
	mustAdd(t, d, makeTask("B"))
	mustEdge(t, d, "A", "B") // A must complete before B

	if deg := d.InDegree("B"); deg != 1 {
		t.Errorf("InDegree(B) = %d, want 1", deg)
	}
	if deg := d.InDegree("A"); deg != 0 {
		t.Errorf("InDegree(A) = %d, want 0 (source node)", deg)
	}
}

func TestAddEdge_UnknownSourceReturnsError(t *testing.T) {
	d := New()
	mustAdd(t, d, makeTask("B"))
	if err := d.AddEdge("X", "B"); err == nil {
		t.Error("expected error for unknown source task, got nil")
	}
}

func TestAddEdge_UnknownTargetReturnsError(t *testing.T) {
	d := New()
	mustAdd(t, d, makeTask("A"))
	if err := d.AddEdge("A", "Y"); err == nil {
		t.Error("expected error for unknown target task, got nil")
	}
}

func TestAddEdge_MultiplePrerequisites(t *testing.T) {
	d := New()
	for _, id := range []string{"A", "B", "C"} {
		mustAdd(t, d, makeTask(id))
	}
	mustEdge(t, d, "A", "C")
	mustEdge(t, d, "B", "C")

	if deg := d.InDegree("C"); deg != 2 {
		t.Errorf("InDegree(C) = %d, want 2", deg)
	}
}

// --- Dependents ---

func TestDependents_ReturnsSuccessors(t *testing.T) {
	d := New()
	for _, id := range []string{"A", "B", "C"} {
		mustAdd(t, d, makeTask(id))
	}
	mustEdge(t, d, "A", "B")
	mustEdge(t, d, "A", "C")

	deps := d.Dependents("A")
	if len(deps) != 2 {
		t.Fatalf("Dependents(A) = %v, want 2 elements", deps)
	}
	set := map[string]bool{}
	for _, id := range deps {
		set[id] = true
	}
	if !set["B"] || !set["C"] {
		t.Errorf("Dependents(A) = %v, want [B C]", deps)
	}
}

func TestDependents_NoDependentsReturnsEmpty(t *testing.T) {
	d := New()
	mustAdd(t, d, makeTask("A"))
	if deps := d.Dependents("A"); len(deps) != 0 {
		t.Errorf("Dependents(A) = %v, want []", deps)
	}
}

func TestDependents_ReturnsCopy(t *testing.T) {
	d := New()
	mustAdd(t, d, makeTask("A"))
	mustAdd(t, d, makeTask("B"))
	mustEdge(t, d, "A", "B")

	got := d.Dependents("A")
	got[0] = "MUTATED" // mutate the returned slice

	// The DAG's internal slice must be unchanged.
	if d.Dependents("A")[0] != "B" {
		t.Error("Dependents returned a live slice — mutation affected internal state")
	}
}

// --- DecrInDegree ---

func TestDecrInDegree_Correctness(t *testing.T) {
	d := New()
	mustAdd(t, d, makeTask("A"))
	mustAdd(t, d, makeTask("B"))
	mustEdge(t, d, "A", "B")

	newDeg, err := d.DecrInDegree("B")
	if err != nil {
		t.Fatalf("DecrInDegree failed: %v", err)
	}
	if newDeg != 0 {
		t.Errorf("after DecrInDegree, got %d, want 0", newDeg)
	}
}

func TestDecrInDegree_UnknownTaskReturnsError(t *testing.T) {
	d := New()
	if _, err := d.DecrInDegree("X"); err == nil {
		t.Error("expected error for unknown task, got nil")
	}
}

// --- Tasks (copy) ---

func TestTasks_ReturnsCopy(t *testing.T) {
	d := New()
	mustAdd(t, d, makeTask("A"))

	snapshot := d.Tasks()
	delete(snapshot, "A") // mutate the returned copy

	// The DAG must still have the task.
	if _, ok := d.Task("A"); !ok {
		t.Error("Tasks() returned a live map — deletion affected internal state")
	}
}

// --- ErrCycle (tested more thoroughly in topo_test.go) ---

func TestErrCycle_IsError(t *testing.T) {
	var err error = &ErrCycle{}
	var cycleErr *ErrCycle
	if !errors.As(err, &cycleErr) {
		t.Error("ErrCycle does not satisfy errors.As")
	}
	if err.Error() == "" {
		t.Error("ErrCycle.Error() returned empty string")
	}
}
