// Package worker implements a fixed-size goroutine pool with bounded
// backpressure for executing Tasks.
//
// # Concurrency model
//
// Submit is safe to call from multiple goroutines concurrently.
// Results() returns a receive-only channel; a single goroutine (the engine's
// dispatch loop) should consume it.
// Shutdown is idempotent.
//
// # Backpressure
//
// taskCh is a bounded channel of capacity queueDepth.  When it is full,
// Submit blocks until space is available, the caller's context is cancelled,
// or Shutdown is called.  This propagates backpressure from the worker pool
// up to the engine.
//
// # Shutdown signalling — why stopCh, not close(taskCh)
//
// In Go, sending to a closed channel panics even inside a select.  So we
// cannot close taskCh from Shutdown while Submit might concurrently be
// evaluating "case taskCh <- t".  Instead we use a separate stopCh channel:
//
//   • Submit selects on both taskCh and stopCh.  When stopCh is closed,
//     the send branch is never chosen.
//   • Workers select on both taskCh and stopCh.  When stopCh is closed they
//     perform a non-blocking drain of any remaining items, then exit.
//   • taskCh is never closed; only stopCh is closed during Shutdown.
//
// # Contract
//
// "No concurrent Submit + Shutdown" is still documented: a Submit call that
// races with Shutdown may enqueue a task after workers have completed their
// drain, causing it to be silently dropped.  In practice the engine cancels
// its context (terminating the blocking Submit) before calling Shutdown.
//
// # Panic safety
//
// Each TaskFunc is called inside a deferred recover.  A panicking task is
// treated identically to a task returning an error; the worker continues.
package worker

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"github.com/hien/chorna/internal/task"
)

// ErrPoolStopped is returned by Submit after Shutdown has been called.
var ErrPoolStopped = errors.New("worker pool: stopped")

// TaskResult carries the outcome of a single task execution.
// The engine reads these from Results() to drive state transitions and
// DAG advancement.
type TaskResult struct {
	Task       *task.Task
	Err        error     // nil on success; non-nil on failure or panic
	StartedAt  time.Time // when the worker began executing
	FinishedAt time.Time // when execution ended (success or failure)
}

// Pool is a fixed-size goroutine pool.  The zero value is not usable;
// construct with New.
type Pool struct {
	nWorkers int
	taskCh   chan *task.Task // bounded dispatch queue; never closed
	resultCh chan TaskResult // workers publish here; closed after all workers exit
	stopCh   chan struct{}   // closed once on Shutdown; signals Submit and workers

	wg   sync.WaitGroup // tracks live worker goroutines
	once sync.Once      // ensures stopCh is closed exactly once
}

// New creates a Pool with nWorkers goroutines and a task queue of queueDepth
// capacity.  Workers start immediately.  ctx is propagated to each TaskFunc
// (cancelling it cancels running tasks at the function level).
//
// Panics if nWorkers < 1 or queueDepth < 1.
func New(ctx context.Context, nWorkers, queueDepth int) *Pool {
	if nWorkers < 1 {
		panic("worker.New: nWorkers must be ≥ 1")
	}
	if queueDepth < 1 {
		panic("worker.New: queueDepth must be ≥ 1")
	}

	p := &Pool{
		nWorkers: nWorkers,
		taskCh: make(chan *task.Task, queueDepth),
		// Buffer to queueDepth+nWorkers so that every queued task plus every
		// in-flight task can deliver its result without blocking the worker.
		// This prevents a deadlock where workers block on resultCh while
		// Shutdown is waiting on wg.Wait() — the engine need not have a
		// concurrent reader active during Shutdown.
		resultCh: make(chan TaskResult, queueDepth+nWorkers),
		stopCh:   make(chan struct{}),
	}

	for range nWorkers {
		p.wg.Add(1)
		go p.work(ctx)
	}

	// Close resultCh only after every worker has exited, signalling to the
	// engine that no more results will arrive.
	go func() {
		p.wg.Wait()
		close(p.resultCh)
	}()

	return p
}

// Submit enqueues task t for execution.  Blocks (backpressure) if the queue
// is full — returns only when one of:
//   - the task was successfully enqueued (nil),
//   - Shutdown was called (ErrPoolStopped),
//   - ctx was cancelled (ctx.Err()).
//
// The task must be in StateReady; the worker transitions it to StateRunning.
func (p *Pool) Submit(ctx context.Context, t *task.Task) error {
	// Fast-path: if stopCh is already closed, return immediately without
	// entering the blocking select.  This prevents the ambiguity where both
	// "taskCh has space" and "stopCh is closed" are true simultaneously —
	// Go's select would pick randomly, making the result non-deterministic.
	// A non-blocking receive on a closed channel always succeeds instantly.
	select {
	case <-p.stopCh:
		return ErrPoolStopped
	default:
	}

	// Blocking path: wait for queue space, shutdown signal, or ctx cancel.
	select {
	case p.taskCh <- t:
		return nil
	case <-p.stopCh:
		return ErrPoolStopped
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Results returns the receive-only channel on which workers publish results.
// The channel is closed after Shutdown completes and all workers have exited.
// The engine should range over this channel until it is closed.
func (p *Pool) Results() <-chan TaskResult {
	return p.resultCh
}

// Shutdown signals the pool to stop accepting new work and waits for all
// queued and in-flight tasks to complete.
//
// shutCtx provides a deadline: if workers don't finish in time, Shutdown
// returns shutCtx.Err().  Shutdown is idempotent.
func (p *Pool) Shutdown(shutCtx context.Context) error {
	p.once.Do(func() {
		// Closing stopCh unblocks any Submit waiting in select and signals
		// all worker goroutines to drain taskCh then exit.
		// taskCh itself is never closed (see package-level comment).
		close(p.stopCh)
	})

	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-shutCtx.Done():
		return shutCtx.Err()
	}
}

// QueueLen returns the approximate number of tasks waiting in the queue.
func (p *Pool) QueueLen() int { return len(p.taskCh) }

// Workers returns the number of goroutines in the pool.
func (p *Pool) Workers() int { return p.nWorkers }

// ── internal ─────────────────────────────────────────────────────────────────

// work is the goroutine body for each worker.
//
// Main loop: select between a new task from taskCh and the shutdown signal
// from stopCh.  When stopCh fires, perform a non-blocking drain of any tasks
// already buffered in taskCh before exiting — no task is silently dropped.
func (p *Pool) work(ctx context.Context) {
	defer p.wg.Done()
	for {
		select {
		case t := <-p.taskCh:
			p.resultCh <- p.execute(ctx, t)

		case <-p.stopCh:
			// Drain any tasks that were already enqueued before shutdown.
			// Non-blocking: once the channel appears empty, exit immediately.
			// Multiple workers may drain concurrently; channel semantics ensure
			// each task is consumed by exactly one goroutine.
			for {
				select {
				case t := <-p.taskCh:
					p.resultCh <- p.execute(ctx, t)
				default:
					return
				}
			}
		}
	}
}

// execute runs task t and returns its result.
//
// Responsibility split:
//   - Worker  → Transition to Running  (worker "owns" the task during execution)
//   - Engine  → Transition to Completed or Failed (after reading the result)
//
// If the Running transition fails (e.g. task externally invalidated), the
// error is surfaced in TaskResult.Err and Execute is skipped.
func (p *Pool) execute(ctx context.Context, t *task.Task) TaskResult {
	res := TaskResult{
		Task:      t,
		StartedAt: time.Now(),
	}

	if err := t.Transition(task.StateRunning); err != nil {
		res.Err = fmt.Errorf("worker: transition to Running: %w", err)
		res.FinishedAt = time.Now()
		return res
	}

	res.Err = safeRun(ctx, t)
	res.FinishedAt = time.Now()
	return res
}

// safeRun calls t.Execute(ctx) and recovers from any panic, converting it to
// an error so the worker goroutine survives and continues processing.
func safeRun(ctx context.Context, t *task.Task) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v\n%s", r, debug.Stack())
		}
	}()
	return t.Execute(ctx)
}
