package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/Nyukimin/RenCrow_WORLD/internal/store"
)

// handleStream serves ordered Events over SSE. A client that supplies
// Last-Event-ID resumes after that transaction boundary. A missing cursor gets a
// Snapshot first. A cursor that is ahead of the Store or inside a transaction
// gets a resync Snapshot instead of a partial transaction.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	cursor, hasCursor, err := parseStreamCursor(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_cursor", "Last-Event-ID and after_seq must be unsigned integers")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "stream_unavailable", "streaming is unavailable")
		return
	}
	snapshot, err := s.snapshotDTO()
	if err != nil {
		if errors.Is(err, store.ErrTimelineNotFound) {
			writeAPIError(w, http.StatusServiceUnavailable, "not_ready", "the timeline is not available")
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "the snapshot could not be read")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, "retry: 3000\n\n")
	flusher.Flush()

	if !hasCursor {
		if err := writeSSE(w, snapshot.StateSeq, "snapshot", snapshot); err != nil {
			return
		}
		flusher.Flush()
		cursor = snapshot.StateSeq
	} else if cursor > snapshot.StateSeq {
		resync := snapshot
		resync.Reason = "cursor_ahead"
		if err := writeSSE(w, resync.StateSeq, "resync", resync); err != nil {
			return
		}
		flusher.Flush()
		cursor = resync.StateSeq
	} else if cursor != 0 {
		boundary, err := s.store.IsTransactionBoundary(s.cfg.Scope, cursor)
		if err != nil {
			_ = writeSSE(w, 0, "error", errorBody{SchemaVersion: SchemaVersion, Error: errorDTO{Code: "store_unavailable"}})
			return
		}
		if !boundary {
			resync := snapshot
			resync.Reason = "cursor_not_boundary"
			if err := writeSSE(w, resync.StateSeq, "resync", resync); err != nil {
				return
			}
			flusher.Flush()
			cursor = resync.StateSeq
		}
	}

	for {
		page, err := s.store.Page(s.cfg.Scope, cursor, s.cfg.Page.MaxEvents, s.cfg.Page.MaxBytes)
		if err != nil {
			_ = writeSSE(w, 0, "error", errorBody{SchemaVersion: SchemaVersion, Error: errorDTO{Code: "store_unavailable"}})
			return
		}
		for _, ev := range page.Events {
			dto := eventDTO{
				Seq:           ev.Seq,
				TransactionID: ev.TransactionID,
				EventID:       ev.EventID,
				Kind:          ev.Kind,
				Payload:       ev.Payload,
				RecordedAt:    ev.RecordedAt,
			}
			if err := writeSSE(w, ev.Seq, "world.event", dto); err != nil {
				return
			}
			cursor = ev.Seq
		}
		if len(page.Events) > 0 {
			flusher.Flush()
		}
		if page.Done {
			break
		}
	}

	poll := time.NewTicker(time.Duration(s.cfg.SSE.PollMillis) * time.Millisecond)
	heartbeat := time.NewTicker(time.Duration(s.cfg.SSE.HeartbeatSeconds) * time.Second)
	defer poll.Stop()
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-poll.C:
			page, err := s.store.Page(s.cfg.Scope, cursor, s.cfg.Page.MaxEvents, s.cfg.Page.MaxBytes)
			if err != nil {
				_ = writeSSE(w, 0, "error", errorBody{SchemaVersion: SchemaVersion, Error: errorDTO{Code: "store_unavailable"}})
				return
			}
			for _, ev := range page.Events {
				dto := eventDTO{
					Seq:           ev.Seq,
					TransactionID: ev.TransactionID,
					EventID:       ev.EventID,
					Kind:          ev.Kind,
					Payload:       ev.Payload,
					RecordedAt:    ev.RecordedAt,
				}
				if err := writeSSE(w, ev.Seq, "world.event", dto); err != nil {
					return
				}
				cursor = ev.Seq
			}
			if len(page.Events) > 0 {
				flusher.Flush()
			}
		}
	}
}

func parseStreamCursor(r *http.Request) (uint64, bool, error) {
	raw := r.Header.Get("Last-Event-ID")
	if raw == "" {
		raw = r.URL.Query().Get("after_seq")
	}
	if raw == "" {
		return 0, false, nil
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, false, err
	}
	return value, true, nil
}

func writeSSE(w http.ResponseWriter, id uint64, event string, data any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if id != 0 {
		if _, err := fmt.Fprintf(w, "id: %d\n", id); err != nil {
			return err
		}
	}
	if event != "" {
		if _, err := fmt.Fprintf(w, "event: %s\n", event); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", raw); err != nil {
		return err
	}
	return nil
}
