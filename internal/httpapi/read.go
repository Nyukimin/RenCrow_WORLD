package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/Nyukimin/RenCrow_WORLD/internal/store"
)

type snapshotDTO struct {
	SchemaVersion int         `json:"schema_version"`
	WorldID       string      `json:"world_id"`
	TimelineID    string      `json:"timeline_id"`
	TimelineEpoch string      `json:"timeline_epoch"`
	StateSeq      uint64      `json:"state_seq"`
	StateHash     string      `json:"state_hash"`
	State         store.State `json:"state"`
	Reason        string      `json:"reason,omitempty"`
}

type eventDTO struct {
	Seq           uint64          `json:"seq"`
	TransactionID string          `json:"transaction_id"`
	EventID       string          `json:"event_id"`
	Kind          string          `json:"kind"`
	Payload       json.RawMessage `json:"payload"`
	RecordedAt    time.Time       `json:"recorded_at"`
}

type eventsDTO struct {
	SchemaVersion int        `json:"schema_version"`
	Events        []eventDTO `json:"events"`
	NextSeq       uint64     `json:"next_seq"`
	Done          bool       `json:"done"`
}

type receiptDTO struct {
	ReceiptID     string    `json:"receipt_id"`
	Status        string    `json:"status"`
	Reason        string    `json:"reason,omitempty"`
	TransactionID string    `json:"transaction_id,omitempty"`
	EventIDs      []string  `json:"event_ids"`
	FirstSeq      uint64    `json:"first_seq"`
	LastSeq       uint64    `json:"last_seq"`
	StateSeq      uint64    `json:"state_seq"`
	RecordedAt    time.Time `json:"recorded_at"`
}

type receiptBody struct {
	SchemaVersion int        `json:"schema_version"`
	Receipt       receiptDTO `json:"receipt"`
	Duplicate     bool       `json:"duplicate,omitempty"`
}

func (s *Server) snapshotDTO() (snapshotDTO, error) {
	state, seq, hash, err := s.store.Snapshot(s.cfg.Scope)
	if err != nil {
		return snapshotDTO{}, err
	}
	return snapshotDTO{
		SchemaVersion: SchemaVersion,
		WorldID:       s.cfg.Scope.WorldID,
		TimelineID:    s.cfg.Scope.TimelineID,
		TimelineEpoch: s.cfg.Scope.TimelineEpoch,
		StateSeq:      seq,
		StateHash:     hash,
		State:         state,
	}, nil
}

func (s *Server) handleSnapshot(w http.ResponseWriter, _ *http.Request) {
	dto, err := s.snapshotDTO()
	if errors.Is(err, store.ErrTimelineNotFound) {
		writeAPIError(w, http.StatusServiceUnavailable, "not_ready", "the timeline is not available")
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "the snapshot could not be read")
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	after, err := parseUintQuery(r, "after_seq", 0)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_after_seq", "after_seq must be an unsigned integer")
		return
	}
	limit := s.cfg.Page.MaxEvents
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > s.cfg.Page.MaxEvents {
			writeAPIError(w, http.StatusBadRequest, "invalid_limit", "limit is outside the configured page bound")
			return
		}
		limit = n
	}
	page, err := s.store.Page(s.cfg.Scope, after, limit, s.cfg.Page.MaxBytes)
	if errors.Is(err, store.ErrTimelineNotFound) {
		writeAPIError(w, http.StatusServiceUnavailable, "not_ready", "the timeline is not available")
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "the event page could not be read")
		return
	}
	out := eventsDTO{SchemaVersion: SchemaVersion, Events: make([]eventDTO, 0, len(page.Events)), NextSeq: page.NextSeq, Done: page.Done}
	for _, ev := range page.Events {
		out.Events = append(out.Events, eventDTO{
			Seq:           ev.Seq,
			TransactionID: ev.TransactionID,
			EventID:       ev.EventID,
			Kind:          ev.Kind,
			Payload:       ev.Payload,
			RecordedAt:    ev.RecordedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleReceipt(w http.ResponseWriter, r *http.Request) {
	receipt, err := s.store.GetReceipt(s.cfg.Scope, r.PathValue("receipt"))
	if errors.Is(err, store.ErrReceiptNotFound) {
		writeAPIError(w, http.StatusNotFound, "receipt_not_found", "the receipt does not exist")
		return
	}
	if errors.Is(err, store.ErrInvalidCommand) {
		writeAPIError(w, http.StatusBadRequest, "invalid_receipt", "the receipt id is invalid")
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "the receipt could not be read")
		return
	}
	writeJSON(w, http.StatusOK, receiptBody{SchemaVersion: SchemaVersion, Receipt: toReceiptDTO(receipt)})
}

func toReceiptDTO(r store.Receipt) receiptDTO {
	return receiptDTO{
		ReceiptID:     r.ID,
		Status:        r.Status,
		Reason:        r.Reason,
		TransactionID: r.TransactionID,
		EventIDs:      append([]string(nil), r.EventIDs...),
		FirstSeq:      r.FirstSeq,
		LastSeq:       r.LastSeq,
		StateSeq:      r.StateSeq,
		RecordedAt:    r.RecordedAt,
	}
}

func parseUintQuery(r *http.Request, key string, fallback uint64) (uint64, error) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return fallback, nil
	}
	return strconv.ParseUint(raw, 10, 64)
}
