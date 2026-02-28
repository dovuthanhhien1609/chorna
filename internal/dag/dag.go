// Package dag implements a directed acyclic graph of tasks.
//
// Nodes are Tasks; directed edges represent dependencies.  An edge A → B means
// "A must complete before B may start."  The graph is append-only after
// construction: tasks and edges are added, never removed.
//
// Concurrency model:
//
//	All structural mutations (AddTask, AddEdge, DecrInDegree) take the write
//	lock.  Read operations (Task, Tasks, Dependents, InDegree) take the read
//	lock and return copies so callers never hold the lock while processing
//	results.  The engine's dispatch loop is the sole caller of DecrInDegree
//	and ReadyTasks — keeping those calls single-goroutine — so no lock
//	ordering issues can arise between DAG mutations and task state transitions.
package dag

import (
	"fmt"
	"sync"

	"github.com/hien/chorna/internal/task"
)

// DAG is a directed acyclic graph of tasks.
// The zero value is not usable; construct with New.
type DAG struct {
	mu         sync.RWMutex
	tasks      map[string]*task.Task
	dependents map[string][]string // id → ids of tasks unblocked when this one completes
	inDegree   map[string]int      // id → number of unsatisfied prerequisite tasks
}

// New returns an empty, ready-to-use DAG.
func New() *DAG {
	return &DAG{
		tasks:      make(map[string]*task.Task),
		dependents: make(map[string][]string),
		inDegree:   make(map[string]int),
	}
}

// AddTask registers a task in the graph.  Its initial in-degree is 0 (no
// prerequisites).  Returns an error if a task with the same ID already exists.
func (d *DAG) AddTask(t *task.Task) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, exists := d.tasks[t.ID]; exists {
		return fmt.Errorf("dag: task %q already registered", t.ID)
	}
	d.tasks[t.ID] = t
	d.inDegree[t.ID] = 0
	return nil
}

// AddEdge declares that task `from` must complete before task `to` may start.
// Both tasks must have been registered with AddTask first.
// Duplicate edges are permitted (and simply increment in-degree again) because
// cycle detection happens at Validate/TopologicalSort time, not here.
func (d *DAG) AddEdge(from, to string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.tasks[from]; !ok {
		return fmt.Errorf("dag: source task %q not found", from)
	}
	if _, ok := d.tasks[to]; !ok {
		return fmt.Errorf("dag: target task %q not found", to)
	}
	d.dependents[from] = append(d.dependents[from], to)
	d.inDegree[to]++
	return nil
}

// Task returns the task registered under id, or (nil, false) if not found.
// The returned pointer is the live task; callers must not store it past the
// lifetime of the engine.
func (d *DAG) Task(id string) (*task.Task, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	t, ok := d.tasks[id]
	return t, ok
}

// Tasks returns a shallow copy of the task map.  The map itself is safe to
// iterate without holding any lock, but the Task pointers inside are live.
func (d *DAG) Tasks() map[string]*task.Task {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make(map[string]*task.Task, len(d.tasks))
	for k, v := range d.tasks {
		out[k] = v
	}
	return out
}

// Dependents returns a copy of the successor list for `id` — the tasks that
// become eligible to run once `id` completes.
func (d *DAG) Dependents(id string) []string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	src := d.dependents[id]
	out := make([]string, len(src))
	copy(out, src)
	return out
}

// InDegree returns the current number of unsatisfied prerequisites for `id`.
// A value of 0 means the task is eligible to be dispatched (assuming it is
// still in the Pending state).
func (d *DAG) InDegree(id string) int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.inDegree[id]
}

// DecrInDegree decrements the in-degree of `id` by one and returns the new
// value.  Called by the engine when one of `id`'s prerequisites completes.
// Returns an error if `id` is not registered.
func (d *DAG) DecrInDegree(id string) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.tasks[id]; !ok {
		return 0, fmt.Errorf("dag: task %q not found", id)
	}
	d.inDegree[id]--
	return d.inDegree[id], nil
}

// Size returns the number of registered tasks.
func (d *DAG) Size() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.tasks)
}
