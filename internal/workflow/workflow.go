// Package workflow composes a DAG with task state tracking to form an
// executable workflow.  It is the primary type the engine operates on.
//
// Ownership model:
//
//	A Workflow owns its DAG and the Tasks within it.  The engine holds
//	references to Workflow objects and drives them forward through the
//	dispatch loop.  Workflow methods that touch the DAG delegate locking
//	to the DAG itself; the Workflow-level mu guards only the Status field
//	and the timing metadata.
package workflow

import (
	"fmt"
	"sync"
	"time"

	"github.com/hien/chorna/internal/dag"
	"github.com/hien/chorna/internal/task"
)

// Status is the coarse-grained state of an entire workflow.
type Status int

const (
	StatusPending   Status = iota // submitted, not yet started
	StatusRunning                 // at least one task has been dispatched
	StatusCompleted               // all tasks completed successfully
	StatusFailed                  // at least one task failed with no retries left
	StatusCancelled               // cancelled by operator or context
)

func (s Status) String() string {
	switch s {
	case StatusPending:
		return "Pending"
	case StatusRunning:
		return "Running"
	case StatusCompleted:
		return "Completed"
	case StatusFailed:
		return "Failed"
	case StatusCancelled:
		return "Cancelled"
	default:
		return fmt.Sprintf("Unknown(%d)", int(s))
	}
}

// Workflow is a named, validated DAG of tasks with lifecycle metadata.
type Workflow struct {
	ID          string
	Description string
	DAG         *dag.DAG

	mu         sync.Mutex
	status     Status
	createdAt  time.Time
	startedAt  time.Time
	finishedAt time.Time
}

// New creates a Workflow in the Pending state.
func New(id, description string) *Workflow {
	return &Workflow{
		ID:          id,
		Description: description,
		DAG:         dag.New(),
		status:      StatusPending,
		createdAt:   time.Now(),
	}
}

// AddTask registers a task with this workflow.  The task's WorkflowID must
// match this workflow's ID; if not, an error is returned.
func (w *Workflow) AddTask(t *task.Task) error {
	if t.WorkflowID != w.ID {
		return fmt.Errorf("workflow %q: task %q belongs to workflow %q", w.ID, t.ID, t.WorkflowID)
	}
	return w.DAG.AddTask(t)
}

// AddDependency declares that prerequisite must complete before dependent starts.
// Both tasks must already be registered via AddTask.
func (w *Workflow) AddDependency(prerequisite, dependent string) error {
	return w.DAG.AddEdge(prerequisite, dependent)
}

// Validate checks that:
//   - The workflow has at least one task.
//   - The DAG is acyclic (performs a full topological sort).
//
// Returns a descriptive error on failure.  Must be called before submitting
// to the engine.
func (w *Workflow) Validate() error {
	if w.DAG.Size() == 0 {
		return fmt.Errorf("workflow %q: no tasks registered", w.ID)
	}
	if _, err := w.DAG.TopologicalSort(); err != nil {
		return fmt.Errorf("workflow %q: %w", w.ID, err)
	}
	return nil
}

// ExecutionPlan returns the layered execution plan produced by topological sort.
// Layer 0 can run immediately; layer N requires all tasks in layers 0..N-1 to
// complete first.  Returns an error if the workflow has not been validated.
func (w *Workflow) ExecutionPlan() (*dag.TopoResult, error) {
	return w.DAG.TopologicalSort()
}

// ReadyTasks returns every task that is currently eligible for dispatch:
// its state is Pending and its in-degree in the DAG is 0.
//
// Concurrency note: this method is designed to be called by a single
// goroutine (the engine's dispatch loop).  The DAG read lock and task state
// lock are acquired separately (not nested), which is safe under this
// single-caller invariant.  In future milestones where the dispatch loop
// remains single-threaded this invariant is preserved by design.
func (w *Workflow) ReadyTasks() []*task.Task {
	var ready []*task.Task
	for id, t := range w.DAG.Tasks() {
		// Check in-degree (DAG read lock) before task state (task mutex)
		// to short-circuit quickly for tasks that still have unsatisfied deps.
		if w.DAG.InDegree(id) == 0 && t.State() == task.StatePending {
			ready = append(ready, t)
		}
	}
	return ready
}

// OnTaskCompleted is called by the engine after a task transitions to
// Completed.  It decrements the in-degree of each direct dependent and
// returns the IDs of tasks that are now unblocked (in-degree reached 0).
func (w *Workflow) OnTaskCompleted(id string) ([]string, error) {
	var unblocked []string
	for _, dep := range w.DAG.Dependents(id) {
		newDeg, err := w.DAG.DecrInDegree(dep)
		if err != nil {
			return nil, fmt.Errorf("workflow %q: %w", w.ID, err)
		}
		if newDeg == 0 {
			unblocked = append(unblocked, dep)
		}
	}
	return unblocked, nil
}

// SetStatus updates the workflow-level status.
func (w *Workflow) SetStatus(s Status) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.status = s
	switch s {
	case StatusRunning:
		if w.startedAt.IsZero() {
			w.startedAt = time.Now()
		}
	case StatusCompleted, StatusFailed, StatusCancelled:
		w.finishedAt = time.Now()
	}
}

// Status returns the current workflow status.
func (w *Workflow) Status() Status {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.status
}

// Snapshot returns an immutable view of the workflow's metadata and each
// task's current state.
func (w *Workflow) Snapshot() WorkflowSnapshot {
	w.mu.Lock()
	snap := WorkflowSnapshot{
		ID:          w.ID,
		Description: w.Description,
		Status:      w.status,
		CreatedAt:   w.createdAt,
		StartedAt:   w.startedAt,
		FinishedAt:  w.finishedAt,
	}
	w.mu.Unlock()

	tasks := w.DAG.Tasks()
	snap.Tasks = make([]task.TaskSnapshot, 0, len(tasks))
	for _, t := range tasks {
		snap.Tasks = append(snap.Tasks, t.Snapshot())
	}
	return snap
}

// WorkflowSnapshot is an immutable point-in-time view of a Workflow.
type WorkflowSnapshot struct {
	ID          string
	Description string
	Status      Status
	Tasks       []task.TaskSnapshot
	CreatedAt   time.Time
	StartedAt   time.Time
	FinishedAt  time.Time
}
