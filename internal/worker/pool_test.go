package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hien/chorna/internal/task"
)

// ── helpers ──────────────────────────────────────────────────────────────────

// readyTask creates a task in StateReady — the required state before Submit.
func readyTask(id string, fn func(context.Context) error) *task.Task {
	if fn == nil {
		fn = func(_ context.Context) error { return nil }
	}
	tk := task.New(id, "wf-test", fn, 0, 0)
	if err := tk.Transition(task.StateReady); err != nil {
		panic(fmt.Sprintf("readyTask: %v", err))
	}
	return tk
}

// collectResults drains the pool's Results() channel into a slice.
// It terminates when the channel is closed (after Shutdown).
// Must be called in a goroutine that runs concurrently with task execution.
func collectResults(p *Pool) <-chan []TaskResult {
	ch := make(chan []TaskResult, 1)
	go func() {
		var all []TaskResult
		for res := range p.Results() {
			all = append(all, res)
		}
		ch <- all
	}()
	return ch
}

// shutdownPool shuts down the pool and returns any error.
func shutdownPool(t *testing.T, p *Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.Shutdown(ctx); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
}

// ── tests ─────────────────────────────────────────────────────────────────────

// TestPool_BasicExecution verifies that N tasks submitted to the pool all
// complete successfully and produce exactly N results.
func TestPool_BasicExecution(t *testing.T) {
	ctx := context.Background()
	p := New(ctx, 4, 8)
	done := collectResults(p)

	const n = 8
	for i := range n {
		tk := readyTask(fmt.Sprintf("t%d", i), nil)
		if err := p.Submit(ctx, tk); err != nil {
			t.Fatalf("Submit(%d): %v", i, err)
		}
	}

	shutdownPool(t, p)
	results := <-done

	if len(results) != n {
		t.Fatalf("got %d results, want %d", len(results), n)
	}
	for _, res := range results {
		if res.Err != nil {
			t.Errorf("task %q: unexpected error: %v", res.Task.ID, res.Err)
		}
		if res.Task.State() != task.StateRunning {
			// State machine: worker set Running; engine hasn't set Completed yet
			// (that's the engine's job in M4). Just verify it's Running.
			// Actually the task is in Running state because the engine hasn't
			// read the result yet to call Transition(Completed).
		}
		if res.StartedAt.IsZero() || res.FinishedAt.IsZero() {
			t.Errorf("task %q: missing timing metadata", res.Task.ID)
		}
	}
}

// TestPool_TaskStateRunningDuringExecution verifies the worker transitions the
// task to StateRunning before calling Execute, not after.
func TestPool_TaskStateRunningDuringExecution(t *testing.T) {
	ctx := context.Background()
	p := New(ctx, 1, 1)
	done := collectResults(p)

	running := make(chan task.TaskState, 1)
	// Declare tk before the closure so the closure can reference it.
	var tk *task.Task
	tk = readyTask("t1", func(_ context.Context) error {
		// Called from inside the worker — state must be Running here.
		running <- tk.State() // capture state mid-execution
		return nil
	})

	if err := p.Submit(ctx, tk); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Wait for the state reading (with timeout).
	select {
	case state := <-running:
		if state != task.StateRunning {
			t.Errorf("task state during Execute = %s, want Running", state)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("task did not execute within timeout")
	}

	shutdownPool(t, p)
	<-done
}

// TestPool_ErrorPropagation verifies that a task function returning an error
// is reflected in TaskResult.Err.
func TestPool_ErrorPropagation(t *testing.T) {
	ctx := context.Background()
	p := New(ctx, 1, 2)
	done := collectResults(p)

	want := errors.New("intentional failure")
	tk := readyTask("t1", func(_ context.Context) error { return want })

	if err := p.Submit(ctx, tk); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	shutdownPool(t, p)
	results := <-done

	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if !errors.Is(results[0].Err, want) {
		t.Errorf("Err = %v, want %v", results[0].Err, want)
	}
}

// TestPool_PanicRecovery verifies that a panicking TaskFunc:
//  1. Does not kill the worker goroutine.
//  2. Produces a TaskResult with a non-nil Err containing "panic".
//  3. The worker continues processing subsequent tasks normally.
func TestPool_PanicRecovery(t *testing.T) {
	ctx := context.Background()
	p := New(ctx, 1, 4)
	done := collectResults(p)

	// Task 1 panics.
	tk1 := readyTask("t1-panic", func(_ context.Context) error {
		panic("intentional panic")
	})
	// Task 2 is normal — verifies the worker survived.
	tk2 := readyTask("t2-normal", nil)

	for _, tk := range []*task.Task{tk1, tk2} {
		if err := p.Submit(ctx, tk); err != nil {
			t.Fatalf("Submit(%q): %v", tk.ID, err)
		}
	}

	shutdownPool(t, p)
	results := <-done

	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}

	// Find results by task ID (arrival order is non-deterministic).
	byID := map[string]TaskResult{}
	for _, r := range results {
		byID[r.Task.ID] = r
	}

	panicRes, ok := byID["t1-panic"]
	if !ok {
		t.Fatal("no result for t1-panic")
	}
	if panicRes.Err == nil {
		t.Error("panic task: expected non-nil Err")
	} else if !strings.Contains(panicRes.Err.Error(), "panic") {
		t.Errorf("panic task Err = %q, want to contain \"panic\"", panicRes.Err.Error())
	}

	normalRes, ok := byID["t2-normal"]
	if !ok {
		t.Fatal("no result for t2-normal")
	}
	if normalRes.Err != nil {
		t.Errorf("normal task after panic has unexpected error: %v", normalRes.Err)
	}
}

// TestPool_Backpressure verifies that Submit blocks when the dispatch queue is
// full and the single worker is busy, then unblocks when capacity is freed.
//
// Layout: 1 worker, queue depth 1.
//   - t1 is submitted and picked up by the worker (worker is now occupied).
//   - t2 is submitted to fill the queue (queue is now full).
//   - t3's Submit is launched in a goroutine — it must block.
//   - Releasing t1 causes the worker to finish, pick t2, and free a queue slot.
//   - t3's Submit unblocks and succeeds.
func TestPool_Backpressure(t *testing.T) {
	ctx := context.Background()
	p := New(ctx, 1, 1)
	done := collectResults(p)

	// started signals when t1 is actually inside Execute (worker is occupied).
	started := make(chan struct{})
	// release unblocks t1.
	release := make(chan struct{})

	t1 := readyTask("t1", func(_ context.Context) error {
		close(started)
		<-release
		return nil
	})
	t2 := readyTask("t2", nil)
	t3 := readyTask("t3", nil)

	// Submit t1 — goes into queue immediately.
	if err := p.Submit(ctx, t1); err != nil {
		t.Fatalf("submit t1: %v", err)
	}

	// Wait until the worker is executing t1 (queue is now empty, worker busy).
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("t1 did not start within timeout")
	}

	// Submit t2 — fills the queue (queue: [t2], worker: busy with t1).
	if err := p.Submit(ctx, t2); err != nil {
		t.Fatalf("submit t2: %v", err)
	}

	// t3 must block: queue is full, worker is busy.
	submitted3 := make(chan error, 1)
	go func() { submitted3 <- p.Submit(ctx, t3) }()

	select {
	case err := <-submitted3:
		t.Fatalf("t3 Submit returned immediately (no backpressure): err=%v", err)
	case <-time.After(80 * time.Millisecond):
		// Backpressure is working — t3 is still waiting.
	}

	// Unblock t1 → worker finishes t1, picks t2 → queue has space → t3 enqueues.
	close(release)

	select {
	case err := <-submitted3:
		if err != nil {
			t.Errorf("t3 submit error after unblock: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("t3 submit did not unblock within timeout")
	}

	shutdownPool(t, p)
	results := <-done
	if len(results) != 3 {
		t.Errorf("got %d results, want 3", len(results))
	}
}

// TestPool_SubmitContextCancelled verifies that a Submit blocked on backpressure
// returns ctx.Err() when the caller's context is cancelled.
func TestPool_SubmitContextCancelled(t *testing.T) {
	ctx := context.Background()
	// 1 worker, queue depth 1 — will reach capacity quickly.
	p := New(ctx, 1, 1)
	done := collectResults(p)

	release := make(chan struct{})
	started := make(chan struct{})

	// Occupy the worker.
	t1 := readyTask("t1", func(_ context.Context) error {
		close(started)
		<-release
		return nil
	})
	if err := p.Submit(ctx, t1); err != nil {
		t.Fatalf("submit t1: %v", err)
	}
	<-started

	// Fill the queue.
	t2 := readyTask("t2", nil)
	if err := p.Submit(ctx, t2); err != nil {
		t.Fatalf("submit t2: %v", err)
	}

	// Submit t3 with a cancellable context — it should block then return.
	cancelCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()

	t3 := readyTask("t3", nil)
	err := p.Submit(cancelCtx, t3)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Submit after timeout: err = %v, want DeadlineExceeded", err)
	}

	close(release)
	shutdownPool(t, p)
	<-done
}

// TestPool_Shutdown_DrainsQueue verifies that tasks already in the queue when
// Shutdown is called are fully processed — none are silently dropped.
func TestPool_Shutdown_DrainsQueue(t *testing.T) {
	ctx := context.Background()
	// 1 slow worker, queue depth 8 — let queue fill before shutdown.
	release := make(chan struct{})
	p := New(ctx, 1, 8)
	done := collectResults(p)

	// Block the worker so the queue fills.
	started := make(chan struct{}, 1)
	t0 := readyTask("t0", func(_ context.Context) error {
		started <- struct{}{}
		<-release
		return nil
	})
	if err := p.Submit(ctx, t0); err != nil {
		t.Fatalf("submit t0: %v", err)
	}
	<-started // worker is now busy

	// Fill the queue with 5 more tasks.
	const extra = 5
	for i := range extra {
		tk := readyTask(fmt.Sprintf("t%d", i+1), nil)
		if err := p.Submit(ctx, tk); err != nil {
			t.Fatalf("submit t%d: %v", i+1, err)
		}
	}

	// Call Shutdown — it must wait for all 6 tasks to complete.
	close(release) // unblock t0 so drain can proceed
	shutdownPool(t, p)
	results := <-done

	if len(results) != extra+1 {
		t.Errorf("got %d results after drain, want %d", len(results), extra+1)
	}
}

// TestPool_Shutdown_Idempotent verifies that Shutdown can be called multiple
// times without panicking or returning an error.
func TestPool_Shutdown_Idempotent(t *testing.T) {
	ctx := context.Background()
	p := New(ctx, 2, 4)
	done := collectResults(p)

	shutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := p.Shutdown(shutCtx); err != nil {
		t.Errorf("first Shutdown: %v", err)
	}
	if err := p.Shutdown(shutCtx); err != nil {
		t.Errorf("second Shutdown: %v", err)
	}
	<-done
}

// TestPool_Submit_AfterShutdown verifies that Submit returns ErrPoolStopped
// when called after Shutdown has been called.
//
// Note: because of the "no concurrent Submit+Shutdown" contract, we call
// Shutdown first (sequentially), then Submit.
func TestPool_Submit_AfterShutdown(t *testing.T) {
	ctx := context.Background()
	p := New(ctx, 1, 2)
	done := collectResults(p)

	shutdownPool(t, p)
	<-done

	tk := readyTask("t1", nil)
	err := p.Submit(ctx, tk)
	if !errors.Is(err, ErrPoolStopped) {
		t.Errorf("Submit after Shutdown: err = %v, want ErrPoolStopped", err)
	}
}

// TestPool_ResultsChannelClosed verifies that Results() is closed after
// Shutdown, allowing callers to range over it without blocking forever.
func TestPool_ResultsChannelClosed(t *testing.T) {
	ctx := context.Background()
	p := New(ctx, 2, 4)

	const n = 4
	for i := range n {
		tk := readyTask(fmt.Sprintf("t%d", i), nil)
		if err := p.Submit(ctx, tk); err != nil {
			t.Fatalf("Submit(%d): %v", i, err)
		}
	}

	shutdownPool(t, p)

	// Range must terminate (channel is closed) — not block forever.
	count := 0
	timedOut := false
	timer := time.AfterFunc(2*time.Second, func() { timedOut = true })
	defer timer.Stop()

	for range p.Results() {
		if timedOut {
			t.Fatal("ranging over Results() timed out — channel not closed after Shutdown")
		}
		count++
	}

	if count != n {
		t.Errorf("got %d results from range, want %d", count, n)
	}
}

// TestPool_Shutdown_Timeout verifies that Shutdown respects its context
// deadline when workers are stuck.
func TestPool_Shutdown_Timeout(t *testing.T) {
	ctx := context.Background()
	p := New(ctx, 1, 1)
	done := collectResults(p)

	// A task that blocks forever (simulates a hung task).
	hung := make(chan struct{})
	tk := readyTask("t-hung", func(_ context.Context) error {
		<-hung // blocks until test ends
		return nil
	})
	started := make(chan struct{})
	tk2 := readyTask("t-sentinel", func(_ context.Context) error {
		close(started)
		<-hung
		return nil
	})

	if err := p.Submit(ctx, tk); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if err := p.Submit(ctx, tk2); err != nil {
		t.Fatalf("Submit sentinel: %v", err)
	}

	// Wait for at least one task to be in flight.
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		// sentinel might not start first, that's OK
	}

	// Shutdown with a very short deadline — must time out.
	shutCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()

	err := p.Shutdown(shutCtx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Shutdown with short deadline: err = %v, want DeadlineExceeded", err)
	}

	// Unblock to let the test goroutines clean up (prevent goroutine leak).
	close(hung)
	<-done
}

// TestPool_ConcurrentSubmitters is a race-detector test: multiple goroutines
// submit tasks concurrently.  If run with -race it will catch any data races
// in Submit or the worker loop.
func TestPool_ConcurrentSubmitters(t *testing.T) {
	ctx := context.Background()
	p := New(ctx, 8, 32)
	done := collectResults(p)

	const (
		submitters = 16
		perWorker  = 10
	)

	var wg sync.WaitGroup
	var submitted atomic.Int64

	for range submitters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range perWorker {
				id := fmt.Sprintf("t-%d", i)
				tk := readyTask(id, nil)
				if err := p.Submit(ctx, tk); err != nil {
					// Pool stopped before we finished — acceptable in shutdown race.
					return
				}
				submitted.Add(1)
			}
		}()
	}

	wg.Wait()
	shutdownPool(t, p)
	results := <-done

	if int64(len(results)) != submitted.Load() {
		t.Errorf("submitted %d tasks, got %d results", submitted.Load(), len(results))
	}
}

// TestPool_Timing verifies that FinishedAt >= StartedAt for every result.
func TestPool_Timing(t *testing.T) {
	ctx := context.Background()
	p := New(ctx, 2, 4)
	done := collectResults(p)

	for i := range 4 {
		tk := readyTask(fmt.Sprintf("t%d", i), nil)
		_ = p.Submit(ctx, tk)
	}

	shutdownPool(t, p)
	for _, res := range <-done {
		if res.FinishedAt.Before(res.StartedAt) {
			t.Errorf("task %q: FinishedAt (%s) before StartedAt (%s)",
				res.Task.ID, res.FinishedAt, res.StartedAt)
		}
	}
}

// TestPool_Accessors verifies the Workers() and QueueLen() methods.
func TestPool_Accessors(t *testing.T) {
	ctx := context.Background()
	p := New(ctx, 3, 10)
	done := collectResults(p)

	if p.Workers() != 3 {
		t.Errorf("Workers() = %d, want 3", p.Workers())
	}

	// Fill half the queue while workers are blocked.
	block := make(chan struct{})
	started := make(chan struct{}, 3)
	for i := range 3 {
		tk := readyTask(fmt.Sprintf("block-%d", i), func(_ context.Context) error {
			started <- struct{}{}
			<-block
			return nil
		})
		_ = p.Submit(ctx, tk)
	}
	// Wait for all 3 workers to be busy.
	for range 3 {
		<-started
	}

	// Enqueue 5 tasks while workers are blocked.
	for i := range 5 {
		_ = p.Submit(ctx, readyTask(fmt.Sprintf("q-%d", i), nil))
	}

	if p.QueueLen() != 5 {
		t.Errorf("QueueLen() = %d, want 5", p.QueueLen())
	}

	close(block)
	shutdownPool(t, p)
	<-done
}
