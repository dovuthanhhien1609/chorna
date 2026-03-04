# Chrona

A deterministic concurrent workflow execution engine built in Go from scratch.

Chrona is a **systems-level learning project** designed to implement and
deeply understand:

- Directed Acyclic Graph (DAG) execution
- Task dependency tracking via topological sort (Kahn's algorithm)
- Fixed-size goroutine pools with bounded backpressure
- Task lifecycle state machines
- Pluggable scheduling strategies (FIFO, Priority, Round-Robin)
- Crash recovery via Write-Ahead Logging
- Graceful shutdown with context cancellation
- Concurrency correctness: no races, no deadlocks, no goroutine leaks

---

## Architecture at a Glance

```
  ┌──────────────────────────────────────────┐
  │              WorkflowEngine              │
  │  ReadyTasks() → Scheduler → pool.Submit  │
  │  pool.Results() → Transition → DAG update│
  └───────┬──────────────┬───────────────────┘
          │              │
  ┌───────▼──────┐  ┌────▼─────────────────────┐
  │  DAG Engine  │  │       Worker Pool        │
  │  topo sort   │  │  N goroutines            │
  │  in-degree   │  │  bounded queue           │
  │  tracking    │  │  backpressure + recovery │
  └──────────────┘  └──────────────────────────┘
          │
  ┌───────▼──────────────────┐
  │    Task State Machine    │
  │  Pending→Ready→Running   │
  │  →Completed / →Retrying  │
  └──────────────────────────┘
```

Task lifecycle:

```
Pending ──► Ready ──► Running ──► Completed
                              ↘
                               Failed ──► Retrying ──► Ready
```

See [`docs/DESIGN.md`](docs/DESIGN.md) for the full architecture, diagrams,
theory, and trade-off analysis.

---

## Project Structure

```
internal/task/      Task state machine + Task struct
internal/dag/       DAG (adjacency list) + Kahn's topological sort
internal/workflow/  Workflow = DAG + lifecycle status
internal/worker/    Fixed-size goroutine pool with backpressure
internal/scheduler/ Pluggable scheduling (FIFO, Priority, RR)  [M3]
internal/engine/    Orchestration + dispatch loop               [M4]
internal/store/     WAL + BoltDB persistence                    [M6]
cmd/chorna/         CLI entry point                             [M7]
docs/DESIGN.md      Full design document
```

---

## Running

**Prerequisites:** Go 1.22+

```bash
# Run all tests (including race detector)
go test -race ./...

# Run the Milestone 1 demo (diamond DAG, sequential execution)
go run ./cmd/chorna/
```

---

## Milestone Progress

| # | Milestone              | Status |
|---|------------------------|--------|
| 1 | DAG + State Machine    | ✅ Done |
| 2 | Worker Pool            | ✅ Done |
| 3 | Scheduler Abstraction  | ⬜      |
| 4 | Engine Dispatch Loop   | ⬜      |
| 5 | Retry + Backoff        | ⬜      |
| 6 | Persistence (WAL)      | ⬜      |
| 7 | CLI                    | ⬜      |
| 8 | Benchmarks             | ⬜      |

**72 tests, 0 data races.**

---

## Key Design Decisions

| Decision | Choice | Why |
|---|---|---|
| Shutdown signal | `stopCh` channel (not `close(taskCh)`) | Sending to a closed channel panics in Go, even inside `select` |
| Submit guard | Two-phase `stopCh` non-blocking check | Prevents non-deterministic result when `taskCh` has space and `stopCh` is closed simultaneously |
| Result buffer | `cap = queueDepth + nWorkers` | Prevents workers blocking on result send while Shutdown waits on `wg.Wait()` |
| Task state | `sync.Mutex` per task | Guards a single field; channels would add unnecessary indirection |
| Dispatch queue | Bounded `chan *task.Task` | Natural backpressure — producer blocks, not buffers |
| Execution guarantee | At-least-once | Exactly-once requires 2PC; `TaskFunc` must be idempotent |
| Topo sort | Kahn's BFS | Produces parallel layers directly; no recursion/stack overflow risk |

---

## Concurrency Guarantees

- **No data races:** verified with `go test -race` on every commit
- **No deadlock:** strict lock ordering (dag.mu > task.mu); channels never held with locks
- **No goroutine leaks:** every goroutine has a clear exit condition via `ctx.Done()` or channel close
- **Panic isolation:** worker goroutines recover from panicking `TaskFunc`s and continue
