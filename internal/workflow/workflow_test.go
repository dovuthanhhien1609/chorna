package workflow

import (
	"context"
	"sort"
	"testing"

	"github.com/hien/chorna/internal/task"
)

// helpers

func noop(_ context.Context) error { return nil }

func makeTask(id, wfID string) *task.Task {
	return task.New(id, wfID, noop, 0, 0)
}

func mustAddTask(t *testing.T, wf *Workflow, tk *task.Task) {
	t.Helper()
	if err := wf.AddTask(tk); err != nil {
		t.Fatalf("AddTask(%q) failed: %v", tk.ID, err)
	}
}

func mustAddDep(t *testing.T, wf *Workflow, pre, dep string) {
	t.Helper()
	if err := wf.AddDependency(pre, dep); err != nil {
		t.Fatalf("AddDependency(%q → %q) failed: %v", pre, dep, err)
	}
}

func sortedIDs(tasks []*task.Task) []string {
	ids := make([]string, len(tasks))
	for i, t := range tasks {
		ids[i] = t.ID
	}
	sort.Strings(ids)
	return ids
}

// --- Construction ---

func TestNew_InitialStatus(t *testing.T) {
	wf := New("wf-1", "test workflow")
	if wf.Status() != StatusPending {
		t.Errorf("initial status = %s, want Pending", wf.Status())
	}
}

// --- AddTask ---

func TestAddTask_WrongWorkflowID(t *testing.T) {
	wf := New("wf-1", "")
	tk := makeTask("t1", "wf-OTHER")
	if err := wf.AddTask(tk); err == nil {
		t.Error("expected error for mismatched WorkflowID, got nil")
	}
}

func TestAddTask_CorrectWorkflowID(t *testing.T) {
	wf := New("wf-1", "")
	mustAddTask(t, wf, makeTask("t1", "wf-1"))
}

// --- Validate ---

func TestValidate_EmptyWorkflow(t *testing.T) {
	wf := New("wf-1", "")
	if err := wf.Validate(); err == nil {
		t.Error("expected error for empty workflow, got nil")
	}
}

func TestValidate_NoDependencies(t *testing.T) {
	wf := New("wf-1", "")
	mustAddTask(t, wf, makeTask("t1", "wf-1"))
	mustAddTask(t, wf, makeTask("t2", "wf-1"))
	if err := wf.Validate(); err != nil {
		t.Errorf("unexpected error for valid workflow: %v", err)
	}
}

func TestValidate_CycleReturnsError(t *testing.T) {
	wf := New("wf-1", "")
	mustAddTask(t, wf, makeTask("A", "wf-1"))
	mustAddTask(t, wf, makeTask("B", "wf-1"))
	mustAddDep(t, wf, "A", "B")
	mustAddDep(t, wf, "B", "A") // creates a cycle
	if err := wf.Validate(); err == nil {
		t.Error("expected cycle error, got nil")
	}
}

func TestValidate_LinearChain(t *testing.T) {
	wf := New("wf-1", "")
	for _, id := range []string{"A", "B", "C"} {
		mustAddTask(t, wf, makeTask(id, "wf-1"))
	}
	mustAddDep(t, wf, "A", "B")
	mustAddDep(t, wf, "B", "C")
	if err := wf.Validate(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// --- ReadyTasks ---

func TestReadyTasks_AllIndependent(t *testing.T) {
	wf := New("wf-1", "")
	for _, id := range []string{"A", "B", "C"} {
		mustAddTask(t, wf, makeTask(id, "wf-1"))
	}
	ready := wf.ReadyTasks()
	if len(ready) != 3 {
		t.Errorf("ReadyTasks = %v, want 3 tasks", sortedIDs(ready))
	}
}

func TestReadyTasks_WithDependency(t *testing.T) {
	// A → B: only A is ready initially
	wf := New("wf-1", "")
	mustAddTask(t, wf, makeTask("A", "wf-1"))
	mustAddTask(t, wf, makeTask("B", "wf-1"))
	mustAddDep(t, wf, "A", "B")

	ready := wf.ReadyTasks()
	if len(ready) != 1 || ready[0].ID != "A" {
		t.Errorf("ReadyTasks = %v, want [A]", sortedIDs(ready))
	}
}

func TestReadyTasks_AfterTransitionToRunning_NotReturned(t *testing.T) {
	wf := New("wf-1", "")
	tk := makeTask("A", "wf-1")
	mustAddTask(t, wf, tk)

	// Simulate the engine dispatching A.
	_ = tk.Transition(task.StateReady)
	_ = tk.Transition(task.StateRunning)

	// A is no longer Pending so it must not appear in ReadyTasks.
	ready := wf.ReadyTasks()
	if len(ready) != 0 {
		t.Errorf("ReadyTasks after dispatch = %v, want []", sortedIDs(ready))
	}
}

// --- OnTaskCompleted ---

func TestOnTaskCompleted_UnblocksDependents(t *testing.T) {
	// A → B, A → C; after A completes both B and C should be unblocked.
	wf := New("wf-1", "")
	for _, id := range []string{"A", "B", "C"} {
		mustAddTask(t, wf, makeTask(id, "wf-1"))
	}
	mustAddDep(t, wf, "A", "B")
	mustAddDep(t, wf, "A", "C")

	unblocked, err := wf.OnTaskCompleted("A")
	if err != nil {
		t.Fatalf("OnTaskCompleted failed: %v", err)
	}
	sort.Strings(unblocked)
	if len(unblocked) != 2 || unblocked[0] != "B" || unblocked[1] != "C" {
		t.Errorf("unblocked = %v, want [B C]", unblocked)
	}
}

func TestOnTaskCompleted_DiamondConvergence(t *testing.T) {
	//   A
	//  / \
	// B   C
	//  \ /
	//   D
	wf := New("wf-1", "")
	for _, id := range []string{"A", "B", "C", "D"} {
		mustAddTask(t, wf, makeTask(id, "wf-1"))
	}
	mustAddDep(t, wf, "A", "B")
	mustAddDep(t, wf, "A", "C")
	mustAddDep(t, wf, "B", "D")
	mustAddDep(t, wf, "C", "D")

	// A completes → B and C unblocked.
	unblocked, err := wf.OnTaskCompleted("A")
	if err != nil {
		t.Fatalf("OnTaskCompleted(A) failed: %v", err)
	}
	sort.Strings(unblocked)
	if len(unblocked) != 2 {
		t.Errorf("after A: unblocked = %v, want [B C]", unblocked)
	}

	// B completes → D still blocked (C hasn't completed).
	unblocked, err = wf.OnTaskCompleted("B")
	if err != nil {
		t.Fatalf("OnTaskCompleted(B) failed: %v", err)
	}
	if len(unblocked) != 0 {
		t.Errorf("after B: unblocked = %v, want []", unblocked)
	}

	// C completes → D now unblocked.
	unblocked, err = wf.OnTaskCompleted("C")
	if err != nil {
		t.Fatalf("OnTaskCompleted(C) failed: %v", err)
	}
	if len(unblocked) != 1 || unblocked[0] != "D" {
		t.Errorf("after C: unblocked = %v, want [D]", unblocked)
	}
}

func TestOnTaskCompleted_LeafNode_NoUnblocked(t *testing.T) {
	wf := New("wf-1", "")
	mustAddTask(t, wf, makeTask("A", "wf-1"))
	unblocked, err := wf.OnTaskCompleted("A")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(unblocked) != 0 {
		t.Errorf("unblocked = %v, want []", unblocked)
	}
}

// --- Full walk-through (simulates what the engine will do) ---

func TestFullWalkThrough_LinearChain(t *testing.T) {
	// A → B → C
	// Simulate engine: dispatch A, complete A, dispatch B, complete B, dispatch C, complete C.
	wf := New("wf-1", "")
	tasks := map[string]*task.Task{}
	for _, id := range []string{"A", "B", "C"} {
		tk := makeTask(id, "wf-1")
		tasks[id] = tk
		mustAddTask(t, wf, tk)
	}
	mustAddDep(t, wf, "A", "B")
	mustAddDep(t, wf, "B", "C")

	if err := wf.Validate(); err != nil {
		t.Fatalf("Validate failed: %v", err)
	}

	// Step 1: only A is ready.
	ready := wf.ReadyTasks()
	if len(ready) != 1 || ready[0].ID != "A" {
		t.Fatalf("step1 ready = %v, want [A]", sortedIDs(ready))
	}
	_ = tasks["A"].Transition(task.StateReady)
	_ = tasks["A"].Transition(task.StateRunning)
	_ = tasks["A"].Transition(task.StateCompleted)

	// Step 2: complete A → B unblocked.
	unblocked, _ := wf.OnTaskCompleted("A")
	if len(unblocked) != 1 || unblocked[0] != "B" {
		t.Fatalf("after A: unblocked = %v, want [B]", unblocked)
	}
	// B is now Pending with in-degree 0.
	ready = wf.ReadyTasks()
	if len(ready) != 1 || ready[0].ID != "B" {
		t.Fatalf("step2 ready = %v, want [B]", sortedIDs(ready))
	}
	_ = tasks["B"].Transition(task.StateReady)
	_ = tasks["B"].Transition(task.StateRunning)
	_ = tasks["B"].Transition(task.StateCompleted)

	// Step 3: complete B → C unblocked.
	unblocked, _ = wf.OnTaskCompleted("B")
	if len(unblocked) != 1 || unblocked[0] != "C" {
		t.Fatalf("after B: unblocked = %v, want [C]", unblocked)
	}
	ready = wf.ReadyTasks()
	if len(ready) != 1 || ready[0].ID != "C" {
		t.Fatalf("step3 ready = %v, want [C]", sortedIDs(ready))
	}
	_ = tasks["C"].Transition(task.StateReady)
	_ = tasks["C"].Transition(task.StateRunning)
	_ = tasks["C"].Transition(task.StateCompleted)

	// Step 4: all done, no more ready tasks.
	ready = wf.ReadyTasks()
	if len(ready) != 0 {
		t.Errorf("final ReadyTasks = %v, want []", sortedIDs(ready))
	}
}

// --- Status ---

func TestSetStatus_SetsRunningTimestamp(t *testing.T) {
	wf := New("wf-1", "")
	wf.SetStatus(StatusRunning)
	snap := wf.Snapshot()
	if snap.StartedAt.IsZero() {
		t.Error("StartedAt not set after StatusRunning")
	}
}

func TestSetStatus_SetsFinishedTimestamp(t *testing.T) {
	wf := New("wf-1", "")
	wf.SetStatus(StatusCompleted)
	snap := wf.Snapshot()
	if snap.FinishedAt.IsZero() {
		t.Error("FinishedAt not set after StatusCompleted")
	}
}

// --- ExecutionPlan ---

func TestExecutionPlan_ReturnsLayers(t *testing.T) {
	wf := New("wf-1", "")
	for _, id := range []string{"A", "B", "C"} {
		mustAddTask(t, wf, makeTask(id, "wf-1"))
	}
	mustAddDep(t, wf, "A", "B")
	mustAddDep(t, wf, "A", "C")

	plan, err := wf.ExecutionPlan()
	if err != nil {
		t.Fatalf("ExecutionPlan failed: %v", err)
	}
	// Layer 0: [A]; Layer 1: [B, C]
	if len(plan.Layers) != 2 {
		t.Errorf("layer count = %d, want 2", len(plan.Layers))
	}
}
