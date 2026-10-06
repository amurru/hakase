// adk_service.go - durable ADK session.Service over JSON files.
//
// The classic Runner's resume path (run_node.go:179-194) reconstructs paused
// HITL state from session history, but hakase hands the runner
// session.InMemoryService(), so all ADK-side history evaporates on restart.
// This service implements the same adksession.Service contract over JSON
// files under <sessionsDir>/adk/, so event history (including
// LongRunningToolIDs, RequestedInput, NodeInfo, InvocationID) survives a
// process restart. Part of durable resume (docs/durable-resume/plan.md,
// Phase 2). Opt-in via config durable_resume.enabled; off = in-memory.
//
// Writer rules mirror SessionStore: 0700 dir / 0600 files, tmp+rename
// atomic saves, cross-process flock during every read and every
// read-modify-write. State scoping (app:/user:/temp: prefixes) mirrors the
// engine's in-memory service exactly, quirks included; the shared
// conformance suite (sessiontestsuite) is the arbiter.
package session

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"amurru/hakase/internal/util"

	adksession "google.golang.org/adk/v2/session"
)

// adkSubdir is the directory under the hakase sessions dir holding ADK
// engine state. Separate from the hakase *.json session files so the two
// stores never collide.
const adkSubdir = "adk"

// adkSessionRecord is the on-disk form of one ADK session: the
// session-scoped state plus the full event history. App/user-scoped state
// lives in adkGlobalState (mirroring the engine's in-memory maps).
type adkSessionRecord struct {
	AppName   string              `json:"app_name"`
	UserID    string              `json:"user_id"`
	SessionID string              `json:"session_id"`
	State     map[string]any      `json:"state,omitempty"`
	Events    []*adksession.Event `json:"events,omitempty"`
	UpdatedAt time.Time           `json:"updated_at"`
}

// adkGlobalState persists the engine's app/user state maps: keys are
// stored stripped of their app:/user: prefixes (see extractADKDeltas),
// re-prefixed on merge (see mergeADKStates).
type adkGlobalState struct {
	Apps  map[string]map[string]any            `json:"apps,omitempty"`
	Users map[string]map[string]map[string]any `json:"users,omitempty"`
}

// DurableADKService implements adksession.Service over JSON files.
// Safe for concurrent use; cross-process safe via the dir flock.
type DurableADKService struct {
	mu  sync.RWMutex
	dir string
}

// NewDurableADKService creates the service rooted at
// <sessionsDir>/adk (created 0700 when missing).
func NewDurableADKService(sessionsDir string) (*DurableADKService, error) {
	dir := filepath.Join(sessionsDir, adkSubdir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create ADK sessions directory: %w", err)
	}
	return &DurableADKService{dir: dir}, nil
}

// Compile-time contract check.
var _ adksession.Service = (*DurableADKService)(nil)

// adkRecordPath maps the (app, user, session) triple to a file name.
// Components are path-escaped so arbitrary IDs cannot traverse.
func (s *DurableADKService) adkRecordPath(appName, userID, sessionID string) string {
	name := url.PathEscape(appName) + "." + url.PathEscape(userID) + "." + url.PathEscape(sessionID) + ".json"
	return filepath.Join(s.dir, name)
}

func (s *DurableADKService) globalStatePath() string {
	return filepath.Join(s.dir, "globalstate.json")
}

// lockADKDir takes the cross-process exclusive flock for the ADK dir.
// Callers must hold the appropriate in-process mu and release via unlock.
func (s *DurableADKService) lockADKDir() (*os.File, error) {
	lockPath := filepath.Join(s.dir, dirLockName)
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("failed to open ADK sessions lock: %w", err)
	}
	if err := util.FlockExclusive(f); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("failed to lock ADK sessions dir: %w", err)
	}
	return f, nil
}

// readRecordLocked loads one session record; missing file = nil, nil
// (callers map that to ErrNotFound). Callers must hold mu + flock.
func (s *DurableADKService) readRecordLocked(appName, userID, sessionID string) (*adkSessionRecord, error) {
	data, err := os.ReadFile(s.adkRecordPath(appName, userID, sessionID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var rec adkSessionRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("failed to decode ADK session %s: %w", sessionID, err)
	}
	if rec.State == nil {
		rec.State = make(map[string]any)
	}
	return &rec, nil
}

// writeRecordLocked persists one session record atomically (0600).
// Callers must hold mu + flock.
func (s *DurableADKService) writeRecordLocked(rec *adkSessionRecord) error {
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal ADK session %s: %w", rec.SessionID, err)
	}
	path := s.adkRecordPath(rec.AppName, rec.UserID, rec.SessionID)
	if err := writeFileAtomic(path, data, 0600); err != nil {
		return fmt.Errorf("failed to save ADK session %s: %w", rec.SessionID, err)
	}
	return nil
}

// readGlobalLocked loads app/user state; missing file = empty.
// Callers must hold mu + flock.
func (s *DurableADKService) readGlobalLocked() (*adkGlobalState, error) {
	data, err := os.ReadFile(s.globalStatePath())
	if err != nil {
		if os.IsNotExist(err) {
			return &adkGlobalState{
				Apps:  make(map[string]map[string]any),
				Users: make(map[string]map[string]map[string]any),
			}, nil
		}
		return nil, err
	}
	var g adkGlobalState
	if err := json.Unmarshal(data, &g); err != nil {
		return nil, fmt.Errorf("failed to decode ADK global state: %w", err)
	}
	if g.Apps == nil {
		g.Apps = make(map[string]map[string]any)
	}
	if g.Users == nil {
		g.Users = make(map[string]map[string]map[string]any)
	}
	return &g, nil
}

// writeGlobalLocked persists app/user state atomically (0600).
// Callers must hold mu + flock.
func (s *DurableADKService) writeGlobalLocked(g *adkGlobalState) error {
	data, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal ADK global state: %w", err)
	}
	if err := writeFileAtomic(s.globalStatePath(), data, 0600); err != nil {
		return fmt.Errorf("failed to save ADK global state: %w", err)
	}
	return nil
}

// extractADKDeltas splits a state delta by scope prefix, mirroring the
// engine's internal sessionutils.ExtractStateDeltas: app:X -> app map,
// user:X -> user map, temp:* -> dropped, anything else -> session map.
func extractADKDeltas(delta map[string]any) (app, user, sess map[string]any) {
	app = make(map[string]any)
	user = make(map[string]any)
	sess = make(map[string]any)
	for k, v := range delta {
		switch {
		case strings.HasPrefix(k, adksession.KeyPrefixApp):
			app[strings.TrimPrefix(k, adksession.KeyPrefixApp)] = v
		case strings.HasPrefix(k, adksession.KeyPrefixUser):
			user[strings.TrimPrefix(k, adksession.KeyPrefixUser)] = v
		case strings.HasPrefix(k, adksession.KeyPrefixTemp):
			// Temporary: never persisted.
		default:
			sess[k] = v
		}
	}
	return app, user, sess
}

// mergeADKStates builds the runner-visible state: the session map plus
// app/user entries re-prefixed, mirroring sessionutils.MergeStates
// (session keys first, then app:, then user:).
func mergeADKStates(app, user, sess map[string]any) map[string]any {
	merged := make(map[string]any, len(app)+len(user)+len(sess))
	for k, v := range sess {
		merged[k] = v
	}
	for k, v := range app {
		merged[adksession.KeyPrefixApp+k] = v
	}
	for k, v := range user {
		merged[adksession.KeyPrefixUser+k] = v
	}
	return merged
}

// trimADKTempDelta returns the event with temp:-prefixed state-delta keys
// removed (copy-on-write when filtering; the caller's event is never
// mutated), mirroring the engine's trimTempDeltaState.
func trimADKTempDelta(event *adksession.Event) *adksession.Event {
	if len(event.Actions.StateDelta) == 0 {
		return event
	}
	filtered := make(map[string]any, len(event.Actions.StateDelta))
	for k, v := range event.Actions.StateDelta {
		if !strings.HasPrefix(k, adksession.KeyPrefixTemp) {
			filtered[k] = v
		}
	}
	if len(filtered) == len(event.Actions.StateDelta) {
		return event
	}
	cp := *event
	cp.Actions.StateDelta = filtered
	return &cp
}

// Create implements adksession.Service.
func (s *DurableADKService) Create(_ context.Context, req *adksession.CreateRequest) (*adksession.CreateResponse, error) {
	if req.AppName == "" || req.UserID == "" {
		return nil, fmt.Errorf("app_name and user_id are required, got app_name: %q, user_id: %q", req.AppName, req.UserID)
	}
	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = uuid.NewString()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	lf, err := s.lockADKDir()
	if err != nil {
		return nil, err
	}
	defer unlockDir(lf)

	if rec, err := s.readRecordLocked(req.AppName, req.UserID, sessionID); err != nil {
		return nil, err
	} else if rec != nil {
		return nil, fmt.Errorf("session %s already exists", req.SessionID)
	}

	state := req.State
	if state == nil {
		state = make(map[string]any)
	}
	g, err := s.readGlobalLocked()
	if err != nil {
		return nil, err
	}
	appDelta, userDelta, _ := extractADKDeltas(req.State)
	mergeIntoStrMap(getOrCreateStrMap(g.Apps, req.AppName), appDelta)
	mergeIntoNestedStrMap(g.Users, req.AppName, req.UserID, userDelta)
	merged := mergeADKStates(g.Apps[req.AppName], g.Users[req.AppName][req.UserID], state)

	rec := &adkSessionRecord{
		AppName:   req.AppName,
		UserID:    req.UserID,
		SessionID: sessionID,
		State:     state,
		UpdatedAt: time.Now().UTC(),
	}
	if err := s.writeRecordLocked(rec); err != nil {
		return nil, err
	}
	if err := s.writeGlobalLocked(g); err != nil {
		return nil, err
	}
	return &adksession.CreateResponse{
		Session: newDurableADKSession(req.AppName, req.UserID, sessionID, merged, nil, rec.UpdatedAt),
	}, nil
}

// Get implements adksession.Service, honoring NumRecentEvents and After.
func (s *DurableADKService) Get(_ context.Context, req *adksession.GetRequest) (*adksession.GetResponse, error) {
	if req.AppName == "" || req.UserID == "" || req.SessionID == "" {
		return nil, fmt.Errorf("app_name, user_id, session_id are required, got app_name: %q, user_id: %q, session_id: %q", req.AppName, req.UserID, req.SessionID)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	lf, err := s.lockADKDir()
	if err != nil {
		return nil, err
	}
	defer unlockDir(lf)

	rec, err := s.readRecordLocked(req.AppName, req.UserID, req.SessionID)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, fmt.Errorf("%w: %q", adksession.ErrNotFound, req.SessionID)
	}
	g, err := s.readGlobalLocked()
	if err != nil {
		return nil, err
	}
	merged := mergeADKStates(g.Apps[req.AppName], lookupNestedStrMap(g.Users, req.AppName, req.UserID), rec.State)

	events := rec.Events
	if req.NumRecentEvents > 0 && len(events) > req.NumRecentEvents {
		events = events[len(events)-req.NumRecentEvents:]
	}
	if !req.After.IsZero() && len(events) > 0 {
		idx := sort.Search(len(events), func(i int) bool {
			return !events[i].Timestamp.Before(req.After)
		})
		events = events[idx:]
	}
	return &adksession.GetResponse{
		Session: newDurableADKSession(req.AppName, req.UserID, req.SessionID, merged, events, rec.UpdatedAt),
	}, nil
}

// List implements adksession.Service: sessions under the app (and user,
// when set), without events, state merged — mirroring the engine.
func (s *DurableADKService) List(_ context.Context, req *adksession.ListRequest) (*adksession.ListResponse, error) {
	if req.AppName == "" {
		return nil, fmt.Errorf("app_name is required, got app_name: %q", req.AppName)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	lf, err := s.lockADKDir()
	if err != nil {
		return nil, err
	}
	defer unlockDir(lf)

	g, err := s.readGlobalLocked()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	out := &adksession.ListResponse{Sessions: make([]adksession.Session, 0)}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || e.Name() == "globalstate.json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var rec adkSessionRecord
		if err := json.Unmarshal(data, &rec); err != nil {
			return nil, fmt.Errorf("failed to decode ADK session file %s: %w", e.Name(), err)
		}
		if rec.AppName != req.AppName {
			continue
		}
		if req.UserID != "" && rec.UserID != req.UserID {
			continue
		}
		merged := mergeADKStates(g.Apps[rec.AppName], lookupNestedStrMap(g.Users, rec.AppName, rec.UserID), rec.State)
		out.Sessions = append(out.Sessions, newDurableADKSession(rec.AppName, rec.UserID, rec.SessionID, merged, nil, rec.UpdatedAt))
	}
	return out, nil
}

// Delete implements adksession.Service. Missing sessions are a no-op,
// mirroring the engine.
func (s *DurableADKService) Delete(_ context.Context, req *adksession.DeleteRequest) error {
	if req.AppName == "" || req.UserID == "" || req.SessionID == "" {
		return fmt.Errorf("app_name, user_id, session_id are required, got app_name: %q, user_id: %q, session_id: %q", req.AppName, req.UserID, req.SessionID)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	lf, err := s.lockADKDir()
	if err != nil {
		return err
	}
	defer unlockDir(lf)

	if err := os.Remove(s.adkRecordPath(req.AppName, req.UserID, req.SessionID)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// AppendEvent implements adksession.Service: partial events are skipped,
// ID-less events are named in place on the caller's event, temp: delta
// keys are stripped from the stored copy, and scope deltas update
// app/user/session state — all mirroring the engine.
func (s *DurableADKService) AppendEvent(ctx context.Context, curSession adksession.Session, event *adksession.Event) error {
	if curSession == nil {
		return fmt.Errorf("session is nil")
	}
	if event == nil {
		return fmt.Errorf("event is nil")
	}
	if event.Partial {
		return nil
	}
	if event.ID == "" {
		event.ID = uuid.NewString()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	lf, err := s.lockADKDir()
	if err != nil {
		return err
	}
	defer unlockDir(lf)

	rec, err := s.readRecordLocked(curSession.AppName(), curSession.UserID(), curSession.ID())
	if err != nil {
		return err
	}
	if rec == nil {
		return fmt.Errorf("%w: %q, cannot apply event", adksession.ErrNotFound, curSession.ID())
	}

	stored := trimADKTempDelta(event)
	rec.Events = append(rec.Events, stored)
	rec.UpdatedAt = event.Timestamp
	if len(event.Actions.StateDelta) > 0 {
		g, err := s.readGlobalLocked()
		if err != nil {
			return err
		}
		appDelta, userDelta, sessionDelta := extractADKDeltas(event.Actions.StateDelta)
		mergeIntoStrMap(getOrCreateStrMap(g.Apps, curSession.AppName()), appDelta)
		mergeIntoNestedStrMap(g.Users, curSession.AppName(), curSession.UserID(), userDelta)
		if rec.State == nil {
			rec.State = make(map[string]any)
		}
		mergeIntoStrMap(rec.State, sessionDelta)
		if err := s.writeGlobalLocked(g); err != nil {
			return err
		}
	}
	return s.writeRecordLocked(rec)
}

// getOrCreateStrMap returns the map for key, creating it when missing.
func getOrCreateStrMap(m map[string]map[string]any, key string) map[string]any {
	inner, ok := m[key]
	if !ok {
		inner = make(map[string]any)
		m[key] = inner
	}
	return inner
}

// lookupNestedStrMap returns users[app][user] or nil.
func lookupNestedStrMap(users map[string]map[string]map[string]any, app, user string) map[string]any {
	if byApp, ok := users[app]; ok {
		return byApp[user]
	}
	return nil
}

// mergeIntoNestedStrMap merges delta into users[app][user], creating maps.
func mergeIntoNestedStrMap(users map[string]map[string]map[string]any, app, user string, delta map[string]any) {
	byApp, ok := users[app]
	if !ok {
		byApp = make(map[string]map[string]any)
		users[app] = byApp
	}
	mergeIntoStrMap(getOrCreateStrMap(byApp, user), delta)
}

// mergeIntoStrMap copies delta into dst.
func mergeIntoStrMap(dst, delta map[string]any) {
	for k, v := range delta {
		dst[k] = v
	}
}

// durableADKSession is the Session implementation returned by the
// durable service. State is the merged snapshot at read time; Events
// shares pointers with the read record (mirroring the engine, which
// shares pointers with its in-memory store).
type durableADKSession struct {
	appName   string
	userID    string
	sessionID string
	state     map[string]any
	events    []*adksession.Event
	updatedAt time.Time
}

func newDurableADKSession(appName, userID, sessionID string, state map[string]any, events []*adksession.Event, updatedAt time.Time) *durableADKSession {
	if state == nil {
		state = make(map[string]any)
	}
	return &durableADKSession{
		appName:   appName,
		userID:    userID,
		sessionID: sessionID,
		state:     state,
		events:    events,
		updatedAt: updatedAt,
	}
}

func (s *durableADKSession) ID() string                { return s.sessionID }
func (s *durableADKSession) AppName() string           { return s.appName }
func (s *durableADKSession) UserID() string            { return s.userID }
func (s *durableADKSession) LastUpdateTime() time.Time { return s.updatedAt }

func (s *durableADKSession) State() adksession.State { return &durableADKState{state: s.state} }
func (s *durableADKSession) Events() adksession.Events {
	return durableADKEvents(s.events)
}

// durableADKState is a snapshot-backed State: Set mutates the snapshot
// only (same ephemerality as the engine's Get copies).
type durableADKState struct {
	mu    sync.RWMutex
	state map[string]any
}

func (s *durableADKState) Get(key string) (any, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.state[key]
	if !ok {
		return nil, adksession.ErrStateKeyNotExist
	}
	return v, nil
}

func (s *durableADKState) Set(key string, value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state[key] = value
	return nil
}

func (s *durableADKState) All() iter.Seq2[string, any] {
	s.mu.RLock()
	snapshot := make(map[string]any, len(s.state))
	for k, v := range s.state {
		snapshot[k] = v
	}
	s.mu.RUnlock()
	return func(yield func(string, any) bool) {
		for k, v := range snapshot {
			if !yield(k, v) {
				return
			}
		}
	}
}

// durableADKEvents implements adksession.Events over a slice.
type durableADKEvents []*adksession.Event

func (e durableADKEvents) Len() int { return len(e) }

func (e durableADKEvents) At(i int) *adksession.Event {
	if i >= 0 && i < len(e) {
		return e[i]
	}
	return nil
}

func (e durableADKEvents) All() iter.Seq[*adksession.Event] {
	return func(yield func(*adksession.Event) bool) {
		for _, ev := range e {
			if !yield(ev) {
				return
			}
		}
	}
}
