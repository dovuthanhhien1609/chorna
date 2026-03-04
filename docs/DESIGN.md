# Chrona — Design Document

> A deterministic concurrent workflow execution engine built to deeply understand
> operating systems concepts, concurrency control, scheduling strategies,
> state machines, and fault tolerance.

---

## Table of Contents

1. [Project Goals](#1-project-goals)
2. [System Architecture](#2-system-architecture)
3. [Component Responsibilities](#3-component-responsibilities)
4. [Theory: Why a DAG?](#4-theory-why-a-dag)
5. [Topological Scheduling — Kahn's Algorithm](#5-topological-scheduling--kahns-algorithm)
6. [Task State Machine](#6-task-state-machine)
7. [Worker Pool & Backpressure](#7-worker-pool--backpressure)
8. [Concurrency Model](#8-concurrency-model)
9. [Scheduler Abstraction](#9-scheduler-abstraction)
10. [Persistence & Crash Recovery](#10-persistence--crash-recovery)
11. [Retry & Fault Tolerance](#11-retry--fault-tolerance)
12. [Go Runtime Considerations](#12-go-runtime-considerations)
13. [Design Trade-offs](#13-design-trade-offs)
14. [Failure Scenarios](#14-failure-scenarios)
15. [Benchmark Design](#15-benchmark-design)
16. [Milestone Roadmap](#16-milestone-roadmap)

---

## 1. Project Goals

Chrona executes workflows modeled as **Directed Acyclic Graphs (DAGs)** where
nodes are tasks and edges represent ordering constraints (dependency arrows).

```
"Task B cannot start until Task A finishes" → edge A → B
```

Core correctness requirements:

| Property         | Requirement                                              |
|------------------|----------------------------------------------------------|
| Safety           | No task executes before its dependencies complete        |
| Liveness         | Every task eventually executes (no starvation)           |
| At-least-once    | After a crash, no task is silently lost                  |
| No deadlock      | Goroutines never form a cycle of mutual waits            |
| No data race     | All shared state accessed under proper synchronization   |
| Graceful shutdown| In-flight tasks complete before the process exits        |

---

## 2. System Architecture

### 2.1 High-Level Component Diagram

```
  ┌──────────────────────────────────────────────────────────────────────┐
  │                         CLI  (Milestone 7)                          │
  │                  submit │ inspect │ cancel │ status                  │
  └──────────────────────────────┬───────────────────────────────────────┘
                                 │ *Workflow
  ┌──────────────────────────────▼───────────────────────────────────────┐
  │                      WorkflowEngine  (M4)                           │
  │                                                                      │
  │   ┌─────────────────── dispatch loop ──────────────────────────┐    │
  │   │                                                             │    │
  │   │  ReadyTasks()   Scheduler.Push()   pool.Submit()           │    │
  │   │  ─────────────► ─────────────────► ──────────────────────► │    │
  │   │                                                             │    │
  │   │  OnTaskCompleted() ◄── Transition() ◄── pool.Results()     │    │
  │   └─────────────────────────────────────────────────────────────┘    │
  │           │                    │                    │                 │
  └───────────┼────────────────────┼────────────────────┼─────────────────┘
              │                    │                    │
  ┌───────────▼────┐  ┌────────────▼──────┐  ┌─────────▼────────────────┐
  │  DAG Engine    │  │    Scheduler      │  │     Worker Pool          │
  │  (M1)          │  │    (M3)           │  │     (M2)                 │
  │                │  │                   │  │                          │
  │ · adjacency    │  │ · FIFO            │  │ · N goroutines           │
  │   list         │  │ · Priority heap   │  │ · bounded taskCh         │
  │ · in-degree    │  │ · Round-Robin     │  │ · backpressure           │
  │ · topo sort    │  │ · pluggable       │  │ · panic recovery         │
  └───────┬────────┘  └───────────────────┘  └──────────────────────────┘
          │
  ┌───────▼────────────────────────────────────────────────────────────┐
  │                  Task State Machine  (M1)                          │
  │    Pending → Ready → Running → Completed                           │
  │                            ↘ Failed → Retrying → Ready             │
  └────────────────────────────────────────────────────────────────────┘
          │
  ┌───────▼────────────────────────────────────────────────────────────┐
  │                  Persistence Layer  (M6)                           │
  │    Write-Ahead Log │ Snapshot │ Crash Recovery                     │
  └────────────────────────────────────────────────────────────────────┘
```

### 2.2 Package Structure

```
chrona/
├── cmd/chorna/          # CLI entry point
│   └── main.go
├── internal/
│   ├── task/            # Task struct + state machine
│   │   ├── state.go     # TaskState, valid transitions, ErrInvalidTransition
│   │   └── task.go      # Task, New, Transition, IncrRetry, Execute, Snapshot
│   ├── dag/             # Directed acyclic graph
│   │   ├── dag.go       # DAG, AddTask, AddEdge, InDegree, DecrInDegree
│   │   └── topo.go      # TopologicalSort (Kahn's), TopoResult, ErrCycle
│   ├── workflow/        # Workflow = DAG + lifecycle
│   │   └── workflow.go  # Workflow, ReadyTasks, OnTaskCompleted, Validate
│   ├── worker/          # Fixed-size goroutine pool
│   │   └── pool.go      # Pool, Submit, Results, Shutdown, TaskResult
│   ├── scheduler/       # Pluggable dispatch ordering  [M3]
│   │   ├── scheduler.go # Scheduler interface
│   │   ├── fifo.go
│   │   ├── priority.go
│   │   └── roundrobin.go
│   ├── engine/          # Orchestration + dispatch loop [M4]
│   │   └── engine.go
│   └── store/           # WAL + snapshot persistence   [M6]
│       └── boltdb.go
└── docs/
    └── DESIGN.md        # This document
```

### 2.3 Data Flow — Lifecycle of One Task

```
 Operator                Engine                   Worker Pool         TaskFunc
    │                       │                           │                 │
    │  Submit(workflow)      │                           │                 │
    │──────────────────────►│                           │                 │
    │                       │ Validate() + TopoSort()   │                 │
    │                       │───────────────────────────X                 │
    │                       │                           │                 │
    │                       │ ReadyTasks() → [taskA]    │                 │
    │                       │ Transition(Ready)          │                 │
    │                       │ scheduler.Push(taskA)      │                 │
    │                       │ pool.Submit(taskA)         │                 │
    │                       │──────────────────────────►│                 │
    │                       │                           │ Transition(Run) │
    │                       │                           │────────────────►│
    │                       │                           │ task.Execute()  │
    │                       │                           │────────────────►│
    │                       │                           │  ◄──────────────│
    │                       │                           │    (result/err) │
    │                       │  ◄── TaskResult ──────────│                 │
    │                       │ Transition(Completed)      │                 │
    │                       │ OnTaskCompleted(taskA)     │                 │
    │                       │   → unblocks [taskB,taskC] │                │
    │                       │ Transition(Ready) for B,C  │                │
    │                       │ ... repeat for B, C, D ... │                │
    │                       │                           │                 │
    │  workflow complete     │                           │                 │
    │◄──────────────────────│                           │                 │
```

---

## 3. Component Responsibilities

| Component       | Owns                              | Does NOT own                         |
|-----------------|-----------------------------------|--------------------------------------|
| DAG Engine      | Graph topology, in-degree counts  | Task state, execution                |
| Task            | State machine, retry count        | Graph structure, execution context   |
| Workflow        | DAG + workflow status             | Worker assignment, scheduling order  |
| Worker Pool     | Goroutines, taskCh, resultCh      | State transitions (except Running)   |
| Scheduler       | Dispatch ordering                 | Execution, graph, state              |
| Engine          | Orchestration, all transitions    | Everything it delegates              |
| Persistence     | Event log, snapshots              | In-memory state (source of truth)    |

---

## 4. Theory: Why a DAG?

### 4.1 Workflow Models Compared

| Model              | Example              | Supports cycles? | Guarantees termination? |
|--------------------|----------------------|------------------|-------------------------|
| Linear pipeline    | ETL: A→B→C           | No               | Yes                     |
| DAG (Chrona)       | Diamond: A→B,C→D     | No               | Yes                     |
| General graph      | State machine w/ loops| Yes             | Not guaranteed          |
| Petri net          | Concurrent tokens    | Yes              | Domain-dependent        |

A DAG is the right choice for workflow execution because:

1. **Termination is guaranteed.** A DAG has no cycles so every topological walk
   reaches the sink nodes in finite steps.

2. **Topological order is computable.** Given a DAG, we can enumerate tasks in
   an order that respects all dependencies. This is not possible for general graphs.

3. **Parallelism is explicit.** Nodes in the same topological layer have no
   inter-dependencies and can run concurrently.

4. **Acyclicity = no circular dependency.** A cycle would mean "A needs B
   needs A" which is logically impossible to satisfy; rejecting cycles at
   submission time (via topological sort) prevents this class of bug at the
   latest possible moment.

### 4.2 Graph Representation

Chrona stores the DAG as two maps:

```
dependents: map[TaskID][]TaskID   A → [B, C]  (successors — who to unblock)
inDegree:   map[TaskID]int        D → 2       (how many unresolved prerequisites)
```

Choosing **adjacency list over adjacency matrix**:

| Criterion         | Adjacency List | Adjacency Matrix |
|-------------------|---------------|-----------------|
| Space             | O(V + E)      | O(V²)           |
| Add edge          | O(1)          | O(1)            |
| Find successors   | O(degree)     | O(V)            |
| Typical workflows | Sparse (E ≈ V)| Sparse wastes   |

Real workflows are sparse (each task has a small number of direct dependencies),
making the adjacency list O(V + E) the clear winner.

---

## 5. Topological Scheduling — Kahn's Algorithm

### 5.1 Algorithm

Kahn's algorithm produces a topological order in **O(V + E)** using BFS:

```
KAHN(G):
  inDeg ← copy of in-degree map
  queue ← all nodes where inDeg[v] = 0     // seed: nodes with no prerequisites
  order ← []
  layers ← []

  while queue is not empty:
    layer ← snapshot of queue              // all nodes in queue are mutually independent
    layers.append(layer)

    next_queue ← []
    for each v in queue:
      order.append(v)
      for each successor u of v:
        inDeg[u] -= 1
        if inDeg[u] = 0:
          next_queue.append(u)

    queue ← next_queue

  if len(order) ≠ |V|:
    return CYCLE_DETECTED                  // not all nodes reachable → cycle exists
  return (order, layers)
```

### 5.2 Why Kahn's over DFS-based sort?

| Property              | Kahn's (BFS)                  | DFS post-order                 |
|-----------------------|-------------------------------|--------------------------------|
| Cycle detection       | Natural: unprocessed nodes    | Requires back-edge tracking    |
| Parallel layers       | Produced directly             | Requires second pass           |
| Memory                | O(V) queue                   | O(V) call stack (risk: overflow)|
| Determinism           | Queue order (can be sorted)   | DFS visit order                |

**Layers** are the key output for Chrona: all tasks in `layer[i]` may run
concurrently because none depends on another task in the same layer.

```
  Workflow DAG           Kahn's layers
  ─────────────          ─────────────────────────────────────────────
       [A]               Layer 0: [A]        ← no prerequisites
      /   \
    [B]   [C]            Layer 1: [B] [C]    ← both depend only on A
      \   /
       [D]               Layer 2: [D]        ← depends on B AND C
```

### 5.3 In-Degree Tracking During Execution

The engine does NOT re-run the topological sort during execution. Instead it
maintains the `inDegree` map incrementally:

```
When task T completes:
  for each successor S of T:
    inDegree[S] -= 1
    if inDegree[S] == 0:
      S is now eligible (add to scheduler)
```

This is O(out-degree of T) per completion — far cheaper than re-sorting.

---

## 6. Task State Machine

### 6.1 State Diagram

```
                  ┌───────────┐
       create     │  Pending  │
  ────────────────►           │
                  └─────┬─────┘
                        │ deps resolved + inDegree = 0
                        │ engine: Transition(StateReady)
                  ┌─────▼─────┐
            ┌────►│   Ready   │
            │     │           │
            │     └─────┬─────┘
            │           │ worker picks up task
            │           │ worker: Transition(StateRunning)
            │     ┌─────▼─────┐
            │     │  Running  │
            │     │           │
            │     └──┬────┬───┘
            │        │    │
            │ success│    │failure
            │        │    │
            │  ┌─────▼┐  ┌▼──────────┐
            │  │ Com- │  │  Failed   │
            │  │pleted│  │           │
            │  │(term)│  └─────┬─────┘
            │  └──────┘        │ retries remain?
            │                  │ engine: IncrRetry() → true
            │           ┌──────▼──────┐
            │           │  Retrying   │
            │           │ (sleeping)  │
            └───────────┤             │
        backoff elapsed  └─────────────┘
        engine: Transition(StateReady)
```

### 6.2 Valid Transitions Table

| From        | To          | Triggered by | Condition              |
|-------------|-------------|--------------|------------------------|
| Pending     | Ready       | Engine       | inDegree == 0          |
| Ready       | Running     | Worker       | Worker picks up task   |
| Running     | Completed   | Engine       | Execute returned nil   |
| Running     | Failed      | Engine       | Execute returned error |
| Failed      | Retrying    | Engine       | retryCount ≤ MaxRetries|
| Retrying    | Ready       | Engine       | backoff elapsed        |
| Completed   | —           | —            | Terminal state         |
| Failed (no retries) | —   | —            | Terminal state         |

### 6.3 Invariants

- At any moment, **exactly one goroutine** executes a task in `Running` state.
- `Completed` and terminal `Failed` are absorbing states; no further transitions.
- A task in `Retrying` is NOT in the scheduler queue; it re-enters via a
  goroutine sleeping through backoff.
- A task's state is **never read without the lock** (enforced by `State()` method).
- State transitions **always write the WAL event first**, then mutate in-memory
  state (prevents losing transitions across crashes).

---

## 7. Worker Pool & Backpressure

### 7.1 Internal Channel Topology

```
  Engine (one goroutine)
        │
        │  pool.Submit(ctx, task)           pool.Results()
        │                                         ▲
  ──────▼──────────────────────────────────────── │ ──────
  │     taskCh  (cap = queueDepth)                │      │
  │     ████████████████░░░░░░                    │      │
  │                                        resultCh       │
  │   ┌─────────┐  ┌─────────┐  ┌─────────┐(cap=Q+N)    │
  │   │Worker 0 │  │Worker 1 │  │Worker N │      │      │
  │   └────┬────┘  └────┬────┘  └────┬────┘      │      │
  │        │            │            │            │      │
  │        └────────────┴────────────┴────────────┘      │
  │                 sends TaskResult                      │
  │                                                       │
  │   stopCh ─── closed on Shutdown ────────────────────►│
  ─────────────────────────────────────────────────────────
```

### 7.2 Backpressure Mechanics

```
Producer (Engine)        Bounded Channel         Consumer (Workers)
      │                  ┌──────────────┐             │
      │─── Submit ──────►│ ████████░░░░ │────────────►│
      │                  │  full?       │             │
      │   blocks here    │              │             │
      │◄─────────────────│  yes: block  │             │
      │  until space     │              │             │
      │  appears         └──────────────┘             │
```

When `taskCh` is at capacity, `Submit` blocks in a `select`. This provides
**natural backpressure**: the engine stops advancing the dispatch loop rather
than accumulating an unbounded in-memory buffer.

The backpressure propagation chain:

```
Slow TaskFuncs
  → Workers busy
    → resultCh fills
      → Workers block on result send
        → taskCh not consumed
          → taskCh fills
            → Submit blocks
              → Engine stops calling ReadyTasks()
```

### 7.3 Why `stopCh` Instead of `close(taskCh)`

Go's specification: **sending to a closed channel always panics**, even inside
a `select` statement. The select only chooses among cases that can *proceed*;
a closed-channel send does not "not proceed" — it panics immediately.

```go
// WRONG: panics if taskCh is closed while this select is evaluated
select {
case taskCh <- t:   // panic if taskCh is closed
case <-ctx.Done():
}

// CORRECT: stopCh is closed (readable) instead of taskCh being closed
select {
case taskCh <- t:   // succeeds if there's space AND channel is open
case <-stopCh:      // chosen when shutdown has been signalled
case <-ctx.Done():  // chosen when caller cancels
}
```

Additionally, Submit uses a **two-phase check**:

```go
// Phase 1: non-blocking fast-path (avoids ambiguous multi-ready select)
select {
case <-p.stopCh:
    return ErrPoolStopped
default:
}

// Phase 2: blocking select
select {
case p.taskCh <- t:  return nil
case <-p.stopCh:     return ErrPoolStopped
case <-ctx.Done():   return ctx.Err()
}
```

Without phase 1: when `taskCh` has space AND `stopCh` is already closed,
Go picks randomly between the two ready cases — making `Submit` non-deterministically
return `nil` or `ErrPoolStopped`. The fast-path eliminates this ambiguity for
the sequential "Submit after Shutdown" case.

### 7.4 `resultCh` Buffer Sizing

```
resultCh capacity = queueDepth + nWorkers
```

Rationale: in the worst case, all `queueDepth` queued tasks complete
simultaneously *and* all `nWorkers` workers finish their current task at the
same moment. Without sufficient buffer, workers block on result delivery while
`Shutdown` blocks on `wg.Wait()` — a deadlock.

---

## 8. Concurrency Model

### 8.1 Lock Hierarchy

To prevent deadlock, locks must always be acquired in this order:

```
Priority (highest to lowest):
  1. dag.mu   (RWMutex)  — DAG graph structure
  2. task.mu  (Mutex)    — task state field

Rule: never hold dag.mu while acquiring task.mu across goroutine boundaries.

In practice:
  • dag.mu is always released before task.mu is acquired.
  • The engine's dispatch loop holds neither lock across function calls.
  • Workers acquire only task.mu (via Transition).
```

### 8.2 Who Owns What

| Shared State          | Protected by          | Accessed by                     |
|-----------------------|-----------------------|---------------------------------|
| `dag.tasks`           | `dag.mu` (RWMutex)    | Engine (write), workers (read)  |
| `dag.inDegree`        | `dag.mu` (RWMutex)    | Engine dispatch loop only*      |
| `task.state`          | `task.mu` (Mutex)     | Workers (Running), engine (all) |
| `task.retryCount`     | `task.mu` (Mutex)     | Engine only                     |
| `workflow.status`     | `workflow.mu` (Mutex) | Engine (write), CLI (read)      |
| `pool.taskCh`         | channel semantics     | Engine (send), workers (recv)   |
| `pool.resultCh`       | channel semantics     | Workers (send), engine (recv)   |
| `pool.stopCh`         | `pool.once`           | Shutdown (close), all (recv)    |

\* `inDegree` is modified only by the engine's dispatch loop (single goroutine);
it requires no separate lock for reads/writes within that goroutine.

### 8.3 Goroutine Lifecycle

```
Process lifetime:
─────────────────────────────────────────────────────────────────────────

main goroutine:
  └─► engine.Run(ctx)
        └─► dispatch loop (blocks on pool.Results())

Pool goroutines (N workers):
  └─► work(ctx)
        loop: select { taskCh / stopCh }
        exit: when stopCh fires and drain completes
        guarantee: p.wg.Done() called exactly once

resultCh closer goroutine:
  └─► p.wg.Wait() → close(resultCh)
      exit: after all workers call wg.Done()

Retry goroutines (one per retrying task):
  └─► select { time.After(backoff) / ctx.Done() }
      exit: backoff elapsed OR context cancelled
      guarantee: no leak because ctx.Done() is always checked

─────────────────────────────────────────────────────────────────────────
```

### 8.4 Deadlock Prevention

Deadlock requires a cycle of goroutines where each holds a resource that the
next is waiting for. Chrona avoids this through:

| Technique                    | What it prevents                                    |
|------------------------------|-----------------------------------------------------|
| Strict lock ordering         | Lock-order cycle (classic deadlock)                 |
| `select` with `ctx.Done()`   | Goroutine blocked forever on channel op             |
| Buffered `resultCh`          | Workers blocked on send while engine waits on wg    |
| `stopCh` pattern             | Workers blocked on taskCh receive after shutdown    |
| No lock held across channels | Lock + channel wait cycle                           |

### 8.5 Race Condition Analysis

| Race scenario                              | Prevention                              |
|--------------------------------------------|-----------------------------------------|
| Two goroutines read+write `task.state`     | `task.mu` serializes all state access   |
| Engine reads `inDegree` while worker writes| Engine is sole writer (single goroutine)|
| `close(taskCh)` races with `taskCh <- t`  | `stopCh` pattern: taskCh never closed   |
| Submit reads `stopped` before Shutdown sets| Fast-path non-blocking `stopCh` receive |
| Worker reads task fields during execution  | Fields set at construction, immutable   |
| Multiple workers write to `resultCh`       | Channel serializes concurrent sends     |

---

## 9. Scheduler Abstraction

### 9.1 Interface

```go
type Scheduler interface {
    Push(task *task.Task)       // add to the ready collection
    Pop() (*task.Task, bool)    // remove and return next task to dispatch
    Len() int                   // current queue depth
}
```

The Scheduler answers one question: *given a set of eligible tasks, which runs
next?* It has no knowledge of the DAG, workers, or state machine.

### 9.2 Strategy Comparison

```
FIFO (First-In, First-Out)
──────────────────────────
  Queue:  [t1, t2, t3, t4]
  Pop →   t1 (oldest first)

  Implementation: ring buffer or linked list
  Complexity: Push O(1), Pop O(1)
  Fairness: yes (submission order respected)
  Starvation: none (every task is eventually first)
  Best for: batch jobs, predictable ordering

Priority Queue
──────────────
  Heap:   [(priority=10, t3), (priority=5, t1), (priority=1, t4)]
  Pop →   t3 (highest priority first)

  Implementation: binary min/max heap
  Complexity: Push O(log n), Pop O(log n)
  Fairness: no (low-priority tasks may starve)
  Starvation: possible — mitigated by aging (priority += wait_time)
  Best for: latency-sensitive tasks with known importance

Round-Robin (workflow-level fairness)
──────────────────────────────────────
  Workflows: [wf-A, wf-B, wf-C]
  Round 1:  take one ready task from wf-A
  Round 2:  take one ready task from wf-B
  Round 3:  take one ready task from wf-C
  Round 4:  back to wf-A

  Implementation: circular iterator over per-workflow FIFOs
  Complexity: Push O(1), Pop O(W) where W = active workflows
  Fairness: yes (at workflow level — no single workflow monopolises)
  Starvation: none (each workflow gets equal time slices)
  Best for: multi-tenant execution with SLA guarantees
```

### 9.3 Starvation Prevention (Priority Scheduler)

Aging increases a waiting task's effective priority over time:

```
effectivePriority(task) = task.Priority + α × waitTime
```

Once `effectivePriority` exceeds the current top of the heap, the waiting task
overtakes higher-priority newcomers. The constant α controls the trade-off
between priority ordering and starvation prevention.

---

## 10. Persistence & Crash Recovery

### 10.1 Write-Ahead Log (WAL)

Every state transition is durable before it takes effect in memory:

```
  Time ──────────────────────────────────────────────────────────►

  ① Append event to WAL (disk)       } atomic and durable
  ② Mutate in-memory state            }

  On crash between ① and ②:  event is in WAL → replay restores state ✓
  On crash before ①:          nothing recorded → task is re-run  ✓
  On crash after ②:           idempotent — re-running from WAL is safe ✓
```

### 10.2 WAL Event Schema

```
Event {
  Seq        uint64     // monotonically increasing, used for replay ordering
  Timestamp  time.Time
  WorkflowID string
  TaskID     string
  Transition TaskState  // the NEW state
  Metadata   map[string]string  // e.g. retryCount, errorMessage
}
```

### 10.3 Snapshot + Compaction

Replaying the full WAL from the beginning grows linearly with history.
Periodic snapshots bound recovery time:

```
Recovery time without snapshot:  O(total events)
Recovery time with snapshot:     O(events since last snapshot)

Strategy:
  • Take snapshot every N events (or every K minutes)
  • Snapshot = full serialisation of all workflow + task states
  • On startup: load latest snapshot, then replay WAL from snapshot.Seq onward
  • Compact: delete WAL entries older than snapshot.Seq
```

### 10.4 Crash Recovery Algorithm

```
RECOVER():
  snapshot ← LoadLatestSnapshot()
  if snapshot exists:
    restore in-memory state from snapshot
    events ← LoadWAL(afterSeq = snapshot.Seq)
  else:
    events ← LoadWAL(afterSeq = 0)

  for each event in events (ordered by Seq):
    apply event.Transition to task[event.TaskID]

  for each task in Running state:
    // was executing at crash time — outcome unknown
    // at-least-once: re-queue as Ready (task.Fn must be idempotent)
    task.state = StateReady
    scheduler.Push(task)

  for each task in Retrying state:
    // was sleeping through backoff — restart the backoff goroutine
    scheduler.PushWithBackoff(task)
```

### 10.5 At-Least-Once vs Exactly-Once

| Guarantee      | What it means                              | How to achieve                   | Cost        |
|----------------|--------------------------------------------|----------------------------------|-------------|
| At-least-once  | Task may run multiple times after crash    | Re-run all Running tasks on boot | Low         |
| Exactly-once   | Task runs exactly once, even after crash   | Two-phase commit + idempotency key | Very high |

**Chrona v1 guarantees at-least-once.** This is documented as a contract:
`TaskFunc` implementations **must be idempotent** — running them twice with
the same input must have the same observable effect as running them once.

---

## 11. Retry & Fault Tolerance

### 11.1 Exponential Backoff

```
attempt 0:  wait = base
attempt 1:  wait = base × 2¹
attempt 2:  wait = base × 2²
attempt n:  wait = min(base × 2ⁿ, maxWait)
```

Adding **jitter** prevents thundering herd (many tasks retrying simultaneously):

```
wait = min(base × 2ⁿ, maxWait) × (0.5 + random(0, 0.5))
```

### 11.2 Retry Goroutine Lifecycle

```go
// Engine spawns this when a task transitions to Retrying.
go func() {
    backoff := computeBackoff(task.RetryCount())
    select {
    case <-time.After(backoff):   // normal path: re-queue after backoff
        task.Transition(StateReady)
        scheduler.Push(task)
    case <-ctx.Done():            // shutdown path: abandon retry, no leak
        // task remains in Retrying state; WAL records this for recovery
    }
}()
```

No goroutine leak: every retry goroutine exits via one of the two select cases.

### 11.3 Panic Isolation

```
  Worker goroutine
  ┌──────────────────────────────────────────────────────────┐
  │  defer func() {                                          │
  │      if r := recover(); r != nil {                       │
  │          result.Err = fmt.Errorf("panic: %v\n%s", r, stack) │
  │      }                                                   │
  │  }()                                                     │
  │  return task.Execute(ctx)                                │
  └──────────────────────────────────────────────────────────┘
          │
          ▼
  TaskResult{Err: "panic: ..."} sent to engine
  Engine: treats as execution failure, applies retry logic
  Worker: continues to next task (did not crash)
```

---

## 12. Go Runtime Considerations

### 12.1 GOMAXPROCS and True Parallelism

```
GOMAXPROCS = N  →  up to N OS threads run goroutines simultaneously

Worker pool sizing:
  CPU-bound tasks:   nWorkers ≈ GOMAXPROCS  (adding more causes contention)
  I/O-bound tasks:   nWorkers >> GOMAXPROCS (goroutines block on I/O, yield OS thread)
  Mixed:             profile first; start with 2×GOMAXPROCS

Default: runtime.GOMAXPROCS(0) = number of CPU cores
```

### 12.2 Goroutine Scheduling

Go uses an M:N scheduler (M goroutines on N OS threads via P processors):

```
  Goroutine states:
  ┌─────────────┐       ┌──────────────┐       ┌──────────────┐
  │  Runnable   │──────►│   Running    │──────►│   Blocked    │
  │ (on run q)  │       │ (on P)       │       │ (chan/mutex)  │
  └─────────────┘       └──────────────┘       └──────────────┘
                              │   ▲                     │
                              │   │ preemption          │ wakeup
                              └───┘ (10ms or syscall)   └────────
```

**Blocking syscalls** (disk I/O, network): the Go runtime detaches the goroutine
from its P and moves it to a background thread — other goroutines on that P
continue uninterrupted. This is why I/O-bound worker pools can safely exceed
GOMAXPROCS without starving CPU-bound work.

### 12.3 Memory Model — Happens-Before

The Go memory model guarantees that a send on a channel *happens-before* the
corresponding receive completes. Chrona relies on this for correctness:

```
Worker sends TaskResult to resultCh
  → engine receives TaskResult from resultCh
  → engine sees all task state mutations made before the send ✓
```

Channel operations provide the happens-before edges that make lock-free
coordination between producer and consumer safe.

---

## 13. Design Trade-offs

### 13.1 Mutex vs Channel

| Decision point                     | Choice      | Reason                                          |
|------------------------------------|-------------|-------------------------------------------------|
| Task state field                   | Mutex       | Guards a single integer; no ownership transfer  |
| Dispatch queue (engine → workers)  | Channel     | Bounded buffer + backpressure are built-in      |
| Results (workers → engine)         | Channel     | Producer-consumer with natural flow control     |
| Shutdown signal (Shutdown → all)   | Channel     | Closed channel broadcasts to N goroutines at once|
| Workflow-level status              | Mutex       | Simple field, no transfer                       |

Rule of thumb: **channels for ownership transfer and synchronisation signals;
mutexes for protecting a shared memory location accessed from multiple sites.**

### 13.2 Bounded vs Unbounded Queue

| Property              | Bounded (Chrona)        | Unbounded                         |
|-----------------------|-------------------------|-----------------------------------|
| Memory usage          | O(queueDepth)           | O(submitted tasks) — can OOM      |
| Backpressure          | Automatic (Submit blocks)| None (caller must throttle)       |
| Latency under load    | Stable (producer slows) | Spiky (tasks pile up)             |
| Throughput            | Lower peak              | Higher peak, unstable under load  |

Bounded queues implement **Little's Law** control: throughput = λ (arrival rate)
× L (queue depth). Capping L bounds throughput and enforces predictable latency.

### 13.3 In-Memory vs Persistent State

```
Hot path (all in memory):
  ReadyTasks() + Submit()   → microseconds
  No fsync required

Persistence (WAL append):
  Event append + fsync      → ~1–10 ms per event
  Amortised with batching   → ~0.1 ms per event

Trade-off:
  • Fsync on every transition: durable but adds ~10ms per task
  • Async WAL: fast but small window of lost events on crash
  • Batch WAL writes: middle ground (configurable)
```

### 13.4 FIFO vs Priority vs Round-Robin

| Scheduler    | Throughput | Latency Fairness | Starvation Risk | Best use case          |
|--------------|------------|------------------|-----------------|------------------------|
| FIFO         | High       | Equal            | None            | Homogeneous batch jobs |
| Priority     | High       | Biased           | Yes (low tasks) | SLA-tiered workloads   |
| Round-Robin  | Medium     | Equal/workflow   | None            | Multi-tenant systems   |

---

## 14. Failure Scenarios

| Scenario                | Detection                    | Recovery                                   |
|-------------------------|------------------------------|--------------------------------------------|
| Task `fn` returns error | `TaskResult.Err != nil`      | Retry with backoff (if retries remain)     |
| Task `fn` panics        | `recover()` in worker        | Same as error; panic string in Err         |
| Task times out          | `context.WithTimeout`        | Context cancel propagates to `fn`          |
| Worker goroutine panic  | `defer recover()` in worker  | Worker survives; result sent with Err      |
| Engine process crash    | WAL replay on next start     | Re-run `Running` tasks (at-least-once)     |
| Cycle in workflow DAG   | `TopologicalSort` at submit  | Reject workflow with `ErrCycle`            |
| Dependency never done   | Workflow-level deadline ctx  | Cancel remaining tasks                     |
| Disk full (WAL write)   | `AppendEvent` returns error  | Engine halts; operator must intervene      |
| resultCh full           | worker blocks on send        | Buffer sized to prevent this; see §7.4     |

---

## 15. Benchmark Design

### 15.1 Throughput vs Worker Count

```
Hypothesis: throughput scales linearly up to GOMAXPROCS, then plateaus
            (CPU-bound tasks) or continues to scale (I/O-bound tasks).

Experiment:
  • Fixed workflow: 1000 independent tasks (no dependencies)
  • TaskFunc: sleep(1ms) to simulate I/O; or busy-spin to simulate CPU
  • Variable: nWorkers ∈ {1, 2, 4, 8, 16, 32, 64}
  • Metric: tasks/second = 1000 / wall-clock time

Expected result:
  CPU-bound:  ──────╮_______________  (plateau at GOMAXPROCS)
  I/O-bound:  ─────────────────╮___  (plateau at ~1000/taskDuration)
```

### 15.2 Latency Under Backpressure

```
Hypothesis: bounded queue causes submit latency to spike when workers
            are slower than the submission rate.

Experiment:
  • Submit rate: 1000 tasks/second
  • Task duration: variable (10ms, 50ms, 100ms)
  • Queue depth: 16, fixed workers: 8
  • Metric: P50, P95, P99 submit latency

Expected result:
  taskDuration × workers < 1/submitRate  → no backpressure, low latency
  taskDuration × workers > 1/submitRate  → backpressure active, latency ≈ taskDuration
```

### 15.3 Scheduler Fairness

```
Hypothesis: Priority scheduler starves low-priority tasks under load;
            Round-Robin does not.

Experiment:
  • 2 workflows: wf-high (priority=10) and wf-low (priority=1)
  • 100 tasks each, submitted simultaneously
  • Metric: completion time per workflow

Expected result:
  FIFO:       wf-high ≈ wf-low (interleaved by submission order)
  Priority:   wf-high completes first; wf-low delayed by wf-high
  RoundRobin: wf-high ≈ wf-low (interleaved by workflow)
```

### 15.4 WAL Write Overhead

```
Hypothesis: fsync per event dominates over computation cost.

Experiment:
  • 1000-task workflow, no retries
  • Variable: no WAL / async WAL / sync WAL / batch WAL (N=10)
  • Metric: total workflow wall-clock time

Expected result:
  no WAL:    fastest (baseline)
  async WAL: ≈ baseline (fsync off critical path)
  sync WAL:  +10ms × 1000 = +10s overhead per 1000 tasks
  batch WAL: +10ms × 100  = +1s overhead (amortised over 10 events)
```

---

## 16. Milestone Roadmap

| # | Milestone             | Status | Key Deliverables                                  |
|---|-----------------------|--------|---------------------------------------------------|
| 1 | DAG + State Machine   | ✅ Done | Task FSM, topological sort, workflow validation   |
| 2 | Worker Pool           | ✅ Done | Bounded pool, backpressure, panic recovery        |
| 3 | Scheduler Abstraction | ⬜ Next | FIFO, Priority, Round-Robin, pluggable interface  |
| 4 | Engine Dispatch Loop  | ⬜      | Full end-to-end: submit workflow → results        |
| 5 | Retry + Backoff       | ⬜      | Exponential backoff with jitter, goroutine-safe   |
| 6 | Persistence           | ⬜      | BoltDB WAL, snapshot, crash recovery              |
| 7 | CLI                   | ⬜      | `submit`, `inspect`, `cancel` commands            |
| 8 | Benchmarks            | ⬜      | pprof, throughput/latency experiments             |

### Test Coverage per Milestone

| Milestone | Unit tests | Edge cases                                      |
|-----------|------------|-------------------------------------------------|
| M1 DAG    | 23 tests   | Cycle, self-loop, diamond, topo idempotency     |
| M1 Task   | 18 tests   | All transitions, terminal states, concurrent read|
| M1 Wflow  | 17 tests   | Full linear walk-through, diamond convergence   |
| M2 Pool   | 14 tests   | Backpressure, drain, panic, stopCh flakiness    |

**Total: 72 tests, 0 data races (verified with `go test -race`).**
