package scheduler

import "github.com/hien/chorna/internal/task"

// FIFO is a first-in, first-out scheduler.  Tasks are dispatched in the
// exact order they were pushed.
type FIFO struct {
	queue []*task.Task
}

// NewFIFO returns an empty FIFO scheduler.
func NewFIFO() *FIFO { return &FIFO{} }

// Push appends t to the back of the queue.
func (f *FIFO) Push(t *task.Task) {
	f.queue = append(f.queue, t)
}

// Pop removes and returns the task at the front of the queue.
// Returns (nil, false) when empty.
func (f *FIFO) Pop() (*task.Task, bool) {
	if len(f.queue) == 0 {
		return nil, false
	}
	t := f.queue[0]
	f.queue = f.queue[1:]
	return t, true
}

// Len reports the number of tasks waiting.
func (f *FIFO) Len() int { return len(f.queue) }
