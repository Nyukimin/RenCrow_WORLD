// Package projection validates the provisional CORE projection envelope and
// the reference WorkSet v2 / LifePolicy payload schemas.
//
// This is the WORLD-side inbox input boundary. It does not claim that CORE has
// adopted these schemas or a projection permission contract; the envelope is
// aligned with the v1.4 reference contract so the owner review can replace it
// without changing the durable ordering rules.
package projection

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	ContractVersion = "world.contracts/v1"
	RecordType      = "CoreProjectionEnvelope"

	KindWorkSet    = "full_actor_work_set"
	KindLifePolicy = "life_policy"

	WorkSetContract    = "rencrow.world.work-set/v2"
	LifePolicyContract = "world.life-policy/v1"
	SourceOwnerCORE    = "CORE"

	MaxEnvelopeBytes  = 256 * 1024
	MaxSourceRevision = 9007199254740991
)

var ErrInvalidEnvelope = errors.New("invalid projection envelope")

// Envelope is one fully validated CORE projection. SourceEpoch and
// SourceRevision are read from the schema-owned payload; they are never
// compared with WORLD event_seq/state_seq.
type Envelope struct {
	WorldID         string
	ActorID         string
	ActorInstanceID string
	Kind            string
	SourceEpoch     string
	SourceRevision  uint64
	ValidUntil      time.Time
	Payload         json.RawMessage
	Digest          string
}

type wireEnvelope struct {
	ContractVersion string          `json:"contract_version"`
	RecordType      string          `json:"record_type"`
	WorldID         string          `json:"world_id"`
	TimelineID      string          `json:"timeline_id"`
	TimelineEpoch   string          `json:"timeline_epoch"`
	ActorInstanceID string          `json:"actor_instance_id"`
	ProjectionKind  string          `json:"projection_kind"`
	Payload         json.RawMessage `json:"payload"`
}

type workSetPayload struct {
	ContractVersion string     `json:"contract_version"`
	WorldID         string     `json:"world_id"`
	ActorID         string     `json:"actor_id"`
	SourceOwner     string     `json:"source_owner"`
	SourceEpoch     string     `json:"source_epoch"`
	SourceRevision  uint64     `json:"source_revision"`
	ValidUntil      string     `json:"valid_until"`
	ActiveWork      []workItem `json:"active_work"`
}

type workItem struct {
	TaskID                   string `json:"task_id"`
	RunID                    string `json:"run_id,omitempty"`
	ExecutionState           string `json:"execution_state"`
	WorkClass                string `json:"work_class"`
	RequiresHousePresence    *bool  `json:"requires_house_presence"`
	ClassificationReasonCode string `json:"classification_reason_code"`
}

type lifePolicyPayload struct {
	ContractVersion                string   `json:"contract_version"`
	WorldID                        string   `json:"world_id"`
	ActorID                        string   `json:"actor_id"`
	SourceOwner                    string   `json:"source_owner"`
	SourceEpoch                    string   `json:"source_epoch"`
	SourceRevision                 uint64   `json:"source_revision"`
	ValidUntil                     string   `json:"valid_until"`
	PersonaRevision                string   `json:"persona_revision"`
	AllowedNonCognitiveActivityIDs []string `json:"allowed_non_cognitive_activity_ids"`
	ExpiredFallbackActivityIDs     []string `json:"expired_fallback_activity_ids"`
	ConditionProfileRef            string   `json:"condition_profile_ref"`
}

// Decode validates one envelope against the configured destination. It rejects
// unknown fields, duplicate keys, trailing values, a mismatched destination,
// a payload schema mismatch, and an unsupported projection kind.
func Decode(raw []byte, worldID, timelineID, timelineEpoch string) (Envelope, error) {
	if len(raw) == 0 || len(raw) > MaxEnvelopeBytes || !utf8.Valid(raw) {
		return Envelope{}, ErrInvalidEnvelope
	}
	if err := rejectDuplicateKeys(raw); err != nil {
		return Envelope{}, invalid(err)
	}
	var wire wireEnvelope
	if err := decodeStrict(raw, &wire); err != nil {
		return Envelope{}, invalid(err)
	}
	if wire.ContractVersion != ContractVersion || wire.RecordType != RecordType ||
		wire.WorldID != worldID || wire.TimelineID != timelineID || wire.TimelineEpoch != timelineEpoch ||
		!validExact(wire.ActorInstanceID, 256) || len(wire.Payload) == 0 ||
		!bytes.HasPrefix(bytes.TrimSpace(wire.Payload), []byte("{")) {
		return Envelope{}, ErrInvalidEnvelope
	}
	env := Envelope{
		ActorInstanceID: wire.ActorInstanceID,
		Kind:            wire.ProjectionKind,
		Payload:         append(json.RawMessage(nil), wire.Payload...),
	}
	var err error
	switch wire.ProjectionKind {
	case KindWorkSet:
		err = env.decodeWorkSet()
	case KindLifePolicy:
		err = env.decodeLifePolicy()
	default:
		return Envelope{}, ErrInvalidEnvelope
	}
	if err != nil {
		return Envelope{}, invalid(err)
	}
	if env.WorldID != worldID {
		return Envelope{}, ErrInvalidEnvelope
	}
	env.Digest, err = PayloadDigest(env.Payload)
	if err != nil {
		return Envelope{}, invalid(err)
	}
	return env, nil
}

func (e *Envelope) decodeWorkSet() error {
	var p workSetPayload
	if err := decodeStrict(e.Payload, &p); err != nil {
		return err
	}
	if p.ContractVersion != WorkSetContract || p.SourceOwner != SourceOwnerCORE ||
		!validExact(p.WorldID, 256) || !validExact(p.ActorID, 256) ||
		!validExact(p.SourceEpoch, 256) || !validRevision(p.SourceRevision) ||
		p.ActiveWork == nil {
		return errors.New("invalid work set header")
	}
	if _, err := parseUTC(p.ValidUntil); err != nil {
		return err
	}
	for _, item := range p.ActiveWork {
		if !validExact(item.TaskID, 256) ||
			(item.RunID != "" && !validExact(item.RunID, 256)) ||
			!validExact(item.ClassificationReasonCode, 256) ||
			item.WorkClass != "assigned_work" || item.RequiresHousePresence == nil {
			return errors.New("invalid work item")
		}
		switch item.ExecutionState {
		case "queued", "waiting", "blocked":
		case "running":
			if item.RunID == "" {
				return errors.New("running work item requires run_id")
			}
		default:
			return errors.New("invalid work item execution_state")
		}
	}
	e.WorldID = p.WorldID
	e.ActorID = p.ActorID
	e.SourceEpoch = p.SourceEpoch
	e.SourceRevision = p.SourceRevision
	e.ValidUntil, _ = parseUTC(p.ValidUntil)
	return nil
}

func (e *Envelope) decodeLifePolicy() error {
	var p lifePolicyPayload
	if err := decodeStrict(e.Payload, &p); err != nil {
		return err
	}
	if p.ContractVersion != LifePolicyContract || p.SourceOwner != SourceOwnerCORE ||
		!validExact(p.WorldID, 256) || !validExact(p.ActorID, 256) ||
		!validExact(p.SourceEpoch, 256) || !validRevision(p.SourceRevision) ||
		p.AllowedNonCognitiveActivityIDs == nil || p.ExpiredFallbackActivityIDs == nil ||
		!validExact(p.PersonaRevision, 256) || !validExact(p.ConditionProfileRef, 256) {
		return errors.New("invalid life policy header")
	}
	if _, err := parseUTC(p.ValidUntil); err != nil {
		return err
	}
	if err := validateActivityIDs(p.AllowedNonCognitiveActivityIDs, 128); err != nil {
		return err
	}
	if err := validateActivityIDs(p.ExpiredFallbackActivityIDs, 16); err != nil {
		return err
	}
	e.WorldID = p.WorldID
	e.ActorID = p.ActorID
	e.SourceEpoch = p.SourceEpoch
	e.SourceRevision = p.SourceRevision
	e.ValidUntil, _ = parseUTC(p.ValidUntil)
	return nil
}

func validateActivityIDs(ids []string, max int) error {
	if len(ids) > max {
		return errors.New("too many activity ids")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !validExact(id, 256) || seen[id] {
			return errors.New("invalid or duplicate activity id")
		}
		seen[id] = true
	}
	return nil
}

func validRevision(v uint64) bool { return v >= 1 && v <= MaxSourceRevision }

func parseUTC(value string) (time.Time, error) {
	if !strings.HasSuffix(value, "Z") || strings.ContainsAny(value, "\r\n") {
		return time.Time{}, errors.New("valid_until must be UTC")
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

func validExact(value string, max int) bool {
	if value == "" || len(value) > max || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func decodeStrict(raw []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("more than one JSON value")
		}
		return err
	}
	return nil
}

func invalid(err error) error { return fmt.Errorf("%w: %v", ErrInvalidEnvelope, err) }

// payloadDigest is SHA-256 over deterministic JSON. Object keys are sorted and
// integer numbers are preserved. The supported schemas contain no floating
// point values, so this is equivalent to JCS for the accepted payloads.
func PayloadDigest(raw []byte) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return "", err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return "", errors.New("payload has trailing data")
	}
	var buf bytes.Buffer
	if err := writeCanonical(&buf, value); err != nil {
		return "", err
	}
	sum := sha256.Sum256(buf.Bytes())
	return hex.EncodeToString(sum[:]), nil
}

func writeCanonical(buf *bytes.Buffer, value any) error {
	switch v := value.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if v {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case string:
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		buf.Write(raw)
	case json.Number:
		// The accepted schemas use integer source_revision only.
		if _, err := strconv.ParseUint(v.String(), 10, 64); err != nil {
			return errors.New("non-integer JSON number")
		}
		buf.WriteString(v.String())
	case []any:
		buf.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonical(buf, item); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			raw, err := json.Marshal(key)
			if err != nil {
				return err
			}
			buf.Write(raw)
			buf.WriteByte(':')
			if err := writeCanonical(buf, v[key]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		return errors.New("unsupported JSON value")
	}
	return nil
}

// rejectDuplicateKeys walks the complete JSON value so duplicate keys at any
// nesting depth are rejected instead of silently losing an earlier value.
func rejectDuplicateKeys(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var walk func() error
	walk = func() error {
		token, err := dec.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				keyToken, err := dec.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("object key is not a string")
				}
				if seen[key] {
					return fmt.Errorf("duplicate object key %q", key)
				}
				seen[key] = true
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		case '[':
			for dec.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		default:
			return errors.New("unexpected closing delimiter")
		}
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}
