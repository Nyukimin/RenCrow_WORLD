package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_WORLD/internal/store"
)

type commandRequest struct {
	Action           string          `json:"action"`
	TimelineEpoch    string          `json:"timeline_epoch"`
	Payload          json.RawMessage `json:"payload"`
	ExpectedStateSeq *uint64         `json:"expected_state_seq"`
}

func (s *Server) handleCommand(w http.ResponseWriter, r *http.Request) {
	if contentType := r.Header.Get("Content-Type"); contentType != "" && !strings.HasPrefix(contentType, "application/json") {
		writeAPIError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "commands require application/json")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxCommandBody)
	var req commandRequest
	if err := decodeSingleJSON(r, &req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeAPIError(w, http.StatusRequestEntityTooLarge, "command_too_large", "the command body exceeds the limit")
			return
		}
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "the command body is not one valid JSON object")
		return
	}
	if !validOpaque(req.Action, 128) {
		writeAPIError(w, http.StatusBadRequest, "invalid_action", "action is required and must be an exact opaque value")
		return
	}
	if !validOpaque(req.TimelineEpoch, 256) {
		writeAPIError(w, http.StatusBadRequest, "missing_timeline_epoch", "timeline_epoch is required and must be an exact opaque value")
		return
	}
	if req.TimelineEpoch != s.cfg.Scope.TimelineEpoch {
		writeAPIError(w, http.StatusConflict, "timeline_epoch_mismatch", "timeline_epoch does not match this server epoch")
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if !validOpaque(key, 128) {
		writeAPIError(w, http.StatusBadRequest, "invalid_idempotency_key", "Idempotency-Key is required and must be an exact opaque value")
		return
	}
	payload := req.Payload
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	if !json.Valid(payload) || !utf8.Valid(payload) {
		writeAPIError(w, http.StatusBadRequest, "invalid_payload", "payload must be valid UTF-8 JSON")
		return
	}
	principal, apiErr := s.authenticate(r)
	if apiErr != nil {
		// The auth wrapper already authenticated this request; this branch is a
		// defensive guard for direct handler use.
		writeAPIError(w, apiErr.status, apiErr.code, apiErr.message)
		return
	}
	result, err := s.store.Append(store.Command{
		Scope:            s.cfg.Scope,
		ClientID:         principal.publisherID,
		IdempotencyKey:   key,
		ExpectedStateSeq: req.ExpectedStateSeq,
		Action:           req.Action,
		Payload:          payload,
		RecordedAt:       nowUTC(),
	})
	switch {
	case err == nil:
	case errors.Is(err, store.ErrIdempotencyConflict):
		writeAPIError(w, http.StatusConflict, "idempotency_conflict", "the idempotency key was already used with another body")
		return
	case errors.Is(err, store.ErrLimitExceeded):
		writeAPIError(w, http.StatusRequestEntityTooLarge, "command_too_large", "the command exceeds the configured limit")
		return
	case errors.Is(err, store.ErrInvalidCommand):
		writeAPIError(w, http.StatusBadRequest, "invalid_command", "the command is invalid")
		return
	case errors.Is(err, store.ErrTimelineNotFound):
		writeAPIError(w, http.StatusServiceUnavailable, "not_ready", "the timeline is not available")
		return
	default:
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "the command could not be committed")
		return
	}

	status := http.StatusOK
	if result.Receipt.Status == "rejected" {
		status = http.StatusConflict
		switch result.Receipt.Reason {
		case "unknown_action", "handler_error":
			status = http.StatusUnprocessableEntity
		}
	}
	writeJSON(w, status, receiptBody{
		SchemaVersion: SchemaVersion,
		Receipt:       toReceiptDTO(result.Receipt),
		Duplicate:     result.Duplicate,
	})
}

func validOpaque(value string, maxLen int) bool {
	if value == "" || len(value) > maxLen || !utf8.ValidString(value) || strings.TrimSpace(value) != value || hasControl(value) {
		return false
	}
	return true
}
