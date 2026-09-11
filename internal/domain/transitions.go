package domain

import (
	"fmt"

	"github.com/voocel/ainovel-cli/internal/errs"
)

// State transition rules (minimal version).
//
// Phase represents a major stage and follows a "forward only, never back" constraint:
//
//	init -> premise -> outline -> writing -> complete
//	  \---------> outline ------^
//	  \-----------------> writing
//
// Flow represents the currently active flow; it may switch within the writing period, but
// clearly aberrant jumps are not allowed:
//
//	writing   -> reviewing / rewriting / polishing / steering / writing
//	reviewing -> writing / rewriting / polishing / steering / reviewing
//	rewriting -> writing / steering / rewriting
//	polishing -> writing / steering / polishing
//	steering  -> writing / reviewing / rewriting / polishing / steering
//
// The empty state (zero value) counts as "not initialised" and may transition to any valid
// non-empty state.

var phaseOrder = map[Phase]int{
	PhaseInit:     1,
	PhasePremise:  2,
	PhaseOutline:  3,
	PhaseWriting:  4,
	PhaseComplete: 5,
}

// CanTransitionPhase reports whether a Phase transition is allowed.
// The rule stays simple: same-state transitions and forward moves are allowed, backward moves
// are not.
func CanTransitionPhase(from, to Phase) bool {
	if to == "" {
		return false
	}
	if from == "" || from == to {
		return true
	}
	fromOrder, fromOK := phaseOrder[from]
	toOrder, toOK := phaseOrder[to]
	if !fromOK || !toOK {
		return false
	}
	return toOrder >= fromOrder
}

// ValidatePhaseTransition validates whether a Phase transition is legal.
func ValidatePhaseTransition(from, to Phase) error {
	if CanTransitionPhase(from, to) {
		return nil
	}
	return fmt.Errorf("invalid phase transition: %q -> %q: %w", from, to, errs.ErrPhaseTransition)
}

// CanTransitionFlow reports whether a FlowState transition is allowed.
func CanTransitionFlow(from, to FlowState) bool {
	if to == "" {
		return false
	}
	if from == "" || from == to {
		return true
	}

	switch from {
	case FlowWriting:
		return to == FlowReviewing || to == FlowRewriting || to == FlowPolishing || to == FlowSteering
	case FlowReviewing:
		return to == FlowWriting || to == FlowRewriting || to == FlowPolishing || to == FlowSteering
	case FlowRewriting:
		return to == FlowWriting || to == FlowSteering
	case FlowPolishing:
		return to == FlowWriting || to == FlowSteering
	case FlowSteering:
		return to == FlowWriting || to == FlowReviewing || to == FlowRewriting || to == FlowPolishing
	default:
		return false
	}
}

// ValidateFlowTransition validates whether a FlowState transition is legal.
func ValidateFlowTransition(from, to FlowState) error {
	if CanTransitionFlow(from, to) {
		return nil
	}
	return fmt.Errorf("invalid flow transition: %q -> %q: %w", from, to, errs.ErrFlowTransition)
}
