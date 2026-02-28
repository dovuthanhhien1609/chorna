// Chrona — deterministic concurrent workflow execution engine.
//
// Milestone 1 demo: builds a diamond-shaped workflow, validates it, prints
// the execution plan, and simulates sequential execution to verify that the
// dependency logic drives the correct dispatch order.
package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/hien/chorna/internal/task"
	"github.com/hien/chorna/internal/workflow"
)

func main() {
	wf := buildDiamondWorkflow()

	if err := wf.Validate(); err != nil {
		log.Fatalf("validation failed: %v", err)
	}

	printExecutionPlan(wf)
	simulateExecution(wf)
}

// buildDiamondWorkflow creates:
//
//	        [ingest]
//	       /        \
//	[transform-a]  [transform-b]
//	       \        /
//	        [report]
func buildDiamondWorkflow() *workflow.Workflow {
	wf := workflow.New("pipeline-001", "ETL pipeline with parallel transforms")

	tasks := []struct {
		id       string
		priority int
		dur      time.Duration
	}{
		{"ingest", 10, 50 * time.Millisecond},
		{"transform-a", 5, 80 * time.Millisecond},
		{"transform-b", 5, 60 * time.Millisecond},
		{"report", 10, 30 * time.Millisecond},
	}

	for _, spec := range tasks {
		spec := spec // capture
		tk := task.New(spec.id, wf.ID, func(ctx context.Context) error {
			fmt.Printf("  ▶ executing [%s]...\n", spec.id)
			select {
			case <-time.After(spec.dur):
				fmt.Printf("  ✓ [%s] done (%s)\n", spec.id, spec.dur)
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}, spec.priority, 2)

		if err := wf.AddTask(tk); err != nil {
			log.Fatalf("AddTask(%q): %v", spec.id, err)
		}
	}

	deps := [][2]string{
		{"ingest", "transform-a"},
		{"ingest", "transform-b"},
		{"transform-a", "report"},
		{"transform-b", "report"},
	}
	for _, d := range deps {
		if err := wf.AddDependency(d[0], d[1]); err != nil {
			log.Fatalf("AddDependency(%q → %q): %v", d[0], d[1], err)
		}
	}

	return wf
}

func printExecutionPlan(wf *workflow.Workflow) {
	plan, err := wf.ExecutionPlan()
	if err != nil {
		log.Fatalf("ExecutionPlan: %v", err)
	}

	fmt.Println("══════════════════════════════════════")
	fmt.Printf(" Workflow: %s\n", wf.ID)
	fmt.Printf(" %s\n", wf.Description)
	fmt.Println("══════════════════════════════════════")
	fmt.Println(" Execution plan (parallel layers):")
	for i, layer := range plan.Layers {
		fmt.Printf("   Layer %d: [%s]\n", i, strings.Join(layer, ", "))
	}
	fmt.Printf(" Sequential order: %s\n", strings.Join(plan.Order, " → "))
	fmt.Println("══════════════════════════════════════")
}

// simulateExecution walks the workflow forward in a single-threaded loop,
// mimicking what the engine's dispatch loop will do in Milestone 4.
// This demonstrates that the dependency tracking and state machine work
// correctly without any concurrency yet.
func simulateExecution(wf *workflow.Workflow) {
	ctx := context.Background()
	wf.SetStatus(workflow.StatusRunning)

	fmt.Println("\n Simulating sequential execution:")

	completed := 0
	total := wf.DAG.Size()

	for completed < total {
		ready := wf.ReadyTasks()
		if len(ready) == 0 {
			// Should never happen in a valid workflow with tasks remaining.
			log.Fatal("deadlock: no ready tasks but workflow not finished")
		}

		for _, t := range ready {
			// State machine: Pending → Ready → Running
			if err := t.Transition(task.StateReady); err != nil {
				log.Fatalf("Transition(Ready) on %q: %v", t.ID, err)
			}
			if err := t.Transition(task.StateRunning); err != nil {
				log.Fatalf("Transition(Running) on %q: %v", t.ID, err)
			}

			// Execute synchronously (single-threaded in Milestone 1).
			execErr := t.Execute(ctx)

			if execErr != nil {
				_ = t.Transition(task.StateFailed)
				wf.SetStatus(workflow.StatusFailed)
				log.Fatalf("task %q failed: %v", t.ID, execErr)
			}

			// State machine: Running → Completed
			if err := t.Transition(task.StateCompleted); err != nil {
				log.Fatalf("Transition(Completed) on %q: %v", t.ID, err)
			}

			// Notify the workflow so it can decrement dependents' in-degrees.
			if _, err := wf.OnTaskCompleted(t.ID); err != nil {
				log.Fatalf("OnTaskCompleted(%q): %v", t.ID, err)
			}
			completed++
		}
	}

	wf.SetStatus(workflow.StatusCompleted)

	snap := wf.Snapshot()
	fmt.Println("\n══════════════════════════════════════")
	fmt.Printf(" Workflow %s: %s\n", snap.ID, snap.Status)
	fmt.Printf(" Duration: %s\n", snap.FinishedAt.Sub(snap.StartedAt).Round(time.Millisecond))
	fmt.Println("══════════════════════════════════════")
}
