package task

import "fmt"

// TaskState represents a point in the task lifecycle.
// Only the transitions listed in validTransitions are legal.
type TaskState int

const (
	StatePending   TaskState = iota // created, waiting for dependencies
	StateReady                      // dependencies resolved, eligible for dispatch
	StateRunning                    // assigned to a worker, executing
	StateCompleted                  // finished successfully (terminal)
	StateFailed                     // execution failed (terminal unless retries remain)
	StateRetrying                   // sleeping through backoff before re-queuing
)

func (s TaskState) String() string {
	switch s {
	case StatePending:
		return "Pending"
	case StateReady:
		return "Ready"
	case StateRunning:
		return "Running"
	case StateCompleted:
		return "Completed"
	case StateFailed:
		return "Failed"
	case StateRetrying:
		return "Retrying"
	default:
		return fmt.Sprintf("Unknown(%d)", int(s))
	}
}

// validTransitions encodes the state machine graph.
// A state not present as a key has no valid outgoing transitions (terminal).
//
//	Pending → Ready → Running → Completed
//	                         ↘ Failed → Retrying → Ready
var validTransitions = map[TaskState][]TaskState{
	StatePending:  {StateReady},
	StateReady:    {StateRunning},
	StateRunning:  {StateCompleted, StateFailed},
	StateFailed:   {StateRetrying},
	StateRetrying: {StateReady},
	// StateCompleted is intentionally absent — it is terminal.
}

// IsValidTransition reports whether the from→to transition is legal.
func IsValidTransition(from, to TaskState) bool {
	for _, allowed := range validTransitions[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

// ErrInvalidTransition is returned when a state transition violates the machine.
type ErrInvalidTransition struct {
	From TaskState
	To   TaskState
}

func (e *ErrInvalidTransition) Error() string {
	return fmt.Sprintf("invalid state transition: %s → %s", e.From, e.To)
}
