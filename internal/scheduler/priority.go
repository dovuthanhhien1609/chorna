package scheduler

import (
	"container/heap"

	"github.com/hien/chorna/internal/task"
)

// Priority is a max-priority scheduler.  Tasks with a higher task.Priority
// value are dispatched first.  Tasks with equal priority are served FIFO
// (insertion order is preserved as a tie-breaker via a sequence counter).
type Priority struct {
	h   priorityHeap
	seq int // monotonically increasing insertion counter
}

// NewPriority returns an empty Priority scheduler.
func NewPriority() *Priority { return &Priority{} }

// Push enqueues t, maintaining the heap invariant.
func (p *Priority) Push(t *task.Task) {
	heap.Push(&p.h, priorityItem{task: t, seq: p.seq})
	p.seq++
}

// Pop removes and returns the highest-priority task.
// Returns (nil, false) when empty.
func (p *Priority) Pop() (*task.Task, bool) {
	if p.h.Len() == 0 {
		return nil, false
	}
	return heap.Pop(&p.h).(priorityItem).task, true
}

// Len reports the number of tasks waiting.
func (p *Priority) Len() int { return p.h.Len() }

// ── heap.Interface implementation ────────────────────────────────────────────

type priorityItem struct {
	task *task.Task
	seq  int // insertion sequence; smaller = earlier (FIFO tie-break)
}

type priorityHeap []priorityItem

func (h priorityHeap) Len() int { return len(h) }

// Less reports whether item i should be popped before item j.
// Higher task.Priority wins; equal priorities resolve by insertion order (FIFO).
func (h priorityHeap) Less(i, j int) bool {
	if h[i].task.Priority != h[j].task.Priority {
		return h[i].task.Priority > h[j].task.Priority
	}
	return h[i].seq < h[j].seq
}

func (h priorityHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *priorityHeap) Push(x any) { *h = append(*h, x.(priorityItem)) }

func (h *priorityHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}
