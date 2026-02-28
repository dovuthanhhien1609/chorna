package dag

// TopoResult holds the output of a topological sort.
type TopoResult struct {
	// Order is one valid sequential execution order for all tasks.
	Order []string

	// Layers groups tasks by dependency depth.  All tasks in layer[i] can run
	// concurrently because none depends on another task in the same layer.
	// This is the foundation for the parallel dispatch algorithm.
	//
	//   Layer 0: tasks with no prerequisites (in-degree 0)
	//   Layer 1: tasks whose only prerequisites are in Layer 0
	//   ...
	Layers [][]string
}

// TopologicalSort validates that the DAG is acyclic and returns a valid
// execution order together with parallel execution layers.
//
// Algorithm: Kahn's BFS — O(V + E).
//   1. Seed the queue with every node whose in-degree is 0.
//   2. Pop a node, record it, decrement successors' in-degrees.
//   3. When a successor reaches in-degree 0, enqueue it.
//   4. If the number of processed nodes < total nodes, a cycle exists.
//
// The method acquires a read lock so it is safe to call concurrently with
// read operations, but not with AddTask/AddEdge.
func (d *DAG) TopologicalSort() (*TopoResult, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	// Work on local copies so we don't mutate the live graph.
	inDeg := make(map[string]int, len(d.inDegree))
	for id, deg := range d.inDegree {
		inDeg[id] = deg
	}

	// Seed: every task with no prerequisites is immediately eligible.
	queue := collectZeroDegree(inDeg)

	var (
		order     []string
		layers    [][]string
		processed int
	)

	for len(queue) > 0 {
		// Snapshot the current queue as one parallel layer.
		layer := make([]string, len(queue))
		copy(layer, queue)
		layers = append(layers, layer)

		// Process each node in the current layer.
		next := []string{}
		for _, id := range queue {
			order = append(order, id)
			processed++

			for _, dep := range d.dependents[id] {
				inDeg[dep]--
				if inDeg[dep] == 0 {
					next = append(next, dep)
				}
			}
		}
		queue = next
	}

	if processed != len(d.tasks) {
		return nil, &ErrCycle{}
	}

	return &TopoResult{Order: order, Layers: layers}, nil
}

// collectZeroDegree returns all node IDs whose in-degree is currently 0.
// The returned slice has no guaranteed order; callers that need determinism
// must sort it themselves.
func collectZeroDegree(inDeg map[string]int) []string {
	out := make([]string, 0, len(inDeg))
	for id, deg := range inDeg {
		if deg == 0 {
			out = append(out, id)
		}
	}
	return out
}

// ErrCycle is returned when TopologicalSort detects a cycle in the graph.
// A cycle means at least one task (directly or indirectly) depends on itself,
// making it impossible to construct a valid execution order.
type ErrCycle struct{}

func (e *ErrCycle) Error() string {
	return "dag: cycle detected — workflow graph is not a DAG"
}
