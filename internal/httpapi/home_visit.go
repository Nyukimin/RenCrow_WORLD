package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/Nyukimin/RenCrow_WORLD/internal/life"
	"github.com/Nyukimin/RenCrow_WORLD/internal/store"
)

func (s *Server) startVisitExpiry() func() {
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.expireVisit()
			}
		}
	}()
	return func() { stop(); <-done }
}

func (s *Server) expireVisit() {
	state, _, _, err := s.store.Snapshot(s.cfg.Scope)
	actor, present := state.Actors[life.HomeVisitorID]
	if err != nil || !present || actor.VisitID == "" {
		return
	}
	until, err := time.Parse(time.RFC3339Nano, actor.VisitUntil)
	now := nowUTC()
	if err != nil || now.Before(until) {
		return
	}
	payload, _ := json.Marshal(map[string]string{"actor_id": life.HomeVisitorID, "visit_id": actor.VisitID})
	key := sha256.Sum256([]byte(actor.VisitID + "\x00" + actor.VisitUntil))
	_, err = s.store.Append(store.Command{
		Scope: s.cfg.Scope, ClientID: "\x01world.home_visit_expiry", IdempotencyKey: fmt.Sprintf("%x", key),
		Action: life.ActionHomeVisitExpire, Payload: payload, RecordedAt: now,
	})
	if err != nil {
		log.Printf("WORLD home visit expiration failed: %v", err)
	}
}
