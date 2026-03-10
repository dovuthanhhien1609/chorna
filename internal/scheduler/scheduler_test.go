package scheduler_test

import (
	"context"
	"testing"

	"github.com/hien/chorna/internal/scheduler"
	"github.com/hien/chorna/internal/task"
)

// newTask is a test helper that builds a Task without a real work function.
func newTask(id, workflowID string, priority int) *task.Task {
	return task.New(id, workflowID, func(_ context.Context) error { return nil }, priority, 0)
}

// ids collects task IDs from successive Pop calls until the scheduler is empty.
func ids(s scheduler.Scheduler) []string {
	var out []string
	for {
		t, ok := s.Pop()
		if !ok {
			break
		}
		out = append(out, t.ID)
	}
	return out
}

// ── FIFO ─────────────────────────────────────────────────────────────────────

func TestFIFO_EmptyPop(t *testing.T) {
	f := scheduler.NewFIFO()
	got, ok := f.Pop()
	if ok || got != nil {
		t.Fatalf("Pop on empty FIFO: got (%v, %v), want (nil, false)", got, ok)
	}
}

func TestFIFO_LenAndOrder(t *testing.T) {
	f := scheduler.NewFIFO()

	tasks := []*task.Task{
		newTask("a", "wf", 0),
		newTask("b", "wf", 0),
		newTask("c", "wf", 0),
	}
	for _, task := range tasks {
		f.Push(task)
	}

	if f.Len() != 3 {
		t.Fatalf("Len = %d, want 3", f.Len())
	}

	want := []string{"a", "b", "c"}
	got := ids(f)
	if !equal(got, want) {
		t.Errorf("FIFO order = %v, want %v", got, want)
	}

	if f.Len() != 0 {
		t.Errorf("Len after drain = %d, want 0", f.Len())
	}
}

func TestFIFO_InterleavedPushPop(t *testing.T) {
	f := scheduler.NewFIFO()
	f.Push(newTask("a", "wf", 0))
	f.Push(newTask("b", "wf", 0))

	got, _ := f.Pop()
	if got.ID != "a" {
		t.Fatalf("first pop = %q, want %q", got.ID, "a")
	}

	f.Push(newTask("c", "wf", 0))

	want := []string{"b", "c"}
	if result := ids(f); !equal(result, want) {
		t.Errorf("after interleave: got %v, want %v", result, want)
	}
}

// ── Priority ──────────────────────────────────────────────────────────────────

func TestPriority_EmptyPop(t *testing.T) {
	p := scheduler.NewPriority()
	got, ok := p.Pop()
	if ok || got != nil {
		t.Fatalf("Pop on empty Priority: got (%v, %v), want (nil, false)", got, ok)
	}
}

func TestPriority_OrderByPriority(t *testing.T) {
	p := scheduler.NewPriority()
	p.Push(newTask("low", "wf", 1))
	p.Push(newTask("high", "wf", 10))
	p.Push(newTask("mid", "wf", 5))

	want := []string{"high", "mid", "low"}
	got := ids(p)
	if !equal(got, want) {
		t.Errorf("priority order = %v, want %v", got, want)
	}
}

func TestPriority_FIFOTieBreak(t *testing.T) {
	p := scheduler.NewPriority()
	// All same priority — should come out in insertion order.
	p.Push(newTask("first", "wf", 5))
	p.Push(newTask("second", "wf", 5))
	p.Push(newTask("third", "wf", 5))

	want := []string{"first", "second", "third"}
	got := ids(p)
	if !equal(got, want) {
		t.Errorf("FIFO tie-break = %v, want %v", got, want)
	}
}

func TestPriority_MixedPriorityAndFIFO(t *testing.T) {
	p := scheduler.NewPriority()
	// Push two tasks at priority 3, then one at priority 5.
	p.Push(newTask("p3-first", "wf", 3))
	p.Push(newTask("p3-second", "wf", 3))
	p.Push(newTask("p5", "wf", 5))

	want := []string{"p5", "p3-first", "p3-second"}
	got := ids(p)
	if !equal(got, want) {
		t.Errorf("mixed priority = %v, want %v", got, want)
	}
}

func TestPriority_Len(t *testing.T) {
	p := scheduler.NewPriority()
	for i := 0; i < 5; i++ {
		p.Push(newTask("t", "wf", i))
	}
	if p.Len() != 5 {
		t.Fatalf("Len = %d, want 5", p.Len())
	}
	ids(p) // drain
	if p.Len() != 0 {
		t.Fatalf("Len after drain = %d, want 0", p.Len())
	}
}

// ── RoundRobin ────────────────────────────────────────────────────────────────

func TestRoundRobin_EmptyPop(t *testing.T) {
	r := scheduler.NewRoundRobin()
	got, ok := r.Pop()
	if ok || got != nil {
		t.Fatalf("Pop on empty RoundRobin: got (%v, %v), want (nil, false)", got, ok)
	}
}

func TestRoundRobin_SingleWorkflow(t *testing.T) {
	r := scheduler.NewRoundRobin()
	r.Push(newTask("a", "wf1", 0))
	r.Push(newTask("b", "wf1", 0))
	r.Push(newTask("c", "wf1", 0))

	want := []string{"a", "b", "c"}
	got := ids(r)
	if !equal(got, want) {
		t.Errorf("single workflow = %v, want %v", got, want)
	}
}

func TestRoundRobin_TwoWorkflows(t *testing.T) {
	r := scheduler.NewRoundRobin()
	// wf1: a, b, c   wf2: x, y
	// Expected round-robin: a, x, b, y, c
	r.Push(newTask("a", "wf1", 0))
	r.Push(newTask("b", "wf1", 0))
	r.Push(newTask("c", "wf1", 0))
	r.Push(newTask("x", "wf2", 0))
	r.Push(newTask("y", "wf2", 0))

	want := []string{"a", "x", "b", "y", "c"}
	got := ids(r)
	if !equal(got, want) {
		t.Errorf("two workflows = %v, want %v", got, want)
	}
}

func TestRoundRobin_ThreeWorkflows(t *testing.T) {
	r := scheduler.NewRoundRobin()
	// Push order: wf1-a, wf2-x, wf3-p, wf1-b, wf2-y
	// Expected: wf1-a, wf2-x, wf3-p, wf1-b, wf2-y
	r.Push(newTask("a", "wf1", 0))
	r.Push(newTask("x", "wf2", 0))
	r.Push(newTask("p", "wf3", 0))
	r.Push(newTask("b", "wf1", 0))
	r.Push(newTask("y", "wf2", 0))

	want := []string{"a", "x", "p", "b", "y"}
	got := ids(r)
	if !equal(got, want) {
		t.Errorf("three workflows = %v, want %v", got, want)
	}
}

func TestRoundRobin_PushAfterPartialDrain(t *testing.T) {
	r := scheduler.NewRoundRobin()
	r.Push(newTask("a", "wf1", 0))
	r.Push(newTask("x", "wf2", 0))

	// Drain both.
	ids(r)
	if r.Len() != 0 {
		t.Fatalf("Len after drain = %d, want 0", r.Len())
	}

	// Push new tasks; wf1 and wf2 are already in the rotation.
	r.Push(newTask("b", "wf1", 0))
	r.Push(newTask("y", "wf2", 0))

	got := ids(r)
	// After draining wf1/wf2, cursor advanced; new tasks should still
	// alternate (exact start depends on where cursor landed, but both must appear).
	if len(got) != 2 {
		t.Errorf("expected 2 tasks after re-push, got %v", got)
	}
}

func TestRoundRobin_Len(t *testing.T) {
	r := scheduler.NewRoundRobin()
	r.Push(newTask("a", "wf1", 0))
	r.Push(newTask("b", "wf2", 0))
	r.Push(newTask("c", "wf1", 0))

	if r.Len() != 3 {
		t.Fatalf("Len = %d, want 3", r.Len())
	}
	ids(r)
	if r.Len() != 0 {
		t.Fatalf("Len after drain = %d, want 0", r.Len())
	}
}

// ── Interface compliance ───────────────────────────────────────────────────────

// TestSchedulerInterface ensures all three types satisfy the Scheduler interface
// at compile time.
func TestSchedulerInterface(_ *testing.T) {
	var _ scheduler.Scheduler = scheduler.NewFIFO()
	var _ scheduler.Scheduler = scheduler.NewPriority()
	var _ scheduler.Scheduler = scheduler.NewRoundRobin()
}

// ── helper ────────────────────────────────────────────────────────────────────

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
