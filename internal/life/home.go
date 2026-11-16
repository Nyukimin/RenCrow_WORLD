package life

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_WORLD/internal/store"
)

const (
	HomeLayoutSchemaVersion = "world.home-layout/v1"
	HomeActivityMove        = "move"
	HomeActivitySleep       = "sleep"
	HomeActivityExercise    = "exercise"
	HomeActivityMirrorPose  = "mirror_pose"

	HomeSlotBed      = "bed"
	HomeSlotExercise = "exercise"
	HomeSlotMirror   = "mirror"
	HomeSlotRest     = "rest"

	ActionHomeMove       = "life.home.move"
	ActionHomeSleep      = "life.home.sleep"
	ActionHomeWake       = "life.home.wake"
	ActionHomeExercise   = "life.home.exercise"
	ActionHomeMirrorPose = "life.home.mirror_pose"
	ActionHomeFinish     = "life.home.finish"
	ActionHomeStop       = "life.home.stop"

	EventHomeMoveStarted               = "life.home.move.started"
	EventHomeMoveArrived               = "life.home.move.arrived"
	EventHomeSleepStarted              = "life.home.sleep.started"
	EventHomeSleepWoke                 = "life.home.sleep.woke"
	EventHomeExerciseStarted           = "life.home.exercise.started"
	EventHomeExerciseCompleted         = "life.home.exercise.completed"
	EventHomeMirrorPoseStarted         = "life.home.mirror_pose.started"
	EventHomeMirrorPoseCompleted       = "life.home.mirror_pose.completed"
	EventHomeActivityStopped           = "life.home.activity.stopped"
	HomeActivityStatusRunning          = "running"
	HomeActivityStatusCompleted        = "completed"
	HomeActivityStatusWoke             = "woke"
	HomeActivityStatusStopped          = "stopped"
	maxHomeLayoutBytes                 = 1 << 20
	maxHomeRooms                       = 256
	maxHomePaths                       = 1024
	maxHomeSlots                       = 512
	maxHomeWaypoints                   = 256
	maxHomeDurationMS            int64 = 7 * 24 * 60 * 60 * 1000
)

var (
	ErrHomePayload          = errors.New("home command payload is invalid")
	ErrHomeLayout           = errors.New("home layout is invalid")
	ErrHomeActorUnknown     = errors.New("home command Actor is not in the Timeline")
	ErrHomeActivityActive   = errors.New("home Actor already has an active Activity")
	ErrHomeActivityHeld     = errors.New("home command does not target the exact active Activity")
	ErrHomeRoomUnknown      = errors.New("home room is not configured")
	ErrHomeNoRoute          = errors.New("home has no configured route to the destination")
	ErrHomePrivateRoom      = errors.New("home room is private to another Actor")
	ErrHomePosition         = errors.New("home Actor is not at a confirmed configured position")
	ErrHomeWrongSlot        = errors.New("home action requires the Actor at its configured slot")
	ErrHomePoseNotAllowed   = errors.New("home mirror pose is not in the Actor repertoire")
	ErrHomeNotDue           = errors.New("home Activity has not reached its configured due time")
	ErrHomeSleepUseWake     = errors.New("sleep ends through the explicit wake command")
	ErrHomeNotInterruptible = errors.New("home Activity has no safe stop action")
	ErrHomeLayoutChanged    = errors.New("home layout changed while the Activity was active")
	ErrHomeClockReversed    = errors.New("home server clock precedes the Activity start")
	ErrHomeAlreadyThere     = errors.New("home Actor is already at the requested position")
	ErrHomeSeatOccupied     = errors.New("home seat is occupied")
)

// Vec3 is one finite [x,y,z] coordinate in the measured scene coordinate frame.
type Vec3 [3]float64

func (v *Vec3) UnmarshalJSON(raw []byte) error {
	var values []float64
	if err := json.Unmarshal(raw, &values); err != nil {
		return fmt.Errorf("coordinate must be a three-number array: %w", err)
	}
	if len(values) != 3 {
		return errors.New("coordinate must contain exactly three numbers")
	}
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return errors.New("coordinate values must be finite")
		}
	}
	copy(v[:], values)
	return nil
}

type HomeLayout struct {
	SchemaVersion string                  `json:"schema_version"`
	SceneSHA256   string                  `json:"scene_sha256"`
	RoomAnchors   map[string]Vec3         `json:"room_anchors"`
	Paths         []HomePath              `json:"paths"`
	PrivateRooms  map[string]string       `json:"private_rooms"`
	Slots         []HomeSlot              `json:"slots"`
	MirrorPoses   map[string][]MirrorPose `json:"mirror_poses,omitempty"`
}

// HomePath is a directed, measured edge between two configured room anchors.
// Points are intermediate waypoints; the room anchors are its endpoints.
type HomePath struct {
	ID         string `json:"id"`
	FromRoomID string `json:"from_room_id"`
	ToRoomID   string `json:"to_room_id"`
	Points     []Vec3 `json:"points,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

// HomeSlot is an exact physical point. ApproachPoints is the measured in-room
// path from the room anchor to Anchor; without it, Anchor must equal that room
// anchor. Exercise slots provide the configured exercise duration.
type HomeSlot struct {
	ID                 string `json:"id"`
	Kind               string `json:"kind"`
	ActorID            string `json:"actor_id,omitempty"`
	RoomID             string `json:"room_id"`
	Anchor             Vec3   `json:"anchor"`
	ApproachPoints     []Vec3 `json:"approach_points,omitempty"`
	ApproachDurationMS int64  `json:"approach_duration_ms,omitempty"`
	DurationMS         int64  `json:"duration_ms,omitempty"`
}

// MirrorPose is an operator-authorized Kuro-style repertoire entry. The Actor
// is selected by the layout key, so the engine has no deployment-specific ID.
type MirrorPose struct {
	ID         string `json:"id"`
	SlotID     string `json:"slot_id"`
	DurationMS int64  `json:"duration_ms"`
}

// ParseHomeLayout accepts one strict, versioned operator JSON object and
// rejects duplicate keys, unknown fields, trailing values, and invalid geometry.
func ParseHomeLayout(raw []byte) (HomeLayout, error) {
	if len(raw) == 0 || len(raw) > maxHomeLayoutBytes {
		return HomeLayout{}, fmt.Errorf("%w: file must be 1 byte..1 MiB", ErrHomeLayout)
	}
	if err := rejectHomeDuplicateKeys(raw); err != nil {
		return HomeLayout{}, fmt.Errorf("%w: %v", ErrHomeLayout, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var layout HomeLayout
	if err := decoder.Decode(&layout); err != nil {
		return HomeLayout{}, fmt.Errorf("%w: %v", ErrHomeLayout, err)
	}
	if err := decoder.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		if err == nil {
			return HomeLayout{}, fmt.Errorf("%w: trailing JSON value", ErrHomeLayout)
		}
		return HomeLayout{}, fmt.Errorf("%w: %v", ErrHomeLayout, err)
	}
	normalized, err := normalizeHomeLayout(layout)
	if err != nil {
		return HomeLayout{}, err
	}
	return normalized, nil
}

// LoadHomeLayout loads the separate optional production layout input. It does
// not invent defaults or use a test fixture when the file is missing.
func LoadHomeLayout(path string) (HomeLayout, error) {
	if path == "" || strings.TrimSpace(path) != path {
		return HomeLayout{}, fmt.Errorf("%w: path is required and exact", ErrHomeLayout)
	}
	info, err := os.Stat(path)
	if err != nil {
		return HomeLayout{}, err
	}
	if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maxHomeLayoutBytes {
		return HomeLayout{}, fmt.Errorf("%w: layout must be a regular file under 1 MiB", ErrHomeLayout)
	}
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return HomeLayout{}, err
	}
	return ParseHomeLayout(data)
}

func normalizeHomeLayout(layout HomeLayout) (HomeLayout, error) {
	layout = cloneHomeLayout(layout)
	if layout.SchemaVersion != HomeLayoutSchemaVersion {
		return HomeLayout{}, fmt.Errorf("%w: schema_version must be %q", ErrHomeLayout, HomeLayoutSchemaVersion)
	}
	layout.SceneSHA256 = strings.ToLower(layout.SceneSHA256)
	if len(layout.SceneSHA256) != 64 {
		return HomeLayout{}, fmt.Errorf("%w: scene_sha256 must be a 64-character SHA-256", ErrHomeLayout)
	}
	if _, err := hex.DecodeString(layout.SceneSHA256); err != nil {
		return HomeLayout{}, fmt.Errorf("%w: scene_sha256 is not hexadecimal", ErrHomeLayout)
	}
	if len(layout.RoomAnchors) == 0 || len(layout.RoomAnchors) > maxHomeRooms {
		return HomeLayout{}, fmt.Errorf("%w: room_anchors must contain 1..%d rooms", ErrHomeLayout, maxHomeRooms)
	}
	for roomID := range layout.RoomAnchors {
		if !validHomeID(roomID) {
			return HomeLayout{}, fmt.Errorf("%w: invalid room ID %q", ErrHomeLayout, roomID)
		}
	}
	if len(layout.Paths) > maxHomePaths || len(layout.Slots) > maxHomeSlots {
		return HomeLayout{}, fmt.Errorf("%w: too many paths or slots", ErrHomeLayout)
	}
	privateRooms := map[string]string{}
	for actorID, roomID := range layout.PrivateRooms {
		if !validHomeID(actorID) || !validHomeID(roomID) {
			return HomeLayout{}, fmt.Errorf("%w: invalid private room mapping", ErrHomeLayout)
		}
		if owner := privateRooms[roomID]; owner != "" {
			return HomeLayout{}, fmt.Errorf("%w: room %q is assigned to more than one Actor", ErrHomeLayout, roomID)
		}
		if _, ok := layout.RoomAnchors[roomID]; !ok {
			return HomeLayout{}, fmt.Errorf("%w: private room %q is not configured", ErrHomeLayout, roomID)
		}
		privateRooms[roomID] = actorID
	}
	pathIDs := map[string]bool{}
	for i, path := range layout.Paths {
		if !validHomeID(path.ID) || pathIDs[path.ID] {
			return HomeLayout{}, fmt.Errorf("%w: path %d has an invalid or duplicate ID", ErrHomeLayout, i)
		}
		pathIDs[path.ID] = true
		if !validHomeID(path.FromRoomID) || !validHomeID(path.ToRoomID) || path.FromRoomID == path.ToRoomID {
			return HomeLayout{}, fmt.Errorf("%w: path %q must join two distinct rooms", ErrHomeLayout, path.ID)
		}
		if _, ok := layout.RoomAnchors[path.FromRoomID]; !ok {
			return HomeLayout{}, fmt.Errorf("%w: path %q starts at an unknown room", ErrHomeLayout, path.ID)
		}
		if _, ok := layout.RoomAnchors[path.ToRoomID]; !ok {
			return HomeLayout{}, fmt.Errorf("%w: path %q ends at an unknown room", ErrHomeLayout, path.ID)
		}
		if path.DurationMS <= 0 || path.DurationMS > maxHomeDurationMS || len(path.Points) > maxHomeWaypoints {
			return HomeLayout{}, fmt.Errorf("%w: path %q has invalid duration or waypoint count", ErrHomeLayout, path.ID)
		}
		for _, point := range path.Points {
			if !finiteVec3(point) {
				return HomeLayout{}, fmt.Errorf("%w: path %q has an invalid coordinate", ErrHomeLayout, path.ID)
			}
		}
		if pathDistance(layout.RoomAnchors[path.FromRoomID], path.Points, layout.RoomAnchors[path.ToRoomID]) <= 0 {
			return HomeLayout{}, fmt.Errorf("%w: path %q has zero measured length", ErrHomeLayout, path.ID)
		}
	}
	slotIDs := map[string]HomeSlot{}
	for i, slot := range layout.Slots {
		if !validHomeID(slot.ID) || !validHomeID(slot.RoomID) {
			return HomeLayout{}, fmt.Errorf("%w: slot %d has an invalid ID or room", ErrHomeLayout, i)
		}
		if _, exists := slotIDs[slot.ID]; exists {
			return HomeLayout{}, fmt.Errorf("%w: duplicate slot ID %q", ErrHomeLayout, slot.ID)
		}
		if _, ok := layout.RoomAnchors[slot.RoomID]; !ok {
			return HomeLayout{}, fmt.Errorf("%w: slot %q uses an unknown room", ErrHomeLayout, slot.ID)
		}
		if slot.ActorID != "" && !validHomeID(slot.ActorID) {
			return HomeLayout{}, fmt.Errorf("%w: slot %q has an invalid Actor ID", ErrHomeLayout, slot.ID)
		}
		switch slot.Kind {
		case HomeSlotBed, HomeSlotExercise, HomeSlotMirror, HomeSlotRest:
		default:
			return HomeLayout{}, fmt.Errorf("%w: slot %q has an unknown kind", ErrHomeLayout, slot.ID)
		}
		if len(slot.ApproachPoints) > maxHomeWaypoints || !finiteVec3(slot.Anchor) {
			return HomeLayout{}, fmt.Errorf("%w: slot %q has invalid geometry", ErrHomeLayout, slot.ID)
		}
		for _, point := range slot.ApproachPoints {
			if !finiteVec3(point) {
				return HomeLayout{}, fmt.Errorf("%w: slot %q has an invalid approach coordinate", ErrHomeLayout, slot.ID)
			}
		}
		roomAnchor := layout.RoomAnchors[slot.RoomID]
		approachLength := pathDistance(roomAnchor, slot.ApproachPoints, slot.Anchor)
		if len(slot.ApproachPoints) == 0 {
			if slot.Anchor != roomAnchor || slot.ApproachDurationMS != 0 {
				return HomeLayout{}, fmt.Errorf("%w: slot %q needs measured approach_points and duration", ErrHomeLayout, slot.ID)
			}
		} else if slot.ApproachDurationMS <= 0 || slot.ApproachDurationMS > maxHomeDurationMS || approachLength <= 0 {
			return HomeLayout{}, fmt.Errorf("%w: slot %q has invalid approach duration or length", ErrHomeLayout, slot.ID)
		}
		switch slot.Kind {
		case HomeSlotBed:
			if slot.ActorID == "" || layout.PrivateRooms[slot.ActorID] != slot.RoomID {
				return HomeLayout{}, fmt.Errorf("%w: bed slot %q must belong to its Actor's private room", ErrHomeLayout, slot.ID)
			}
			if slot.DurationMS != 0 {
				return HomeLayout{}, fmt.Errorf("%w: bed slot %q cannot impose a sleep duration", ErrHomeLayout, slot.ID)
			}
		case HomeSlotExercise:
			if slot.DurationMS <= 0 || slot.DurationMS > maxHomeDurationMS {
				return HomeLayout{}, fmt.Errorf("%w: exercise slot %q requires a bounded duration", ErrHomeLayout, slot.ID)
			}
		case HomeSlotMirror, HomeSlotRest:
			if slot.DurationMS != 0 {
				return HomeLayout{}, fmt.Errorf("%w: slot %q duration belongs to its activity definition", ErrHomeLayout, slot.ID)
			}
		}
		if owner, private := privateRooms[slot.RoomID]; private && slot.ActorID != owner {
			return HomeLayout{}, fmt.Errorf("%w: slot %q is in a private room and must name its owner", ErrHomeLayout, slot.ID)
		}
		slotIDs[slot.ID] = slot
	}
	poseIDs := map[string]bool{}
	for actorID, poses := range layout.MirrorPoses {
		if !validHomeID(actorID) || len(poses) == 0 || len(poses) > maxHomeSlots {
			return HomeLayout{}, fmt.Errorf("%w: invalid mirror repertoire for Actor %q", ErrHomeLayout, actorID)
		}
		if _, ok := layout.RoomAnchors[layout.PrivateRooms[actorID]]; !ok {
			return HomeLayout{}, fmt.Errorf("%w: mirror repertoire Actor %q has no configured private room", ErrHomeLayout, actorID)
		}
		for _, pose := range poses {
			key := actorID + "\x00" + pose.ID
			if !validHomeID(pose.ID) || poseIDs[key] || pose.DurationMS <= 0 || pose.DurationMS > maxHomeDurationMS {
				return HomeLayout{}, fmt.Errorf("%w: invalid or duplicate pose for Actor %q", ErrHomeLayout, actorID)
			}
			poseIDs[key] = true
			slot, ok := slotIDs[pose.SlotID]
			if !ok || slot.Kind != HomeSlotMirror || (slot.ActorID != "" && slot.ActorID != actorID) {
				return HomeLayout{}, fmt.Errorf("%w: pose %q uses an unauthorized mirror slot", ErrHomeLayout, pose.ID)
			}
			if owner, private := privateRooms[slot.RoomID]; private && owner != actorID {
				return HomeLayout{}, fmt.Errorf("%w: pose %q targets another Actor's private room", ErrHomeLayout, pose.ID)
			}
		}
	}
	sort.Slice(layout.Paths, func(i, j int) bool { return layout.Paths[i].ID < layout.Paths[j].ID })
	sort.Slice(layout.Slots, func(i, j int) bool { return layout.Slots[i].ID < layout.Slots[j].ID })
	for actorID := range layout.MirrorPoses {
		sort.Slice(layout.MirrorPoses[actorID], func(i, j int) bool {
			return layout.MirrorPoses[actorID][i].ID < layout.MirrorPoses[actorID][j].ID
		})
	}
	return layout, nil
}

type HomeController struct {
	layout        HomeLayout
	layoutSHA256  string
	now           func() time.Time
	slots         map[string]HomeSlot
	paths         map[string]HomePath
	roomOwner     map[string]string
	pathResources map[string]string
}

func NewHomeController(layout HomeLayout, now func() time.Time) (*HomeController, error) {
	layout, err := normalizeHomeLayout(layout)
	if err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(layout)
	if err != nil {
		return nil, fmt.Errorf("%w: canonical encode: %v", ErrHomeLayout, err)
	}
	sum := sha256.Sum256(canonical)
	if now == nil {
		now = time.Now
	}
	controller := &HomeController{
		layout: layout, layoutSHA256: hex.EncodeToString(sum[:]), now: now,
		slots: map[string]HomeSlot{}, paths: map[string]HomePath{}, roomOwner: map[string]string{}, pathResources: map[string]string{},
	}
	for actorID, roomID := range layout.PrivateRooms {
		controller.roomOwner[roomID] = actorID
	}
	for _, slot := range layout.Slots {
		controller.slots[slot.ID] = slot
	}
	for _, path := range layout.Paths {
		controller.paths[path.ID] = path
		controller.pathResources[path.ID] = pathGeometryResource(layout, path)
	}
	return controller, nil
}

func (c *HomeController) LayoutSHA256() string {
	if c == nil {
		return ""
	}
	return c.layoutSHA256
}

func (c *HomeController) RoomAnchor(roomID string) (Vec3, bool) {
	if c == nil {
		return Vec3{}, false
	}
	anchor, ok := c.layout.RoomAnchors[roomID]
	return anchor, ok
}

func (c *HomeController) SceneSHA256() string {
	if c == nil {
		return ""
	}
	return c.layout.SceneSHA256
}

// ResourceCapacities exposes the configured single-capacity edges and slots
// for initial Timeline creation. Production startup must verify the same IDs
// and capacities when reopening an existing Timeline.
func (c *HomeController) ResourceCapacities() map[string]int64 {
	resources := map[string]int64{}
	if c == nil {
		return resources
	}
	for _, path := range c.layout.Paths {
		resources[c.pathResources[path.ID]] = 1
	}
	for _, slot := range c.layout.Slots {
		resources[homeSlotResource(slot.ID)] = 1
	}
	return resources
}

// Register installs the closed home action vocabulary on the canonical Store
// Command -> Handler -> Event/Receipt path. The runtime decides whether to
// expose those actions through an authenticated route.
func (c *HomeController) Register(st *store.Store) {
	if c == nil || st == nil {
		return
	}
	st.Register(ActionHomeMove, c.handleMove)
	st.Register(ActionHomeSleep, c.handleSleep)
	st.Register(ActionHomeWake, c.handleWake)
	st.Register(ActionHomeExercise, c.handleExercise)
	st.Register(ActionHomeMirrorPose, c.handleMirrorPose)
	st.Register(ActionHomeFinish, c.handleFinish)
	st.Register(ActionHomeStop, c.handleStop)
}

func IsHomeAction(action string) bool {
	switch action {
	case ActionHomeMove, ActionHomeSleep, ActionHomeWake, ActionHomeExercise, ActionHomeMirrorPose, ActionHomeFinish, ActionHomeStop:
		return true
	default:
		return false
	}
}

// HomeCommandActorID extracts the actor scope claim for HTTP authorization.
// Full action-specific strict decoding still runs in the registered handler.
func HomeCommandActorID(action string, raw json.RawMessage) (string, error) {
	if !IsHomeAction(action) {
		return "", ErrHomePayload
	}
	if err := rejectHomeDuplicateKeys(raw); err != nil {
		return "", fmt.Errorf("%w: %v", ErrHomePayload, err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return "", fmt.Errorf("%w: payload must be an object", ErrHomePayload)
	}
	var actorID string
	if err := json.Unmarshal(object["actor_id"], &actorID); err != nil || !validHomeID(actorID) {
		return "", fmt.Errorf("%w: actor_id is required", ErrHomePayload)
	}
	return actorID, nil
}

type homeMoveCommand struct {
	ActorID string `json:"actor_id"`
	RoomID  string `json:"room_id"`
	SlotID  string `json:"slot_id,omitempty"`
}

type homeActorCommand struct {
	ActorID string `json:"actor_id"`
}

type homeActivityCommand struct {
	ActorID    string `json:"actor_id"`
	ActivityID string `json:"activity_id"`
}

type homePoseCommand struct {
	ActorID string `json:"actor_id"`
	PoseID  string `json:"pose_id"`
}

type homeEvent struct {
	ActorID               string  `json:"actor_id"`
	ActivityID            string  `json:"activity_id"`
	ActivityKind          string  `json:"activity_kind"`
	PoseID                string  `json:"pose_id,omitempty"`
	RoomID                string  `json:"room_id,omitempty"`
	FromRoomID            string  `json:"from_room_id,omitempty"`
	TargetRoomID          string  `json:"target_room_id,omitempty"`
	SlotID                string  `json:"slot_id,omitempty"`
	RouteID               string  `json:"route_id,omitempty"`
	LayoutSHA256          string  `json:"layout_sha256"`
	SceneSHA256           string  `json:"scene_sha256"`
	StartedAt             string  `json:"started_at"`
	DueAt                 string  `json:"due_at,omitempty"`
	EndedAt               string  `json:"ended_at,omitempty"`
	ElapsedMS             *int64  `json:"elapsed_ms,omitempty"`
	MirrorPoseCompletions *uint64 `json:"mirror_pose_completions,omitempty"`
}

func (c *HomeController) handleMove(state store.State, raw json.RawMessage) (store.Outcome, error) {
	var cmd homeMoveCommand
	if err := decodeHomePayload(raw, &cmd); err != nil || !validHomeID(cmd.ActorID) || !validHomeID(cmd.RoomID) || (cmd.SlotID != "" && !validHomeID(cmd.SlotID)) {
		return store.Outcome{}, ErrHomePayload
	}
	actor, ok := state.Actors[cmd.ActorID]
	if !ok {
		return store.Outcome{}, fmt.Errorf("%w: actor=%q", ErrHomeActorUnknown, cmd.ActorID)
	}
	if actor.ActivityID != "" {
		return store.Outcome{}, fmt.Errorf("%w: activity=%q", ErrHomeActivityActive, actor.ActivityID)
	}
	if _, ok := c.layout.RoomAnchors[cmd.RoomID]; !ok {
		return store.Outcome{}, fmt.Errorf("%w: room=%q", ErrHomeRoomUnknown, cmd.RoomID)
	}
	if owner := c.roomOwner[cmd.RoomID]; owner != "" && owner != cmd.ActorID {
		return store.Outcome{}, ErrHomePrivateRoom
	}
	route, err := c.resolveRoute(cmd.ActorID, actor, cmd.RoomID, cmd.SlotID)
	if err != nil {
		return store.Outcome{}, err
	}
	if slot := c.slots[route.targetSlotID]; slot.Kind == HomeSlotRest {
		// The route reservation covers incoming moves. A committed arrival
		// retains the seat until the occupant's departure commits.
		for id, occupant := range state.Actors {
			if id != cmd.ActorID && occupant.SlotID == slot.ID {
				return store.Outcome{}, ErrHomeSeatOccupied
			}
		}
	}
	now := c.now().UTC()
	return c.startActivity(state, cmd.ActorID, HomeActivityMove, "", route.targetSlotID, route.durationMS, now, EventHomeMoveStarted, func(event *homeEvent) {
		event.RoomID = actor.RoomID
		event.FromRoomID = actor.RoomID
		event.TargetRoomID = cmd.RoomID
		event.SlotID = route.targetSlotID
		event.RouteID = route.routeID
	}, func(updated *store.ActorState) {
		updated.TargetRoomID = cmd.RoomID
		updated.TargetSlotID = route.targetSlotID
		updated.MovePhase = "in_transit"
		updated.MoveRouteID = route.routeID
		updated.MovePathIDs = encodePathIDs(route.pathIDs)
		updated.MoveResourceIDs = encodePathIDs(route.resourceIDs)
	}, func(activityID string) []store.Reservation {
		reservations := make([]store.Reservation, 0, len(route.resourceIDs))
		for _, resourceID := range route.resourceIDs {
			reservations = append(reservations, store.Reservation{
				ResourceID: resourceID, ActorID: cmd.ActorID, ActivityID: activityID, Quantity: 1,
			})
		}
		return reservations
	})
}

func (c *HomeController) handleSleep(state store.State, raw json.RawMessage) (store.Outcome, error) {
	var cmd homeActorCommand
	if err := decodeHomePayload(raw, &cmd); err != nil || !validHomeID(cmd.ActorID) {
		return store.Outcome{}, ErrHomePayload
	}
	slot, actor, err := c.actorAtSlot(state, cmd.ActorID, HomeSlotBed)
	if err != nil {
		return store.Outcome{}, err
	}
	now := c.now().UTC()
	return c.startActivity(state, cmd.ActorID, HomeActivitySleep, "", slot.ID, 0, now, EventHomeSleepStarted, func(event *homeEvent) {
		event.RoomID = actor.RoomID
		event.SlotID = slot.ID
	}, nil, func(activityID string) []store.Reservation {
		return []store.Reservation{{ResourceID: homeSlotResource(slot.ID), ActorID: cmd.ActorID, ActivityID: activityID, Quantity: 1}}
	})
}

func (c *HomeController) handleWake(state store.State, raw json.RawMessage) (store.Outcome, error) {
	var cmd homeActivityCommand
	if err := decodeHomePayload(raw, &cmd); err != nil || !validHomeID(cmd.ActorID) || !validHomeID(cmd.ActivityID) {
		return store.Outcome{}, ErrHomePayload
	}
	actor, activity, err := c.activeActivity(state, cmd.ActorID, cmd.ActivityID)
	if err != nil {
		return store.Outcome{}, err
	}
	if activity.Kind != HomeActivitySleep || actor.ActivityKind != HomeActivitySleep {
		return store.Outcome{}, ErrHomeSleepUseWake
	}
	now := c.now().UTC()
	startedAt, err := parseHomeTime(activity.StartedAt)
	if err != nil || now.Before(startedAt) {
		return store.Outcome{}, ErrHomeClockReversed
	}
	endedAt := formatHomeTime(now)
	elapsed := now.Sub(startedAt).Milliseconds()
	after := copyState(state)
	updated := after.Actors[cmd.ActorID]
	clearHomeActivity(&updated)
	after.Actors[cmd.ActorID] = updated
	ended := activity
	ended.Status = HomeActivityStatusWoke
	ended.EndedAt = endedAt
	after.Activities[cmd.ActivityID] = ended
	event := homeEvent{
		ActorID: cmd.ActorID, ActivityID: cmd.ActivityID, ActivityKind: HomeActivitySleep,
		RoomID: actor.RoomID, SlotID: actor.SlotID, LayoutSHA256: activity.LayoutSHA256, SceneSHA256: activity.SceneSHA256,
		StartedAt: activity.StartedAt, EndedAt: endedAt, ElapsedMS: &elapsed,
	}
	return store.Outcome{
		State:   after,
		Events:  []store.EventDraft{{Kind: EventHomeSleepWoke, Payload: marshalHomeEvent(event)}},
		Release: []store.Reservation{{ResourceID: homeSlotResource(actor.SlotID), ActorID: cmd.ActorID, ActivityID: cmd.ActivityID, Quantity: 1}},
	}, nil
}

func (c *HomeController) handleExercise(state store.State, raw json.RawMessage) (store.Outcome, error) {
	var cmd homeActorCommand
	if err := decodeHomePayload(raw, &cmd); err != nil || !validHomeID(cmd.ActorID) {
		return store.Outcome{}, ErrHomePayload
	}
	slot, actor, err := c.actorAtSlot(state, cmd.ActorID, HomeSlotExercise)
	if err != nil {
		return store.Outcome{}, err
	}
	now := c.now().UTC()
	return c.startActivity(state, cmd.ActorID, HomeActivityExercise, "", slot.ID, slot.DurationMS, now, EventHomeExerciseStarted, func(event *homeEvent) {
		event.RoomID = actor.RoomID
		event.SlotID = slot.ID
	}, nil, func(activityID string) []store.Reservation {
		return []store.Reservation{{ResourceID: homeSlotResource(slot.ID), ActorID: cmd.ActorID, ActivityID: activityID, Quantity: 1}}
	})
}

func (c *HomeController) handleMirrorPose(state store.State, raw json.RawMessage) (store.Outcome, error) {
	var cmd homePoseCommand
	if err := decodeHomePayload(raw, &cmd); err != nil || !validHomeID(cmd.ActorID) || !validHomeID(cmd.PoseID) {
		return store.Outcome{}, ErrHomePayload
	}
	var pose MirrorPose
	found := false
	for _, candidate := range c.layout.MirrorPoses[cmd.ActorID] {
		if candidate.ID == cmd.PoseID {
			pose = candidate
			found = true
			break
		}
	}
	if !found {
		return store.Outcome{}, ErrHomePoseNotAllowed
	}
	slot, ok := c.slots[pose.SlotID]
	if !ok || (slot.ActorID != "" && slot.ActorID != cmd.ActorID) {
		return store.Outcome{}, ErrHomePoseNotAllowed
	}
	actor, ok := state.Actors[cmd.ActorID]
	if !ok {
		return store.Outcome{}, ErrHomeActorUnknown
	}
	if err := c.actorAtConfiguredSlot(cmd.ActorID, actor, slot); err != nil {
		return store.Outcome{}, err
	}
	now := c.now().UTC()
	return c.startActivity(state, cmd.ActorID, HomeActivityMirrorPose, cmd.PoseID, slot.ID, pose.DurationMS, now, EventHomeMirrorPoseStarted, func(event *homeEvent) {
		event.RoomID = actor.RoomID
		event.SlotID = slot.ID
		event.PoseID = cmd.PoseID
	}, nil, func(activityID string) []store.Reservation {
		return []store.Reservation{{ResourceID: homeSlotResource(slot.ID), ActorID: cmd.ActorID, ActivityID: activityID, Quantity: 1}}
	})
}

func (c *HomeController) handleFinish(state store.State, raw json.RawMessage) (store.Outcome, error) {
	var cmd homeActivityCommand
	if err := decodeHomePayload(raw, &cmd); err != nil || !validHomeID(cmd.ActorID) || !validHomeID(cmd.ActivityID) {
		return store.Outcome{}, ErrHomePayload
	}
	actor, activity, err := c.activeActivity(state, cmd.ActorID, cmd.ActivityID)
	if err != nil {
		return store.Outcome{}, err
	}
	if activity.Kind == HomeActivitySleep {
		return store.Outcome{}, ErrHomeSleepUseWake
	}
	if actor.LayoutSHA256 != c.layoutSHA256 || activity.LayoutSHA256 != "" && activity.LayoutSHA256 != c.layoutSHA256 {
		return store.Outcome{}, ErrHomeLayoutChanged
	}
	now := c.now().UTC()
	startedAt, err := parseHomeTime(activity.StartedAt)
	if err != nil || now.Before(startedAt) {
		return store.Outcome{}, ErrHomeClockReversed
	}
	dueAt, err := parseHomeTime(activity.DueAt)
	if err != nil || now.Before(dueAt) {
		return store.Outcome{}, ErrHomeNotDue
	}
	endedAt := formatHomeTime(now)
	elapsed := now.Sub(startedAt).Milliseconds()
	after := copyState(state)
	updated := after.Actors[cmd.ActorID]
	updatedActivity := after.Activities[cmd.ActivityID]
	updatedActivity.Status = HomeActivityStatusCompleted
	updatedActivity.EndedAt = endedAt
	after.Activities[cmd.ActivityID] = updatedActivity
	kind := ""
	roomID := actor.RoomID
	targetSlotID := actor.TargetSlotID
	var release []store.Reservation
	event := homeEvent{
		ActorID: cmd.ActorID, ActivityID: cmd.ActivityID, ActivityKind: activity.Kind,
		PoseID: activity.PoseID, RoomID: actor.RoomID, FromRoomID: actor.RoomID,
		TargetRoomID: actor.TargetRoomID, SlotID: actor.SlotID,
		RouteID: actor.MoveRouteID, LayoutSHA256: c.layoutSHA256, SceneSHA256: c.layout.SceneSHA256,
		StartedAt: activity.StartedAt, DueAt: activity.DueAt, EndedAt: endedAt, ElapsedMS: &elapsed,
	}
	switch activity.Kind {
	case HomeActivityMove:
		if actor.MovePhase != "in_transit" || actor.TargetRoomID == "" || actor.MoveRouteID == "" {
			return store.Outcome{}, ErrHomeActivityHeld
		}
		points, err := c.MovePoints(state, cmd.ActorID)
		if err != nil || len(points) < 2 {
			return store.Outcome{}, ErrHomePosition
		}
		roomID = actor.TargetRoomID
		updated.RoomID = roomID
		destination := c.layout.RoomAnchors[roomID]
		if targetSlotID != "" {
			destination = c.slots[targetSlotID].Anchor
		}
		position := [3]float64(destination)
		updated.Position = &position
		updated.SlotID = targetSlotID
		event.SlotID = targetSlotID
		kind = EventHomeMoveArrived
		release = c.pathReservations(cmd.ActorID, cmd.ActivityID, actor.MoveResourceIDs)
	case HomeActivityExercise:
		kind = EventHomeExerciseCompleted
		release = []store.Reservation{{ResourceID: homeSlotResource(actor.SlotID), ActorID: cmd.ActorID, ActivityID: cmd.ActivityID, Quantity: 1}}
	case HomeActivityMirrorPose:
		kind = EventHomeMirrorPoseCompleted
		if updated.MirrorPoseCompletions == math.MaxUint64 {
			return store.Outcome{}, ErrHomeLayout
		}
		updated.MirrorPoseCompletions++
		count := updated.MirrorPoseCompletions
		event.MirrorPoseCompletions = &count
		release = []store.Reservation{{ResourceID: homeSlotResource(actor.SlotID), ActorID: cmd.ActorID, ActivityID: cmd.ActivityID, Quantity: 1}}
	default:
		return store.Outcome{}, ErrHomeActivityHeld
	}
	event.RoomID = roomID
	clearHomeActivity(&updated)
	after.Actors[cmd.ActorID] = updated
	return store.Outcome{State: after, Events: []store.EventDraft{{Kind: kind, Payload: marshalHomeEvent(event)}}, Release: release}, nil
}

func (c *HomeController) handleStop(state store.State, raw json.RawMessage) (store.Outcome, error) {
	var cmd homeActivityCommand
	if err := decodeHomePayload(raw, &cmd); err != nil || !validHomeID(cmd.ActorID) || !validHomeID(cmd.ActivityID) {
		return store.Outcome{}, ErrHomePayload
	}
	actor, activity, err := c.activeActivity(state, cmd.ActorID, cmd.ActivityID)
	if err != nil {
		return store.Outcome{}, err
	}
	if activity.Kind == HomeActivitySleep {
		return store.Outcome{}, ErrHomeNotInterruptible
	}
	if activity.Kind != HomeActivityMove && activity.Kind != HomeActivityExercise && activity.Kind != HomeActivityMirrorPose {
		return store.Outcome{}, ErrHomeNotInterruptible
	}
	now := c.now().UTC()
	startedAt, err := parseHomeTime(activity.StartedAt)
	if err != nil || now.Before(startedAt) {
		return store.Outcome{}, ErrHomeClockReversed
	}
	endedAt := formatHomeTime(now)
	elapsed := now.Sub(startedAt).Milliseconds()
	after := copyState(state)
	updated := after.Actors[cmd.ActorID]
	updatedActivity := after.Activities[cmd.ActivityID]
	updatedActivity.Status = HomeActivityStatusStopped
	updatedActivity.EndedAt = endedAt
	after.Activities[cmd.ActivityID] = updatedActivity
	event := homeEvent{
		ActorID: cmd.ActorID, ActivityID: cmd.ActivityID, ActivityKind: activity.Kind,
		PoseID: activity.PoseID, RoomID: actor.RoomID, FromRoomID: actor.RoomID,
		TargetRoomID: actor.TargetRoomID, SlotID: actor.SlotID,
		RouteID: actor.MoveRouteID, LayoutSHA256: activity.LayoutSHA256, SceneSHA256: activity.SceneSHA256,
		StartedAt: activity.StartedAt, DueAt: activity.DueAt, EndedAt: endedAt, ElapsedMS: &elapsed,
	}
	var release []store.Reservation
	switch activity.Kind {
	case HomeActivityMove:
		release = c.pathReservations(cmd.ActorID, cmd.ActivityID, actor.MoveResourceIDs)
	case HomeActivityExercise, HomeActivityMirrorPose:
		release = []store.Reservation{{ResourceID: homeSlotResource(actor.SlotID), ActorID: cmd.ActorID, ActivityID: cmd.ActivityID, Quantity: 1}}
	}
	clearHomeActivity(&updated)
	after.Actors[cmd.ActorID] = updated
	return store.Outcome{State: after, Events: []store.EventDraft{{Kind: EventHomeActivityStopped, Payload: marshalHomeEvent(event)}}, Release: release}, nil
}

func (c *HomeController) startActivity(state store.State, actorID, kind, poseID, slotID string, durationMS int64, now time.Time, eventKind string,
	fillEvent func(*homeEvent), updateActor func(*store.ActorState), reservations func(string) []store.Reservation) (store.Outcome, error) {
	actor, ok := state.Actors[actorID]
	if !ok {
		return store.Outcome{}, fmt.Errorf("%w: actor=%q", ErrHomeActorUnknown, actorID)
	}
	if actor.ActivityID != "" {
		return store.Outcome{}, fmt.Errorf("%w: activity=%q", ErrHomeActivityActive, actor.ActivityID)
	}
	if actor.ActivitySequence == math.MaxUint64 {
		return store.Outcome{}, ErrHomeLayout
	}
	activityID := homeActivityID(actorID, actor.ActivitySequence+1)
	startedAt := formatHomeTime(now)
	dueAt := ""
	if durationMS > 0 {
		dueAt = formatHomeTime(now.Add(time.Duration(durationMS) * time.Millisecond))
	}
	after := copyState(state)
	updated := after.Actors[actorID]
	updated.ActivitySequence++
	updated.ActivityID = activityID
	updated.ActivityKind = kind
	updated.PoseID = poseID
	updated.ActivityStartedAt = startedAt
	updated.ActivityDueAt = dueAt
	updated.LayoutSHA256 = c.layoutSHA256
	updated.HomeSceneSHA256 = c.layout.SceneSHA256
	if slotID != "" && kind != HomeActivityMove {
		updated.SlotID = slotID
	}
	if updateActor != nil {
		updateActor(&updated)
	}
	after.Actors[actorID] = updated
	activity := store.ActivityState{
		ID: activityID, ActorID: actorID, Status: HomeActivityStatusRunning,
		Kind: kind, PoseID: poseID, StartedAt: startedAt, DueAt: dueAt, LayoutSHA256: c.layoutSHA256, SceneSHA256: c.layout.SceneSHA256,
	}
	after.Activities[activityID] = activity
	event := homeEvent{
		ActorID: actorID, ActivityID: activityID, ActivityKind: kind, PoseID: poseID,
		SlotID: slotID, LayoutSHA256: c.layoutSHA256, SceneSHA256: c.layout.SceneSHA256, StartedAt: startedAt, DueAt: dueAt,
	}
	if fillEvent != nil {
		fillEvent(&event)
	}
	var reserve []store.Reservation
	if reservations != nil {
		reserve = reservations(activityID)
	}
	return store.Outcome{State: after, Events: []store.EventDraft{{Kind: eventKind, Payload: marshalHomeEvent(event)}}, Reserve: reserve}, nil
}

func (c *HomeController) activeActivity(state store.State, actorID, activityID string) (store.ActorState, store.ActivityState, error) {
	actor, ok := state.Actors[actorID]
	if !ok {
		return store.ActorState{}, store.ActivityState{}, ErrHomeActorUnknown
	}
	if actor.ActivityID == "" || actor.ActivityID != activityID {
		return store.ActorState{}, store.ActivityState{}, ErrHomeActivityHeld
	}
	activity, ok := state.Activities[activityID]
	if !ok || activity.ActorID != actorID || activity.Status != HomeActivityStatusRunning || actor.ActivityKind != activity.Kind {
		return store.ActorState{}, store.ActivityState{}, ErrHomeActivityHeld
	}
	return actor, activity, nil
}

func (c *HomeController) actorAtSlot(state store.State, actorID, kind string) (HomeSlot, store.ActorState, error) {
	actor, ok := state.Actors[actorID]
	if !ok {
		return HomeSlot{}, store.ActorState{}, ErrHomeActorUnknown
	}
	if actor.ActivityID != "" {
		return HomeSlot{}, store.ActorState{}, ErrHomeActivityActive
	}
	if actor.Position == nil {
		return HomeSlot{}, store.ActorState{}, ErrHomePosition
	}
	matches := make([]HomeSlot, 0, 1)
	for _, slot := range c.layout.Slots {
		if slot.Kind != kind || (slot.ActorID != "" && slot.ActorID != actorID) || !sameVec3(Vec3(*actor.Position), slot.Anchor) || actor.RoomID != slot.RoomID {
			continue
		}
		if owner := c.roomOwner[slot.RoomID]; owner != "" && owner != actorID {
			continue
		}
		matches = append(matches, slot)
	}
	if len(matches) != 1 {
		return HomeSlot{}, store.ActorState{}, ErrHomeWrongSlot
	}
	return matches[0], actor, nil
}

type homeRoute struct {
	targetSlotID string
	pathIDs      []string
	resourceIDs  []string
	points       []Vec3
	durationMS   int64
	routeID      string
}

func (c *HomeController) resolveRoute(actorID string, actor store.ActorState, targetRoomID, targetSlotID string) (homeRoute, error) {
	if actor.Position == nil {
		return homeRoute{}, ErrHomePosition
	}
	fromAnchor, ok := c.layout.RoomAnchors[actor.RoomID]
	if !ok {
		return homeRoute{}, ErrHomeRoomUnknown
	}
	if owner := c.roomOwner[actor.RoomID]; owner != "" && owner != actorID {
		return homeRoute{}, ErrHomePrivateRoom
	}
	current := Vec3(*actor.Position)
	var fromSlot *HomeSlot
	if actor.SlotID != "" {
		slot, found := c.slots[actor.SlotID]
		if !found || slot.RoomID != actor.RoomID || slot.Anchor != current || (slot.ActorID != "" && slot.ActorID != actorID) {
			return homeRoute{}, ErrHomePosition
		}
		fromSlot = &slot
	} else if current != fromAnchor {
		for _, slot := range c.layout.Slots {
			if slot.RoomID == actor.RoomID && slot.Anchor == current && (slot.ActorID == "" || slot.ActorID == actorID) {
				if fromSlot != nil {
					return homeRoute{}, ErrHomePosition
				}
				copySlot := slot
				fromSlot = &copySlot
			}
		}
		if fromSlot == nil {
			return homeRoute{}, ErrHomePosition
		}
	}
	var targetSlot *HomeSlot
	if targetSlotID != "" {
		slot, found := c.slots[targetSlotID]
		if !found || slot.RoomID != targetRoomID || (slot.ActorID != "" && slot.ActorID != actorID) {
			return homeRoute{}, ErrHomeWrongSlot
		}
		if owner := c.roomOwner[slot.RoomID]; owner != "" && owner != actorID {
			return homeRoute{}, ErrHomePrivateRoom
		}
		targetSlot = &slot
	}
	edges, err := c.shortestRoomPath(actorID, actor.RoomID, targetRoomID)
	if err != nil {
		return homeRoute{}, err
	}
	durationMS := int64(0)
	pathIDs := make([]string, 0, len(edges))
	points := []Vec3{current}
	if fromSlot != nil && current != fromAnchor {
		reversed := reverseVec3(fromSlot.ApproachPoints)
		appendVec3s(&points, reversed)
		appendVec3(&points, fromAnchor)
		durationMS += fromSlot.ApproachDurationMS
	}
	resourceIDs := make([]string, 0, len(edges))
	for _, edge := range edges {
		pathIDs = append(pathIDs, edge.ID)
		appendVec3s(&points, edge.Points)
		appendVec3(&points, c.layout.RoomAnchors[edge.ToRoomID])
		durationMS += edge.DurationMS
	}
	if targetSlot != nil && targetSlot.Anchor != c.layout.RoomAnchors[targetRoomID] {
		appendVec3s(&points, targetSlot.ApproachPoints)
		appendVec3(&points, targetSlot.Anchor)
		durationMS += targetSlot.ApproachDurationMS
	}
	if targetSlot != nil {
		resourceIDs = append(resourceIDs, homeSlotResource(targetSlot.ID))
	}
	target := c.layout.RoomAnchors[targetRoomID]
	if targetSlot != nil {
		target = targetSlot.Anchor
	}
	if current == target && len(points) == 1 {
		return homeRoute{}, ErrHomeAlreadyThere
	}
	if durationMS <= 0 || durationMS > maxHomeDurationMS {
		return homeRoute{}, fmt.Errorf("%w: total route duration is outside bounds", ErrHomeLayout)
	}
	pathJSON, _ := json.Marshal(pathIDs)
	routeIdentity, _ := json.Marshal(struct {
		LayoutSHA256 string `json:"layout_sha256"`
		ActorID      string `json:"actor_id"`
		FromRoomID   string `json:"from_room_id"`
		TargetRoomID string `json:"target_room_id"`
		FromSlotID   string `json:"from_slot_id"`
		TargetSlotID string `json:"target_slot_id"`
		Position     Vec3   `json:"position"`
		PathIDs      string `json:"path_ids"`
	}{c.layoutSHA256, actorID, actor.RoomID, targetRoomID, actor.SlotID, targetSlotID, current, string(pathJSON)})
	checksum := sha256.Sum256(routeIdentity)
	return homeRoute{
		targetSlotID: targetSlotID, pathIDs: pathIDs, resourceIDs: resourceIDs, points: points, durationMS: durationMS,
		routeID: hex.EncodeToString(checksum[:]),
	}, nil
}

func (c *HomeController) shortestRoomPath(actorID, from, to string) ([]HomePath, error) {
	if from == to {
		return []HomePath{}, nil
	}
	if _, ok := c.layout.RoomAnchors[from]; !ok {
		return nil, ErrHomeRoomUnknown
	}
	if _, ok := c.layout.RoomAnchors[to]; !ok {
		return nil, ErrHomeRoomUnknown
	}
	type routeNode struct {
		room     string
		distance float64
		key      string
		path     []HomePath
	}
	best := map[string]routeNode{from: {room: from, distance: 0}}
	visited := map[string]bool{}
	for range c.layout.RoomAnchors {
		currentRoom := ""
		var current routeNode
		for room, candidate := range best {
			if visited[room] {
				continue
			}
			if currentRoom == "" || candidate.distance < current.distance-1e-9 ||
				(math.Abs(candidate.distance-current.distance) <= 1e-9 && candidate.key < current.key) {
				currentRoom, current = room, candidate
			}
		}
		if currentRoom == "" {
			break
		}
		if currentRoom == to {
			return current.path, nil
		}
		visited[currentRoom] = true
		for _, edge := range c.layout.Paths {
			if edge.FromRoomID != currentRoom || visited[edge.ToRoomID] {
				continue
			}
			if owner := c.roomOwner[edge.ToRoomID]; owner != "" && owner != actorID {
				continue
			}
			distance := current.distance + pathDistance(c.layout.RoomAnchors[edge.FromRoomID], edge.Points, c.layout.RoomAnchors[edge.ToRoomID])
			key := edge.ID
			if current.key != "" {
				key = current.key + "\x00" + edge.ID
			}
			old, exists := best[edge.ToRoomID]
			if exists && (old.distance < distance-1e-9 || (math.Abs(old.distance-distance) <= 1e-9 && old.key <= key)) {
				continue
			}
			path := append(append([]HomePath(nil), current.path...), edge)
			best[edge.ToRoomID] = routeNode{room: edge.ToRoomID, distance: distance, key: key, path: path}
		}
	}
	return nil, ErrHomeNoRoute
}

// MovePoints returns the owner's measured route for one active movement only
// when the current actor state is pinned to this exact immutable layout hash.
// A public runtime may expose a separately authorized, typed read projection.
func (c *HomeController) MovePoints(state store.State, actorID string) ([]Vec3, error) {
	actor, ok := state.Actors[actorID]
	if !ok || actor.ActivityKind != HomeActivityMove || actor.ActivityID == "" {
		return nil, ErrHomeActivityHeld
	}
	if actor.LayoutSHA256 != c.layoutSHA256 {
		return nil, ErrHomeLayoutChanged
	}
	var pathIDs []string
	if err := json.Unmarshal([]byte(actor.MovePathIDs), &pathIDs); err != nil {
		return nil, ErrHomePosition
	}
	for _, id := range pathIDs {
		if _, ok := c.paths[id]; !ok {
			return nil, ErrHomeLayoutChanged
		}
	}
	targetSlotID := actor.TargetSlotID
	route, err := c.resolveRouteWithPathIDs(actorID, actor, actor.TargetRoomID, targetSlotID, pathIDs)
	if err != nil || route.routeID != actor.MoveRouteID {
		return nil, ErrHomeLayoutChanged
	}
	return append([]Vec3(nil), route.points...), nil
}

func (c *HomeController) resolveRouteWithPathIDs(actorID string, actor store.ActorState, targetRoomID, targetSlotID string, pathIDs []string) (homeRoute, error) {
	// Resolve the same deterministic route from immutable layout and persisted
	// actor fields, then require its graph edge sequence to match the start Event.
	route, err := c.resolveRoute(actorID, actor, targetRoomID, targetSlotID)
	if err != nil {
		return homeRoute{}, err
	}
	if encodePathIDs(route.pathIDs) != encodePathIDs(pathIDs) {
		return homeRoute{}, ErrHomeLayoutChanged
	}
	return route, nil
}

func (c *HomeController) pathReservations(actorID, activityID, encodedResourceIDs string) []store.Reservation {
	var resourceIDs []string
	if err := json.Unmarshal([]byte(encodedResourceIDs), &resourceIDs); err != nil {
		return nil
	}
	reservations := make([]store.Reservation, 0, len(resourceIDs))
	for _, resourceID := range resourceIDs {
		reservations = append(reservations, store.Reservation{
			ResourceID: resourceID, ActorID: actorID, ActivityID: activityID, Quantity: 1,
		})
	}
	return reservations
}

func (c *HomeController) actorAtConfiguredSlot(actorID string, actor store.ActorState, slot HomeSlot) error {
	if slot.ActorID != "" && slot.ActorID != actorID {
		return ErrHomeWrongSlot
	}
	if owner := c.roomOwner[slot.RoomID]; owner != "" && owner != actorID {
		return ErrHomePrivateRoom
	}
	if actor.RoomID != slot.RoomID || actor.Position == nil || Vec3(*actor.Position) != slot.Anchor {
		return ErrHomeWrongSlot
	}
	return nil
}

func clearHomeActivity(actor *store.ActorState) {
	actor.ActivityID = ""
	actor.ActivityKind = ""
	actor.PoseID = ""
	actor.ActivityStartedAt = ""
	actor.ActivityDueAt = ""
	actor.TargetRoomID = ""
	actor.TargetSlotID = ""
	actor.MovePhase = ""
	actor.MoveRouteID = ""
	actor.MovePathIDs = ""
	actor.MoveResourceIDs = ""
	actor.LayoutSHA256 = ""
	actor.HomeSceneSHA256 = ""
}

func homeActivityID(actorID string, sequence uint64) string {
	return fmt.Sprintf("home:%d:%s:%d", len(actorID), actorID, sequence)
}

func homeSlotResource(id string) string { return "home-slot:" + id }

func pathGeometryResource(layout HomeLayout, path HomePath) string {
	fromRoom, toRoom := path.FromRoomID, path.ToRoomID
	points := append([]Vec3(nil), path.Points...)
	if fromRoom > toRoom {
		fromRoom, toRoom = toRoom, fromRoom
		points = reverseVec3(points)
	}
	polyline := make([]Vec3, 0, len(points)+2)
	polyline = append(polyline, layout.RoomAnchors[fromRoom])
	polyline = append(polyline, points...)
	polyline = append(polyline, layout.RoomAnchors[toRoom])
	canonical, _ := json.Marshal(struct {
		FromRoomID string `json:"from_room_id"`
		ToRoomID   string `json:"to_room_id"`
		Points     []Vec3 `json:"points"`
	}{fromRoom, toRoom, polyline})
	sum := sha256.Sum256(canonical)
	return "home-path:" + hex.EncodeToString(sum[:])
}

func encodePathIDs(ids []string) string {
	raw, err := json.Marshal(ids)
	if err != nil {
		return "[]"
	}
	return string(raw)
}

func formatHomeTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseHomeTime(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, ErrHomePayload
	}
	value, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, err
	}
	return value.UTC(), nil
}

func marshalHomeEvent(value homeEvent) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		panic("life home event is not serializable: " + err.Error())
	}
	return raw
}

func decodeHomePayload(raw json.RawMessage, target any) error {
	if len(raw) == 0 || !json.Valid(raw) {
		return ErrHomePayload
	}
	if err := rejectHomeDuplicateKeys(raw); err != nil {
		return fmt.Errorf("%w: %v", ErrHomePayload, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: %v", ErrHomePayload, err)
	}
	if err := decoder.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing JSON value", ErrHomePayload)
	}
	return nil
}

func rejectHomeDuplicateKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok || seen[key] {
					return errors.New("duplicate or invalid JSON object key")
				}
				seen[key] = true
				if err := walk(); err != nil {
					return err
				}
			}
			_, err := decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err := decoder.Token()
			return err
		default:
			return errors.New("unexpected JSON delimiter")
		}
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func cloneHomeLayout(layout HomeLayout) HomeLayout {
	copyLayout := layout
	copyLayout.RoomAnchors = make(map[string]Vec3, len(layout.RoomAnchors))
	for id, anchor := range layout.RoomAnchors {
		copyLayout.RoomAnchors[id] = anchor
	}
	copyLayout.PrivateRooms = make(map[string]string, len(layout.PrivateRooms))
	for actor, room := range layout.PrivateRooms {
		copyLayout.PrivateRooms[actor] = room
	}
	copyLayout.Paths = append([]HomePath(nil), layout.Paths...)
	for i := range copyLayout.Paths {
		copyLayout.Paths[i].Points = append([]Vec3(nil), layout.Paths[i].Points...)
	}
	copyLayout.Slots = append([]HomeSlot(nil), layout.Slots...)
	for i := range copyLayout.Slots {
		copyLayout.Slots[i].ApproachPoints = append([]Vec3(nil), layout.Slots[i].ApproachPoints...)
	}
	copyLayout.MirrorPoses = make(map[string][]MirrorPose, len(layout.MirrorPoses))
	for actor, poses := range layout.MirrorPoses {
		copyLayout.MirrorPoses[actor] = append([]MirrorPose(nil), poses...)
	}
	return copyLayout
}

func validHomeID(value string) bool {
	if value == "" || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func finiteVec3(value Vec3) bool {
	for _, coordinate := range value {
		if math.IsNaN(coordinate) || math.IsInf(coordinate, 0) {
			return false
		}
	}
	return true
}

func sameVec3(a, b Vec3) bool { return a == b }

func pathDistance(from Vec3, intermediate []Vec3, to Vec3) float64 {
	distance := 0.0
	previous := from
	for _, point := range intermediate {
		distance += vecDistance(previous, point)
		previous = point
	}
	return distance + vecDistance(previous, to)
}

func vecDistance(a, b Vec3) float64 {
	dx := a[0] - b[0]
	dy := a[1] - b[1]
	dz := a[2] - b[2]
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

func appendVec3(points *[]Vec3, point Vec3) {
	if len(*points) == 0 || (*points)[len(*points)-1] != point {
		*points = append(*points, point)
	}
}

func appendVec3s(points *[]Vec3, values []Vec3) {
	for _, point := range values {
		appendVec3(points, point)
	}
}

func reverseVec3(values []Vec3) []Vec3 {
	reversed := make([]Vec3, len(values))
	for i := range values {
		reversed[len(values)-1-i] = values[i]
	}
	return reversed
}
