package store

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

var projectionAt = time.Date(2026, 10, 20, 2, 3, 4, 0, time.UTC)

func testProjection(scope Scope, actor, instance, kind, epoch string, revision uint64, payload string) Projection {
	if payload == "" {
		payload = `{"value":"` + epoch + `-` + string(rune('0'+revision%10)) + `"}`
	}
	return Projection{
		Scope:           scope,
		ActorID:         actor,
		ActorInstanceID: instance,
		Kind:            kind,
		SourceEpoch:     epoch,
		SourceRevision:  revision,
		Payload:         json.RawMessage(payload),
		PublisherID:     "publisher-" + instance,
		CredentialID:    "credential-" + instance,
		ValidUntil:      projectionAt.Add(24 * time.Hour),
		ReceivedAt:      projectionAt.Add(time.Duration(revision) * time.Second),
	}
}

func bindTestProjectionActors(t *testing.T, s *Store, scope Scope) {
	t.Helper()
	for actor, instance := range map[string]string{"mio": "core-instance-mio", "shiro": "core-instance-shiro"} {
		if err := s.BindProjectionActor(scope, actor, instance, projectionAt); err != nil {
			t.Fatalf("bind %s: %v", actor, err)
		}
	}
}

func applyProjection(t *testing.T, s *Store, p Projection) ProjectionAck {
	t.Helper()
	ack, err := s.ApplyProjection(p)
	if err != nil {
		t.Fatalf("ApplyProjection: %v", err)
	}
	return ack
}

func TestProjectionOrderingIsScopedAndIdempotent(t *testing.T) {
	s, scope := openTestStore(t)
	bindTestProjectionActors(t, s, scope)

	first := testProjection(scope, "mio", "core-instance-mio", ProjectionKindWorkSet, "core-epoch-1", 10, `{"task":"a"}`)
	if ack := applyProjection(t, s, first); ack.Status != ProjectionAccepted {
		t.Fatalf("first ack = %+v", ack)
	}
	if ack := applyProjection(t, s, testProjection(scope, "mio", "core-instance-mio", ProjectionKindWorkSet, "core-epoch-1", 9, `{"task":"old"}`)); ack.Status != ProjectionStaleRevision {
		t.Fatalf("stale ack = %+v", ack)
	}
	if ack := applyProjection(t, s, first); ack.Status != ProjectionDuplicate {
		t.Fatalf("duplicate ack = %+v", ack)
	}
	if ack := applyProjection(t, s, testProjection(scope, "mio", "core-instance-mio", ProjectionKindWorkSet, "core-epoch-1", 10, `{"task":"b"}`)); ack.Status != ProjectionRevisionConflict {
		t.Fatalf("conflict ack = %+v", ack)
	}
	if ack := applyProjection(t, s, testProjection(scope, "mio", "core-instance-mio", ProjectionKindWorkSet, "core-epoch-1", 11, `{"task":"c"}`)); ack.Status != ProjectionAccepted {
		t.Fatalf("newer ack = %+v", ack)
	}

	// Actor, projection kind, and source epoch are independent ordering
	// namespaces. A large revision in one column must not affect another.
	if ack := applyProjection(t, s, testProjection(scope, "mio", "core-instance-mio", ProjectionKindLifePolicy, "core-epoch-1", 1, `{"policy":"a"}`)); ack.Status != ProjectionAccepted {
		t.Fatalf("life policy ack = %+v", ack)
	}
	if ack := applyProjection(t, s, testProjection(scope, "shiro", "core-instance-shiro", ProjectionKindWorkSet, "core-epoch-1", 1, `{"task":"s"}`)); ack.Status != ProjectionAccepted {
		t.Fatalf("other actor ack = %+v", ack)
	}
	if ack := applyProjection(t, s, testProjection(scope, "shiro", "core-instance-shiro", ProjectionKindWorkSet, "core-epoch-1", 2, `{"task":"s2"}`)); ack.Status != ProjectionAccepted {
		t.Fatalf("other actor newer ack = %+v", ack)
	}

	head, err := s.ProjectionHead(scope, "mio", ProjectionKindWorkSet)
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if head.SourceRevision != 11 || head.SourceEpoch != "core-epoch-1" || string(head.Payload) != `{"task":"c"}` {
		t.Fatalf("head = %+v", head)
	}
	life, err := s.ProjectionHead(scope, "mio", ProjectionKindLifePolicy)
	if err != nil || life.SourceRevision != 1 {
		t.Fatalf("life head = %+v, %v", life, err)
	}
}

func TestProjectionPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "world.db")
	scope := testScope()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := s.CreateTimeline(scope, State{}, nil); err != nil {
		t.Fatalf("create: %v", err)
	}
	bindTestProjectionActors(t, s, scope)
	first := testProjection(scope, "mio", "core-instance-mio", ProjectionKindWorkSet, "core-epoch-1", 4, `{"task":"a"}`)
	if ack := applyProjection(t, s, first); ack.Status != ProjectionAccepted {
		t.Fatalf("first = %+v", ack)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s.Close()
	if ack := applyProjection(t, s, first); ack.Status != ProjectionDuplicate {
		t.Fatalf("duplicate after reopen = %+v", ack)
	}
	if ack := applyProjection(t, s, testProjection(scope, "mio", "core-instance-mio", ProjectionKindWorkSet, "core-epoch-1", 5, `{"task":"b"}`)); ack.Status != ProjectionAccepted {
		t.Fatalf("revision after reopen = %+v", ack)
	}
	head, err := s.ProjectionHead(scope, "mio", ProjectionKindWorkSet)
	if err != nil || head.SourceRevision != 5 {
		t.Fatalf("head after reopen = %+v, %v", head, err)
	}
}

func TestProjectionReconcileSwitchesEpoch(t *testing.T) {
	s, scope := openTestStore(t)
	bindTestProjectionActors(t, s, scope)
	if ack := applyProjection(t, s, testProjection(scope, "mio", "core-instance-mio", ProjectionKindWorkSet, "core-epoch-1", 5, `{"task":"a"}`)); ack.Status != ProjectionAccepted {
		t.Fatalf("old epoch = %+v", ack)
	}
	if ack := applyProjection(t, s, testProjection(scope, "mio", "core-instance-mio", ProjectionKindWorkSet, "core-epoch-2", 1, `{"task":"b"}`)); ack.Status != ProjectionEpochRequiresReconcile {
		t.Fatalf("unapproved new epoch = %+v", ack)
	}

	reconciled, err := s.ReconcileProjection(testProjection(scope, "mio", "core-instance-mio", ProjectionKindWorkSet, "core-epoch-2", 1, `{"task":"full"}`))
	if err != nil {
		t.Fatalf("ReconcileProjection: %v", err)
	}
	if reconciled.Status != ProjectionAccepted {
		t.Fatalf("reconcile = %+v", reconciled)
	}
	if ack := applyProjection(t, s, testProjection(scope, "mio", "core-instance-mio", ProjectionKindWorkSet, "core-epoch-1", 999, `{"task":"late"}`)); ack.Status != ProjectionEpochSuperseded {
		t.Fatalf("old epoch after reconcile = %+v", ack)
	}
	if ack := applyProjection(t, s, testProjection(scope, "mio", "core-instance-mio", ProjectionKindWorkSet, "core-epoch-2", 2, `{"task":"next"}`)); ack.Status != ProjectionAccepted {
		t.Fatalf("new epoch next = %+v", ack)
	}
	unchanged, err := s.ReconcileProjection(testProjection(scope, "mio", "core-instance-mio", ProjectionKindWorkSet, "core-epoch-2", 3, `{"task":"again"}`))
	if err != nil {
		t.Fatalf("same epoch reconcile: %v", err)
	}
	if unchanged.Status != ProjectionEpochUnchanged {
		t.Fatalf("same epoch reconcile = %+v", unchanged)
	}
}

func TestProjectionRequiresBoundActorMapping(t *testing.T) {
	s, scope := openTestStore(t)
	if _, err := s.ApplyProjection(testProjection(scope, "mio", "core-instance-mio", ProjectionKindWorkSet, "core-epoch-1", 1, `{}`)); !errors.Is(err, ErrProjectionActorUnbound) {
		t.Fatalf("unbound err = %v", err)
	}
	if err := s.BindProjectionActor(scope, "mio", "core-instance-mio", projectionAt); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if err := s.BindProjectionActor(scope, "mio", "core-instance-mio", projectionAt); err != nil {
		t.Fatalf("idempotent bind: %v", err)
	}
	if _, err := s.ApplyProjection(testProjection(scope, "mio", "other-instance", ProjectionKindWorkSet, "core-epoch-1", 1, `{}`)); !errors.Is(err, ErrProjectionActorConflict) {
		t.Fatalf("wrong instance err = %v", err)
	}
	if err := s.BindProjectionActor(scope, "shiro", "core-instance-mio", projectionAt); !errors.Is(err, ErrProjectionActorConflict) {
		t.Fatalf("duplicate instance bind err = %v", err)
	}
	if err := s.BindProjectionActor(scope, "shiro", "core-instance-shiro", projectionAt); err != nil {
		t.Fatalf("second bind: %v", err)
	}
}

func TestProjectionRejectsInvalidInputAndLimits(t *testing.T) {
	s, scope := openTestStore(t)
	bindTestProjectionActors(t, s, scope)
	base := testProjection(scope, "mio", "core-instance-mio", ProjectionKindWorkSet, "core-epoch-1", 1, `{}`)
	cases := []Projection{
		func() Projection { p := base; p.SourceRevision = 0; return p }(),
		func() Projection { p := base; p.Kind = "memory_dump"; return p }(),
		func() Projection { p := base; p.SourceEpoch = ""; return p }(),
		func() Projection { p := base; p.ActorID = ""; return p }(),
		func() Projection { p := base; p.Payload = json.RawMessage(`[]`); return p }(),
		func() Projection { p := base; p.Payload = json.RawMessage(`{"x":`); return p }(),
		func() Projection { p := base; p.PublisherID = ""; return p }(),
		func() Projection { p := base; p.CredentialID = ""; return p }(),
		func() Projection { p := base; p.ValidUntil = time.Time{}; return p }(),
	}
	for i, p := range cases {
		if _, err := s.ApplyProjection(p); !errors.Is(err, ErrInvalidProjection) {
			t.Fatalf("case %d err = %v", i, err)
		}
	}
	large := base
	large.Payload = json.RawMessage(`{"x":"` + string(make([]byte, MaxProjectionBytes)) + `"}`)
	if _, err := s.ApplyProjection(large); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("large err = %v", err)
	}
}

func TestProjectionRejectsSupersededTimelineEpoch(t *testing.T) {
	s, scope := openTestStore(t)
	bindTestProjectionActors(t, s, scope)
	if _, err := s.db.Exec(`UPDATE timeline SET superseded_by = 'epoch-2'
		WHERE world_id = ? AND timeline_id = ? AND timeline_epoch = ?`, scope.WorldID, scope.TimelineID, scope.TimelineEpoch); err != nil {
		t.Fatalf("supersede: %v", err)
	}
	if _, err := s.ApplyProjection(testProjection(scope, "mio", "core-instance-mio", ProjectionKindWorkSet, "core-epoch-1", 1, `{}`)); !errors.Is(err, ErrEpochSuperseded) {
		t.Fatalf("superseded err = %v", err)
	}
}

// Failure / Problem: D-07 and docs/R09.md promise that re-receiving a
// projection never extends its expiry and that only a newer CORE revision may
// carry a new `valid_until`. Only the acknowledgement status was checked, so a
// change that stored a refused payload's later expiry, or an acknowledgement
// that reported a refused later expiry as held, would have broken the
// documented invariant while every existing test still passed.
//
// Cause: the head write and the acknowledgement are both built from the
// incoming projection, so nothing bound the stored and reported expiry to the
// accepted head once a request was refused.
//
// Lesson / Invariant: for every non-accepted status WORLD keeps and reports the
// held head's source epoch, revision, digest, and `valid_until`. Expiry extends
// only when a newer source revision is accepted, and an older request carrying
// a later expiry never becomes the held validity.
//
// Enforcement / Tests: TestProjectionReReceiptNeverExtendsValidity, against the
// real SQLite EventStore.
func TestProjectionReReceiptNeverExtendsValidity(t *testing.T) {
	s, scope := openTestStore(t)
	bindTestProjectionActors(t, s, scope)

	const kind = ProjectionKindWorkSet
	const actor = "mio"
	heldUntil := projectionAt.Add(time.Hour)
	laterUntil := projectionAt.Add(99 * time.Hour)

	accepted := testProjection(scope, actor, "core-instance-mio", kind, "core-epoch-1", 10, `{"task":"a"}`)
	accepted.ValidUntil = heldUntil
	ack := applyProjection(t, s, accepted)
	if ack.Status != ProjectionAccepted {
		t.Fatalf("first ack = %+v", ack)
	}
	heldDigest := ack.Digest
	heldAcceptedAt := ack.AcceptedAt

	headHeld := func(status string, revision uint64, payload string) {
		t.Helper()
		head, err := s.ProjectionHead(scope, actor, kind)
		if err != nil {
			t.Fatalf("%s head: %v", status, err)
		}
		if head.SourceRevision != revision || head.SourceEpoch != "core-epoch-1" ||
			string(head.Payload) != payload || !head.ValidUntil.Equal(heldUntil) {
			t.Fatalf("%s head = %+v, want revision %d payload %s valid %v",
				status, head, revision, payload, heldUntil)
		}
	}
	ackReportsHeldHead := func(status, wantStatus string) {
		t.Helper()
		if status != wantStatus {
			t.Fatalf("ack status = %q, want %q", status, wantStatus)
		}
		if ack.SourceEpoch != "core-epoch-1" || ack.SourceRevision != 10 ||
			ack.Digest != heldDigest || !ack.ValidUntil.Equal(heldUntil) {
			t.Fatalf("%s ack reported revision %d epoch %s digest %q valid %v, want the held head (revision 10, digest %q, valid %v)",
				wantStatus, ack.SourceRevision, ack.SourceEpoch, ack.Digest, ack.ValidUntil, heldDigest, heldUntil)
		}
	}

	duplicate := accepted
	duplicate.ValidUntil = laterUntil
	ack = applyProjection(t, s, duplicate)
	ackReportsHeldHead(ack.Status, ProjectionDuplicate)
	if !ack.AcceptedAt.Equal(heldAcceptedAt) {
		t.Fatalf("duplicate ack accepted_at = %v, want the held head %v", ack.AcceptedAt, heldAcceptedAt)
	}
	headHeld(ProjectionDuplicate, 10, `{"task":"a"}`)

	stale := testProjection(scope, actor, "core-instance-mio", kind, "core-epoch-1", 9, `{"task":"old"}`)
	stale.ValidUntil = laterUntil
	ack = applyProjection(t, s, stale)
	ackReportsHeldHead(ack.Status, ProjectionStaleRevision)
	headHeld(ProjectionStaleRevision, 10, `{"task":"a"}`)

	conflict := testProjection(scope, actor, "core-instance-mio", kind, "core-epoch-1", 10, `{"task":"other"}`)
	conflict.ValidUntil = laterUntil
	ack = applyProjection(t, s, conflict)
	ackReportsHeldHead(ack.Status, ProjectionRevisionConflict)
	headHeld(ProjectionRevisionConflict, 10, `{"task":"a"}`)

	// Only an accepted newer revision extends expiry.
	newer := testProjection(scope, actor, "core-instance-mio", kind, "core-epoch-1", 11, `{"task":"b"}`)
	newer.ValidUntil = laterUntil
	if ack = applyProjection(t, s, newer); ack.Status != ProjectionAccepted {
		t.Fatalf("newer ack = %+v", ack)
	}
	if !ack.ValidUntil.Equal(laterUntil) {
		t.Fatalf("newer ack valid_until = %v, want %v", ack.ValidUntil, laterUntil)
	}
	head, err := s.ProjectionHead(scope, actor, kind)
	if err != nil {
		t.Fatalf("newer head: %v", err)
	}
	if head.SourceRevision != 11 || !head.ValidUntil.Equal(laterUntil) {
		t.Fatalf("newer head = %+v, want revision 11 valid %v", head, laterUntil)
	}
	heldUntil = laterUntil

	// A newer revision from another source epoch needs authenticated
	// reconciliation first, and the refusal still reports the held head.
	otherEpoch := testProjection(scope, actor, "core-instance-mio", kind, "core-epoch-2", 12, `{"task":"c"}`)
	otherEpoch.ValidUntil = projectionAt.Add(200 * time.Hour)
	ack = applyProjection(t, s, otherEpoch)
	if ack.Status != ProjectionEpochRequiresReconcile {
		t.Fatalf("other epoch ack = %+v", ack)
	}
	if ack.SourceEpoch != "core-epoch-1" || ack.SourceRevision != 11 ||
		ack.Digest != head.Digest || !ack.ValidUntil.Equal(heldUntil) {
		t.Fatalf("requires-reconcile ack reported revision %d epoch %s digest %q valid %v, want the held head (revision 11, digest %q, valid %v)",
			ack.SourceRevision, ack.SourceEpoch, ack.Digest, ack.ValidUntil, head.Digest, heldUntil)
	}
	head, err = s.ProjectionHead(scope, actor, kind)
	if err != nil {
		t.Fatalf("head after refusal: %v", err)
	}
	if head.SourceRevision != 11 || head.SourceEpoch != "core-epoch-1" || !head.ValidUntil.Equal(heldUntil) {
		t.Fatalf("head after refusal = %+v, want the accepted head unchanged", head)
	}
}
