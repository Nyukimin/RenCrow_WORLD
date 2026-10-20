package store

import (
	"errors"
	"testing"
	"time"
)

func TestCompleteReconcileCheckedRequiresFreshProjectionHeads(t *testing.T) {
	s, scope := openTestStore(t)
	bindTestProjectionActors(t, s, scope)
	if _, err := s.db.Exec(`UPDATE timeline SET reconciled = 0
		WHERE world_id = ? AND timeline_id = ? AND timeline_epoch = ?`, scope.WorldID, scope.TimelineID, scope.TimelineEpoch); err != nil {
		t.Fatalf("close epoch: %v", err)
	}

	at := projectionAt.Add(time.Hour)
	required := []ProjectionRequirement{
		{ActorID: "mio", Kind: ProjectionKindWorkSet},
		{ActorID: "mio", Kind: ProjectionKindLifePolicy},
	}
	if err := s.CompleteReconcileChecked(scope, "core-revision-1", required, at); !errors.Is(err, ErrProjectionNotFound) {
		t.Fatalf("missing projection err = %v", err)
	}
	if reconciled, _, err := s.EpochStatus(scope); err != nil || reconciled {
		t.Fatalf("epoch opened without projections = %v, %v", reconciled, err)
	}

	applyProjection(t, s, testProjection(scope, "mio", "core-instance-mio", ProjectionKindWorkSet, "core-epoch-1", 1, `{"kind":"work"}`))
	applyProjection(t, s, testProjection(scope, "mio", "core-instance-mio", ProjectionKindLifePolicy, "core-epoch-1", 1, `{"kind":"policy"}`))
	if err := s.CompleteReconcileChecked(scope, "core-revision-1", required, at); err != nil {
		t.Fatalf("checked reconcile: %v", err)
	}
	if reconciled, _, err := s.EpochStatus(scope); err != nil || !reconciled {
		t.Fatalf("epoch not opened = %v, %v", reconciled, err)
	}
}

func TestCompleteReconcileCheckedRejectsExpiredHead(t *testing.T) {
	s, scope := openTestStore(t)
	bindTestProjectionActors(t, s, scope)
	if _, err := s.db.Exec(`UPDATE timeline SET reconciled = 0
		WHERE world_id = ? AND timeline_id = ? AND timeline_epoch = ?`, scope.WorldID, scope.TimelineID, scope.TimelineEpoch); err != nil {
		t.Fatalf("close epoch: %v", err)
	}
	p := testProjection(scope, "mio", "core-instance-mio", ProjectionKindWorkSet, "core-epoch-1", 1, `{"kind":"work"}`)
	p.ValidUntil = projectionAt.Add(time.Hour)
	applyProjection(t, s, p)

	err := s.CompleteReconcileChecked(scope, "core-revision-1",
		[]ProjectionRequirement{{ActorID: "mio", Kind: ProjectionKindWorkSet}},
		projectionAt.Add(2*time.Hour))
	if !errors.Is(err, ErrProjectionExpired) {
		t.Fatalf("expired projection err = %v", err)
	}
	if reconciled, _, err := s.EpochStatus(scope); err != nil || reconciled {
		t.Fatalf("expired epoch opened = %v, %v", reconciled, err)
	}
}

func TestCompleteReconcileCheckedRejectsUnboundAndDuplicateRequirements(t *testing.T) {
	s, scope := openTestStore(t)
	bindTestProjectionActors(t, s, scope)
	at := projectionAt.Add(time.Hour)
	if err := s.CompleteReconcileChecked(scope, "core-revision-1",
		[]ProjectionRequirement{{ActorID: "mio", Kind: ProjectionKindWorkSet}}, at); !errors.Is(err, ErrProjectionNotFound) {
		t.Fatalf("unbound missing head err = %v", err)
	}
	if err := s.CompleteReconcileChecked(scope, "core-revision-1",
		[]ProjectionRequirement{
			{ActorID: "mio", Kind: ProjectionKindWorkSet},
			{ActorID: "mio", Kind: ProjectionKindWorkSet},
		}, at); !errors.Is(err, ErrInvalidProjection) {
		t.Fatalf("duplicate requirement err = %v", err)
	}
	if err := s.CompleteReconcileChecked(scope, "core-revision-1", nil, at); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("empty requirement err = %v", err)
	}
}
