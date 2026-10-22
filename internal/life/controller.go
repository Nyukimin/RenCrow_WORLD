package life

import (
	"errors"
	"fmt"
)

// This file is the R10 controller logic that executes the decisions this
// package produces (D-11/D-12). It mutates only the logical ledger below:
// activity, logical occupancy name, posture, and the receipt log. Position is
// recorded for audit but never changed here — movement, measured geometry,
// and spatial readiness stay D-17/R10-runtime work, and occupancy release is
// a separate capability that only runs after the stop action executed.
// Nothing here writes WORLD State, creates Events, or touches CORE work.

// Ledger is the logical execution ledger of one Actor. Names are logical
// resource and place labels (D-18 provisional interface); there are no
// coordinates, room graphs, or movement operations.
type Ledger struct {
	Position  string
	Activity  string
	Occupancy string
	State     string
	// Actions is the append-only receipt log of executed actions.
	Actions []string
	// stopped marks that the stop action for the last running activity has
	// executed, which is the only thing that authorizes ReleaseOccupancy.
	stopped bool
}

var (
	// ErrDecisionNotExecutable reports a decision the controller must not
	// execute: refused, posture-less, or otherwise not a complete owner
	// decision. It mutates nothing.
	ErrDecisionNotExecutable = errors.New("life controller refuses a decision that is not executable")
	// ErrReleaseWithoutStop reports an occupancy release attempted before the
	// stop action for the activity that held it executed (D-11).
	ErrReleaseWithoutStop = errors.New("life controller releases occupancy only after the stop action executed")
	// ErrCandidateNotAdmitted reports an attempt to start work the admission
	// decision did not admit. The controller never starts refused work.
	ErrCandidateNotAdmitted = errors.New("life controller starts only admitted work")
	// ErrBusyLedger reports a start attempt while an activity is running;
	// one Actor runs one activity at a time.
	ErrBusyLedger = errors.New("life controller starts no new activity while one is running")
	// ErrMovementNotImplemented reports a candidate whose start would move the
	// Actor away. The controller performs no movement (D-17) and fails closed.
	ErrMovementNotImplemented = errors.New("life controller performs no movement")
	// ErrOccupancyConflict reports a start whose occupancy name differs from
	// a name still held after a previous stop. Releasing held occupancy is a
	// separate capability, never an implicit overwrite.
	ErrOccupancyConflict = errors.New("life controller refuses to overwrite held occupancy")
)

// ExecuteContinuation applies one continuation decision to the ledger.
// Continue and Hold change nothing except the receipt. A stop executes only
// the owner-authorized actions and the posture from the activity's own
// interruption rule; it never releases occupancy and never changes Position.
func ExecuteContinuation(ledger *Ledger, decision Continuation) error {
	if ledger == nil {
		return ErrDecisionNotExecutable
	}
	switch decision.Action {
	case ContinuationContinue, ContinuationHold:
		ledger.Actions = append(ledger.Actions, string(decision.Action))
		return nil
	case ContinuationStop:
		if !decision.Stop.Allowed || decision.Stop.StopState == "" || len(decision.Stop.Actions) == 0 || ledger.Activity == "" {
			return fmt.Errorf("%w: stop=%+v running=%q", ErrDecisionNotExecutable, decision.Stop, ledger.Activity)
		}
		ledger.Actions = append(ledger.Actions, decision.Stop.Actions...)
		ledger.Activity = ""
		ledger.State = decision.Stop.StopState
		ledger.stopped = true
		return nil
	default:
		return fmt.Errorf("%w: unknown action %q", ErrDecisionNotExecutable, decision.Action)
	}
}

// ReleaseOccupancy is the separate capability that releases the logical
// occupancy name still held after a stop. It refuses while an activity runs
// or when nothing was stopped, so a stop never drops a reservation up front.
func ReleaseOccupancy(ledger *Ledger) error {
	if ledger == nil || !ledger.stopped || ledger.Occupancy == "" {
		return ErrReleaseWithoutStop
	}
	ledger.Actions = append(ledger.Actions, "release:"+ledger.Occupancy)
	ledger.Occupancy = ""
	ledger.stopped = false
	return nil
}

// ExecuteAdmission starts exactly one activity the admission decision
// admitted for this ledger. It refuses refused or mismatched candidates, a
// running ledger, movement-requiring starts (D-17), and an occupancy name
// that differs from one still held; every refusal mutates nothing.
func ExecuteAdmission(ledger *Ledger, candidate Candidate, activity RunningActivity) error {
	if ledger == nil {
		return ErrCandidateNotAdmitted
	}
	if !candidate.Allowed || candidate.Reason != reasonAdmitted || candidate.ActivityID == "" || candidate.ActivityID != activity.ID {
		return fmt.Errorf("%w: candidate=%+v requested=%q", ErrCandidateNotAdmitted, candidate, activity.ID)
	}
	if ledger.Activity != "" {
		return fmt.Errorf("%w: running=%q", ErrBusyLedger, ledger.Activity)
	}
	if activity.RequiresMovement {
		return fmt.Errorf("%w: activity=%q", ErrMovementNotImplemented, activity.ID)
	}
	if ledger.Occupancy != "" && activity.Occupancy != "" && ledger.Occupancy != activity.Occupancy {
		return fmt.Errorf("%w: held=%q requested=%q", ErrOccupancyConflict, ledger.Occupancy, activity.Occupancy)
	}
	ledger.Actions = append(ledger.Actions, "start:"+activity.ID)
	ledger.Activity = activity.ID
	if activity.Occupancy != "" {
		ledger.Occupancy = activity.Occupancy
	}
	ledger.stopped = false
	return nil
}
