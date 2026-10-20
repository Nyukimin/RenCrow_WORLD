// Package httpapi is the R07 authenticated HTTP source foundation for WORLD.
//
// Routes and DTOs follow the provisional R07 target table, but they are not an
// adopted cross-module contract. An operator must supply an explicit listen
// address, Store path, scope, and token digests; this package has no default
// port, credential, or browser-facing route.
package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_WORLD/internal/idmap"
	"github.com/Nyukimin/RenCrow_WORLD/internal/store"
)

const maxCommandBody = store.MaxCommandBytes + 4096

type Server struct {
	cfg     Config
	store   *store.Store
	handler http.Handler
	tokens  []tokenEntry
}

type tokenEntry struct {
	id          string
	publisherID string
	sha256      string
	scopes      map[string]bool
	scope       *store.Scope
}

type principal struct {
	id           string
	publisherID  string
	credentialID string
	scopes       map[string]bool
}

func (p principal) allows(scope string) bool {
	if scope == "" {
		return true
	}
	return p.scopes[scope]
}

// New validates cfg, opens its Store, optionally creates the configured initial
// Timeline, and builds the HTTP handler. It never starts a listener.
func New(cfg Config) (*Server, error) {
	cfg, err := NormalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	st, err := store.Open(cfg.StorePath)
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, store: st}
	for _, t := range cfg.Tokens {
		scopes := make(map[string]bool, len(t.Scopes))
		for _, sc := range t.Scopes {
			scopes[sc] = true
		}
		s.tokens = append(s.tokens, tokenEntry{
			id: t.ID, publisherID: t.PublisherID, sha256: t.SHA256, scopes: scopes, scope: t.Scope,
		})
	}
	if cfg.InitialState != nil {
		if _, _, _, err := st.Snapshot(cfg.Scope); errors.Is(err, store.ErrTimelineNotFound) {
			initial := store.State{Actors: cfg.InitialState.Actors}
			if err := st.CreateTimeline(cfg.Scope, initial, cfg.InitialState.Resources); err != nil {
				_ = st.Close()
				return nil, err
			}
		} else if err != nil {
			_ = st.Close()
			return nil, err
		}
	}
	for _, binding := range cfg.CoreProjectionActors {
		if err := st.BindProjectionActor(cfg.Scope, binding.ActorID, binding.ActorInstanceID, nowUTC()); err != nil {
			_ = st.Close()
			return nil, err
		}
	}
	if cfg.AllowProbeAction {
		st.Register("debug.noop", probeHandler)
	}
	s.handler = securityHeaders(s.routes())
	return s, nil
}

func (s *Server) Handler() http.Handler { return s.handler }
func (s *Server) Close() error          { return s.store.Close() }

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", s.handleLive)
	mux.HandleFunc("GET /health/ready", s.auth("", s.handleReady))
	mux.HandleFunc("GET /v1/worlds/{world}/timelines/{timeline}/snapshot", s.auth("read", s.handleSnapshot))
	mux.HandleFunc("GET /v1/worlds/{world}/timelines/{timeline}/events", s.auth("read", s.handleEvents))
	mux.HandleFunc("GET /v1/worlds/{world}/timelines/{timeline}/receipts/{receipt}", s.auth("read", s.handleReceipt))
	mux.HandleFunc("POST /v1/worlds/{world}/timelines/{timeline}/commands", s.auth("write", s.handleCommand))
	mux.HandleFunc("PUT /v1/worlds/{world}/timelines/{timeline}/core-projections/{actor}/{kind}", s.auth("", s.handleCoreProjection))
	mux.HandleFunc("GET /v1/worlds/{world}/timelines/{timeline}/core-projections/{actor}/{kind}", s.auth("", s.handleProjectionHead))
	mux.HandleFunc("POST /v1/worlds/{world}/timelines/{timeline}/core-projections/reconcile", s.auth("projection_reconcile", s.handleProjectionReconcile))
	mux.HandleFunc("GET /v1/worlds/{world}/timelines/{timeline}/stream", s.auth("stream", s.handleStream))
	return mux
}

func (s *Server) auth(scope string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, apiErr := s.authenticate(r)
		if apiErr != nil {
			writeAPIError(w, apiErr.status, apiErr.code, apiErr.message)
			return
		}
		if !p.allows(scope) {
			writeAPIError(w, http.StatusForbidden, "forbidden", "the credential does not allow this operation")
			return
		}
		if rawWorld := r.PathValue("world"); rawWorld != "" {
			world, err := idmap.Decode(rawWorld)
			if err != nil {
				writeAPIError(w, http.StatusBadRequest, "invalid_scope_id", "the world path is not a valid transport ID")
				return
			}
			if world != s.cfg.Scope.WorldID {
				writeAPIError(w, http.StatusForbidden, "scope_mismatch", "the request scope does not match this server")
				return
			}
		}
		if rawTimeline := r.PathValue("timeline"); rawTimeline != "" {
			timeline, err := idmap.Decode(rawTimeline)
			if err != nil {
				writeAPIError(w, http.StatusBadRequest, "invalid_scope_id", "the timeline path is not a valid transport ID")
				return
			}
			if timeline != s.cfg.Scope.TimelineID {
				writeAPIError(w, http.StatusForbidden, "scope_mismatch", "the request scope does not match this server")
				return
			}
		}
		next(w, r)
	}
}

func (s *Server) authenticate(r *http.Request) (principal, *apiError) {
	header := r.Header.Get("Authorization")
	scheme, raw, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || raw == "" || strings.ContainsAny(raw, " \t") {
		return principal{}, &apiError{status: http.StatusUnauthorized, code: "unauthorized", message: "a bearer token is required"}
	}
	sum := sha256.Sum256([]byte(raw))
	want := hex.EncodeToString(sum[:])
	for _, t := range s.tokens {
		if subtle.ConstantTimeCompare([]byte(want), []byte(t.sha256)) != 1 {
			continue
		}
		if t.scope != nil && *t.scope != s.cfg.Scope {
			return principal{}, &apiError{status: http.StatusForbidden, code: "scope_mismatch", message: "the credential is bound to another scope"}
		}
		return principal{
			id: t.id, publisherID: t.publisherID, credentialID: t.id, scopes: t.scopes,
		}, nil
	}
	return principal{}, &apiError{status: http.StatusUnauthorized, code: "unauthorized", message: "a bearer token is required"}
}

func (s *Server) handleLive(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"schema_version": SchemaVersion, "status": "live", "live": true,
		"store_ready": false, "logical_ready": false, "reconciled": false, "spatial_ready": false,
	})
}

func (s *Server) handleReady(w http.ResponseWriter, _ *http.Request) {
	_, seq, hash, err := s.store.Snapshot(s.cfg.Scope)
	if errors.Is(err, store.ErrTimelineNotFound) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"schema_version": SchemaVersion,
			"status":         "not_ready",
			"code":           "timeline_not_found",
			"live":           true,
			"store_ready":    true,
			"logical_ready":  false,
			"reconciled":     false,
			"spatial_ready":  false,
		})
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "store_unavailable", "the store is unavailable")
		return
	}
	reconciled, supersededBy, err := s.store.EpochStatus(s.cfg.Scope)
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "store_unavailable", "the store is unavailable")
		return
	}
	if supersededBy != "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"schema_version": SchemaVersion,
			"status":         "not_ready",
			"code":           "epoch_superseded",
			"superseded_by":  supersededBy,
			"live":           true,
			"store_ready":    true,
			"logical_ready":  false,
			"reconciled":     false,
			"spatial_ready":  false,
		})
		return
	}
	if !reconciled {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"schema_version": SchemaVersion,
			"status":         "not_ready",
			"code":           "epoch_not_reconciled",
			"live":           true,
			"store_ready":    true,
			"logical_ready":  false,
			"reconciled":     false,
			"spatial_ready":  false,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"schema_version": SchemaVersion,
		"status":         "ready",
		"world_id":       s.cfg.Scope.WorldID,
		"timeline_id":    s.cfg.Scope.TimelineID,
		"timeline_epoch": s.cfg.Scope.TimelineEpoch,
		"state_seq":      seq,
		"state_hash":     hash,
		"live":           true,
		"store_ready":    true,
		"logical_ready":  true,
		"reconciled":     true,
		"spatial_ready":  false,
	})
}

type apiError struct {
	status  int
	code    string
	message string
}

type errorBody struct {
	SchemaVersion int      `json:"schema_version"`
	Error         errorDTO `json:"error"`
}

type errorDTO struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorBody{
		SchemaVersion: SchemaVersion,
		Error:         errorDTO{Code: code, Message: message},
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func decodeSingleJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request contains more than one JSON value")
		}
		return err
	}
	return nil
}

func probeHandler(state store.State, payload json.RawMessage) (store.Outcome, error) {
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	return store.Outcome{
		State:  state,
		Events: []store.EventDraft{{Kind: "debug.noop", Payload: payload}},
	}, nil
}

func nowUTC() time.Time { return time.Now().UTC() }
