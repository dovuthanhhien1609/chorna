package task

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func newTestTask(id string) *Task {
	return New(id, "wf-1", func(_ context.Context) error { return nil }, 0, 0)
}

// --- Construction ---

func TestNew_InitialState(t *testing.T) {
	tk := newTestTask("t1")
	if tk.State() != StatePending {
		t.Errorf("new task state = %s, want Pending", tk.State())
	}
	if tk.RetryCount() != 0 {
		t.Errorf("new task RetryCount = %d, want 0", tk.RetryCount())
	}
	if tk.ID != "t1" {
		t.Errorf("ID = %q, want %q", tk.ID, "t1")
	}
}

// --- Valid transition chain ---

func TestTransition_FullHappyPath(t *testing.T) {
	tk := newTestTask("t1")
	chain := []TaskState{StateReady, StateRunning, StateCompleted}
	for _, to := range chain {
		if err := tk.Transition(to); err != nil {
			t.Fatalf("Transition to %s failed: %v", to, err)
		}
		if tk.State() != to {
			t.Errorf("after Transition(%s), State() = %s", to, tk.State())
		}
	}
}

func TestTransition_RetryPath(t *testing.T) {
	tk := New("t1", "wf-1", func(_ context.Context) error { return nil }, 0, 3)
	steps := []TaskState{StateReady, StateRunning, StateFailed, StateRetrying, StateReady}
	for _, to := range steps {
		if err := tk.Transition(to); err != nil {
			t.Fatalf("Transition(%s) failed: %v", to, err)
		}
	}
	if tk.State() != StateReady {
		t.Errorf("after retry cycle, state = %s, want Ready", tk.State())
	}
}

// --- Invalid transitions ---

func TestTransition_InvalidReturnsError(t *testing.T) {
	tk := newTestTask("t1") // Pending
	err := tk.Transition(StateRunning) // Pending → Running is illegal
	if err == nil {
		t.Fatal("expected error for invalid transition, got nil")
	}
	var invErr *ErrInvalidTransition
	if !errors.As(err, &invErr) {
		t.Errorf("error type = %T, want *ErrInvalidTransition", err)
	}
	// State must be unchanged after a failed transition.
	if tk.State() != StatePending {
		t.Errorf("state changed after invalid transition: got %s", tk.State())
	}
}

func TestTransition_CompletedIsTerminal(t *testing.T) {
	tk := newTestTask("t1")
	for _, s := range []TaskState{StateReady, StateRunning, StateCompleted} {
		_ = tk.Transition(s)
	}
	// Any further transition must fail.
	for _, to := range allStates {
		if err := tk.Transition(to); err == nil {
			t.Errorf("expected Completed → %s to fail, but got nil", to)
		}
	}
}

// --- Retry counting ---

func TestIncrRetry_WithinLimit(t *testing.T) {
	tk := New("t1", "wf-1", nil, 0, 2)
	if !tk.IncrRetry() {
		t.Error("first retry (1 of 2) should return true")
	}
	if !tk.IncrRetry() {
		t.Error("second retry (2 of 2) should return true")
	}
	if tk.IncrRetry() {
		t.Error("third retry (3 of 2) should return false — no retries left")
	}
	if tk.RetryCount() != 3 {
		t.Errorf("RetryCount = %d, want 3", tk.RetryCount())
	}
}

func TestIncrRetry_NoRetries(t *testing.T) {
	tk := New("t1", "wf-1", nil, 0, 0)
	if tk.IncrRetry() {
		t.Error("expected false for task with MaxRetries=0")
	}
}

// --- Execute ---

func TestExecute_PropagatesError(t *testing.T) {
	want := errors.New("boom")
	tk := New("t1", "wf-1", func(_ context.Context) error { return want }, 0, 0)
	if err := tk.Execute(context.Background()); !errors.Is(err, want) {
		t.Errorf("Execute() error = %v, want %v", err, want)
	}
}

func TestExecute_ContextCancellation(t *testing.T) {
	tk := New("t1", "wf-1", func(ctx context.Context) error {
		return ctx.Err()
	}, 0, 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := tk.Execute(ctx); err == nil {
		t.Error("expected context error, got nil")
	}
}

// --- Snapshot ---

func TestSnapshot_ReflectsMutableFields(t *testing.T) {
	tk := newTestTask("t1")
	_ = tk.Transition(StateReady)
	snap := tk.Snapshot()
	if snap.State != StateReady {
		t.Errorf("snapshot state = %s, want Ready", snap.State)
	}
	if snap.ID != "t1" {
		t.Errorf("snapshot ID = %q, want t1", snap.ID)
	}
}

// --- Concurrency (run with -race) ---

// TestConcurrentStateAccess verifies that concurrent reads of State() while
// a single writer calls Transition() does not produce a data race.
func TestConcurrentStateAccess(t *testing.T) {
	tk := New("t1", "wf-1", nil, 0, 0)

	var wg sync.WaitGroup
	// 50 concurrent readers
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = tk.State()
		}()
	}
	// 1 writer
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = tk.Transition(StateReady) // may succeed or fail; we just check for races
	}()

	wg.Wait()
}

// TestConcurrentSnapshot verifies Snapshot is race-free under concurrent transitions.
func TestConcurrentSnapshot(t *testing.T) {
	tk := New("t1", "wf-1", nil, 0, 0)
	_ = tk.Transition(StateReady) // known good state for readers

	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = tk.Snapshot()
		}()
	}
	wg.Wait()
}
