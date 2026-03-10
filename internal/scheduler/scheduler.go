// Package scheduler provides pluggable task-scheduling strategies for the
// Chrona engine.
//
// All implementations satisfy the Scheduler interface, which the engine uses
// to decide the order in which ready tasks are dispatched to the worker pool.
//
// # Concurrency
//
// Scheduler implementations are NOT goroutine-safe.  They are designed for
// use by the engine's single-threaded dispatch loop; no external locking is
// required.
//
// # Available strategies
//
//   - FIFO       – tasks are served in arrival order (default)
//   - Priority   – higher task.Priority value is served first; equal-priority
//     tasks are served FIFO
//   - RoundRobin – tasks cycle across workflow IDs; tasks within a workflow
//     are served FIFO
package scheduler

import "github.com/hien/chorna/internal/task"

// Scheduler is the interface all scheduling strategies must satisfy.
type Scheduler interface {
	// Push enqueues t for scheduling.
	Push(t *task.Task)

	// Pop dequeues the next task according to the strategy.
	// Returns (nil, false) when the scheduler is empty.
	Pop() (*task.Task, bool)

	// Len reports the number of tasks currently queued.
	Len() int
}
