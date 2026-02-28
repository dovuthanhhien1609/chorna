package task

import (
	"context"
	"sync"
	"time"
)

// Func is the signature every task's work function must satisfy.
// Implementations must be idempotent: because Chrona guarantees at-least-once
// execution, the same Func may be called more than once on crash recovery.
type Func func(ctx context.Context) error

// Task is the atomic unit of work inside a workflow.
//
// Fields set at construction (ID, WorkflowID, Priority, MaxRetries, fn) are
// immutable and safe to read without locks.  The mutable fields (state,
// retryCount, timestamps) are protected by mu and accessed only through the
// exported methods below.
type Task struct {
	ID         string
	WorkflowID string
	Priority   int // higher value = higher priority; used by Priority scheduler
	MaxRetries int // 0 means no retries

	fn func(ctx context.Context) error // unexported; call Execute to invoke

	mu         sync.Mutex
	state      TaskState
	retryCount int
	createdAt  time.Time
	updatedAt  time.Time
}

// New constructs a Task in the Pending state.
func New(id, workflowID string, fn Func, priority, maxRetries int) *Task {
	now := time.Now()
	return &Task{
		ID:         id,
		WorkflowID: workflowID,
		fn:         fn,
		Priority:   priority,
		MaxRetries: maxRetries,
		state:      StatePending,
		createdAt:  now,
		updatedAt:  now,
	}
}

// State returns the current state.  Safe for concurrent reads.
func (t *Task) State() TaskState {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state
}

// Transition attempts to move the task to state `to`.
// Returns *ErrInvalidTransition if the move is not permitted by the state machine.
// The transition is atomic: state and updatedAt are both updated under the lock.
func (t *Task) Transition(to TaskState) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !IsValidTransition(t.state, to) {
		return &ErrInvalidTransition{From: t.state, To: to}
	}
	t.state = to
	t.updatedAt = time.Now()
	return nil
}

// IncrRetry increments the retry counter and reports whether retries remain.
// Returns true if retryCount <= MaxRetries (the caller may schedule another attempt).
// This must be called after Transition(StateFailed) and before Transition(StateRetrying).
func (t *Task) IncrRetry() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.retryCount++
	return t.retryCount <= t.MaxRetries
}

// RetryCount returns the number of retry attempts made so far.
func (t *Task) RetryCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.retryCount
}

// Execute runs the task's function with the provided context.
// It does NOT change state — that is the engine's responsibility.
func (t *Task) Execute(ctx context.Context) error {
	return t.fn(ctx)
}

// Snapshot returns a point-in-time copy of the task's mutable fields.
// Useful for persistence and observability without holding the lock.
func (t *Task) Snapshot() TaskSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	return TaskSnapshot{
		ID:         t.ID,
		WorkflowID: t.WorkflowID,
		State:      t.state,
		RetryCount: t.retryCount,
		Priority:   t.Priority,
		MaxRetries: t.MaxRetries,
		CreatedAt:  t.createdAt,
		UpdatedAt:  t.updatedAt,
	}
}

// TaskSnapshot is an immutable, lock-free view of a Task at a point in time.
type TaskSnapshot struct {
	ID         string
	WorkflowID string
	State      TaskState
	RetryCount int
	Priority   int
	MaxRetries int
	CreatedAt  time.Time
	UpdatedAt  time.Time
}
