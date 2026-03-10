package scheduler

import "github.com/hien/chorna/internal/task"

// RoundRobin schedules tasks by cycling through workflow IDs in the order
// they were first seen.  Tasks within a single workflow are served FIFO.
//
// Example with workflows A and B, tasks pushed in order A1, A2, B1, A3, B2:
//
//	Pop order: A1, B1, A2, B2, A3
//
// This prevents any single workflow from monopolising the worker pool.
type RoundRobin struct {
	queues  map[string][]*task.Task // per-workflow FIFO queues
	order   []string                // workflow IDs in first-seen order
	current int                     // next index in order to pop from
	total   int                     // total tasks across all queues
}

// NewRoundRobin returns an empty RoundRobin scheduler.
func NewRoundRobin() *RoundRobin {
	return &RoundRobin{queues: make(map[string][]*task.Task)}
}

// Push enqueues t into its workflow's FIFO queue, registering the workflow
// in the rotation if this is the first task from that workflow.
func (r *RoundRobin) Push(t *task.Task) {
	id := t.WorkflowID
	if _, exists := r.queues[id]; !exists {
		r.order = append(r.order, id)
	}
	r.queues[id] = append(r.queues[id], t)
	r.total++
}

// Pop returns the next task according to the round-robin rotation.
// It advances the cursor past any exhausted workflow queues.
// Returns (nil, false) when all queues are empty.
func (r *RoundRobin) Pop() (*task.Task, bool) {
	if r.total == 0 {
		return nil, false
	}

	n := len(r.order)
	for i := 0; i < n; i++ {
		idx := (r.current + i) % n
		id := r.order[idx]
		q := r.queues[id]
		if len(q) == 0 {
			continue
		}
		t := q[0]
		r.queues[id] = q[1:]
		r.total--
		// Advance cursor past this slot so the next Pop starts from the
		// workflow after the one we just served.
		r.current = (idx + 1) % n
		return t, true
	}

	return nil, false // should be unreachable when r.total > 0
}

// Len reports the total number of tasks across all workflow queues.
func (r *RoundRobin) Len() int { return r.total }
