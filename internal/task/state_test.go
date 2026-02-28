package task

import (
	"strings"
	"testing"
)

// allStates lists every state so tests stay exhaustive as the machine grows.
var allStates = []TaskState{
	StatePending, StateReady, StateRunning, StateCompleted, StateFailed, StateRetrying,
}

func TestIsValidTransition_AllLegalEdges(t *testing.T) {
	legal := []struct{ from, to TaskState }{
		{StatePending, StateReady},
		{StateReady, StateRunning},
		{StateRunning, StateCompleted},
		{StateRunning, StateFailed},
		{StateFailed, StateRetrying},
		{StateRetrying, StateReady},
	}
	for _, tc := range legal {
		if !IsValidTransition(tc.from, tc.to) {
			t.Errorf("expected %s → %s to be valid", tc.from, tc.to)
		}
	}
}

func TestIsValidTransition_IllegalEdges(t *testing.T) {
	// Build a set of legal edges for exclusion.
	legal := map[[2]TaskState]bool{}
	for from, tos := range validTransitions {
		for _, to := range tos {
			legal[[2]TaskState{from, to}] = true
		}
	}

	for _, from := range allStates {
		for _, to := range allStates {
			if legal[[2]TaskState{from, to}] {
				continue
			}
			if IsValidTransition(from, to) {
				t.Errorf("expected %s → %s to be invalid", from, to)
			}
		}
	}
}

func TestIsValidTransition_CompletedIsTerminal(t *testing.T) {
	for _, to := range allStates {
		if IsValidTransition(StateCompleted, to) {
			t.Errorf("Completed should be terminal, but %s → %s was valid", StateCompleted, to)
		}
	}
}

func TestTaskState_String(t *testing.T) {
	cases := map[TaskState]string{
		StatePending:   "Pending",
		StateReady:     "Ready",
		StateRunning:   "Running",
		StateCompleted: "Completed",
		StateFailed:    "Failed",
		StateRetrying:  "Retrying",
	}
	for state, want := range cases {
		if got := state.String(); got != want {
			t.Errorf("TaskState(%d).String() = %q, want %q", int(state), got, want)
		}
	}
}

func TestTaskState_String_Unknown(t *testing.T) {
	s := TaskState(999).String()
	if !strings.HasPrefix(s, "Unknown") {
		t.Errorf("unknown state String() = %q, want prefix \"Unknown\"", s)
	}
}

func TestErrInvalidTransition_Error(t *testing.T) {
	err := &ErrInvalidTransition{From: StatePending, To: StateRunning}
	msg := err.Error()
	if !strings.Contains(msg, "Pending") || !strings.Contains(msg, "Running") {
		t.Errorf("ErrInvalidTransition.Error() = %q, expected to contain state names", msg)
	}
}
