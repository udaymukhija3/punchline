package realtime

import (
	"context"
	"crypto/rand"
	"errors"
	"math/big"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"punchline/backend/internal/cards"
)

const (
	defaultRoomLeaseTTL  = 90 * time.Second
	defaultRoomIdleTTL   = 30 * time.Minute
	defaultMaxLocalRooms = 5000
	roomCodeAttempts     = 64
)

// ErrRoomCapacity is returned when this instance is already hosting the maximum
// number of local rooms.
var ErrRoomCapacity = errors.New("room capacity reached")

// ErrDraining is returned when the instance is intentionally leaving service.
var ErrDraining = errors.New("instance is draining")

type RoomManager struct {
	mu            sync.RWMutex
	deck          cards.Deck
	rooms         map[string]*Room
	registry      RoomRegistry
	stateStore    RoomStateStore
	instanceID    string
	roomLeaseTTL  time.Duration
	roomIdleTTL   time.Duration
	maxLocalRooms int
	draining      bool

	metricsMu       sync.Mutex
	registryMetrics map[registryMetricKey]registryMetric

	stateMu           sync.Mutex
	pendingRoomStates map[string]PersistedRoomState
	stateWake         chan struct{}
	stateWorker       sync.Once
}

type registryMetricKey struct {
	Operation string
	Result    string
}

type registryMetric struct {
	Count           uint64
	DurationSeconds float64
}

type RoomManagerOption func(*RoomManager)

func WithRoomRegistry(registry RoomRegistry) RoomManagerOption {
	return func(m *RoomManager) {
		if registry != nil {
			m.registry = registry
		}
	}
}

func WithRoomStateStore(store RoomStateStore) RoomManagerOption {
	return func(m *RoomManager) {
		if store != nil {
			m.stateStore = store
		}
	}
}

func WithInstanceID(instanceID string) RoomManagerOption {
	return func(m *RoomManager) {
		if id := strings.TrimSpace(instanceID); id != "" {
			m.instanceID = id
		}
	}
}

func WithRoomLeaseTTL(ttl time.Duration) RoomManagerOption {
	return func(m *RoomManager) {
		if ttl > 0 {
			m.roomLeaseTTL = ttl
		}
	}
}

func WithRoomIdleTTL(ttl time.Duration) RoomManagerOption {
	return func(m *RoomManager) {
		if ttl > 0 {
			m.roomIdleTTL = ttl
		}
	}
}

func WithMaxLocalRooms(n int) RoomManagerOption {
	return func(m *RoomManager) {
		if n > 0 {
			m.maxLocalRooms = n
		}
	}
}

func NewRoomManager(deck cards.Deck, opts ...RoomManagerOption) *RoomManager {
	m := &RoomManager{
		deck:              deck,
		rooms:             map[string]*Room{},
		registry:          NewMemoryRoomRegistry(),
		stateStore:        NewMemoryRoomStateStore(),
		instanceID:        defaultInstanceID(),
		roomLeaseTTL:      defaultRoomLeaseTTL,
		roomIdleTTL:       defaultRoomIdleTTL,
		maxLocalRooms:     defaultMaxLocalRooms,
		registryMetrics:   map[registryMetricKey]registryMetric{},
		pendingRoomStates: map[string]PersistedRoomState{},
		stateWake:         make(chan struct{}, 1),
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

func (m *RoomManager) CreateRoom(ctx context.Context) (*Room, error) {
	for attempt := 0; attempt < roomCodeAttempts; attempt++ {
		if err := m.canAcceptNewRoom(); err != nil {
			return nil, err
		}
		code := randomCode(4)

		m.mu.RLock()
		_, exists := m.rooms[code]
		m.mu.RUnlock()
		if exists {
			continue
		}

		// Reserve in the registry WITHOUT holding the manager lock — this may be
		// a network round-trip to Postgres and must not serialise other rooms.
		started := time.Now()
		if err := m.registry.ReserveRoom(ctx, code, m.instanceID, m.roomLeaseTTL); err != nil {
			m.recordRegistryOperation("reserve_room", started, err)
			if errors.Is(err, ErrRoomCodeReserved) {
				continue
			}
			return nil, err
		}
		m.recordRegistryOperation("reserve_room", started, nil)
		if err := m.resetRoomState(ctx, code); err != nil {
			m.releaseRoom(context.Background(), code)
			return nil, err
		}

		m.mu.Lock()
		if m.draining {
			m.mu.Unlock()
			m.releaseRoom(context.Background(), code)
			return nil, ErrDraining
		}
		if len(m.rooms) >= m.maxLocalRooms {
			m.mu.Unlock()
			m.releaseRoom(context.Background(), code)
			return nil, ErrRoomCapacity
		}
		if _, exists := m.rooms[code]; exists {
			m.mu.Unlock()
			m.releaseRoom(context.Background(), code)
			continue // lost a local race for this code; try another
		}
		room := NewRoom(code, m.deck)
		room.SetStateObserver(m.enqueueRoomState)
		m.rooms[code] = room
		m.mu.Unlock()

		if err := m.PersistRoom(ctx, room); err != nil {
			m.removeRoom(code, room)
			m.releaseRoom(context.Background(), code)
			return nil, err
		}
		return room, nil
	}
	return nil, errors.New("could not reserve a unique room code")
}

func (m *RoomManager) GetRoom(ctx context.Context, code string) (*Room, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if m.Draining() {
		return nil, ErrDraining
	}
	m.mu.RLock()
	r := m.rooms[code]
	m.mu.RUnlock()
	if r != nil {
		return r, nil
	}

	started := time.Now()
	record, err := m.registry.LookupRoom(ctx, code)
	m.recordRegistryOperation("lookup_room", started, err)
	if err == nil {
		if record.InstanceID != m.instanceID {
			return nil, &RoomOwnedElsewhereError{Code: code, InstanceID: record.InstanceID}
		}
		return m.restoreRoom(ctx, code)
	}
	if !errors.Is(err, ErrRoomNotFound) {
		return nil, err
	}

	// The previous owner may have disappeared after its lease expired. A durable
	// snapshot lets this instance safely claim and resume that room.
	state, loadErr := m.loadRoomState(ctx, code)
	if loadErr != nil {
		if errors.Is(loadErr, ErrRoomStateNotFound) {
			return nil, ErrRoomNotFound
		}
		return nil, loadErr
	}
	started = time.Now()
	if claimErr := m.registry.ClaimRoom(ctx, code, m.instanceID, m.roomLeaseTTL); claimErr != nil {
		m.recordRegistryOperation("claim_room", started, claimErr)
		return nil, claimErr
	}
	m.recordRegistryOperation("claim_room", started, nil)
	if err := m.saveRoomState(ctx, state); err != nil {
		m.releaseRoom(context.Background(), code)
		return nil, err
	}
	room, restoreErr := m.installRestoredRoom(code, state)
	if restoreErr != nil {
		m.releaseRoom(context.Background(), code)
	}
	return room, restoreErr
}

func (m *RoomManager) canAcceptNewRoom() error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.draining {
		return ErrDraining
	}
	if len(m.rooms) >= m.maxLocalRooms {
		return ErrRoomCapacity
	}
	return nil
}

func (m *RoomManager) restoreRoom(ctx context.Context, code string) (*Room, error) {
	state, err := m.loadRoomState(ctx, code)
	if err != nil {
		if errors.Is(err, ErrRoomStateNotFound) {
			return nil, ErrRoomStateUnavailable
		}
		return nil, err
	}
	return m.installRestoredRoom(code, state)
}

func (m *RoomManager) loadRoomState(ctx context.Context, code string) (PersistedRoomState, error) {
	started := time.Now()
	state, err := m.stateStore.LoadRoomState(ctx, code)
	m.recordRegistryOperation("load_room_state", started, err)
	return state, err
}

func (m *RoomManager) installRestoredRoom(code string, state PersistedRoomState) (*Room, error) {
	room, err := RestoreRoom(state, m.deck)
	if err != nil {
		return nil, err
	}
	if room.code != code {
		return nil, errors.New("room state code does not match reservation")
	}
	room.SetStateObserver(m.enqueueRoomState)

	m.mu.Lock()
	if existing := m.rooms[code]; existing != nil {
		m.mu.Unlock()
		return existing, nil
	}
	if m.draining {
		m.mu.Unlock()
		return nil, ErrDraining
	}
	if len(m.rooms) >= m.maxLocalRooms {
		m.mu.Unlock()
		return nil, ErrRoomCapacity
	}
	m.rooms[code] = room
	m.mu.Unlock()
	room.Resume()
	return room, nil
}

// PersistRoom synchronously saves a snapshot before a caller acknowledges a
// user action. Timer and computer transitions use the bounded background queue
// below because they have no request lifecycle to wait on.
func (m *RoomManager) PersistRoom(ctx context.Context, room *Room) error {
	if room == nil {
		return errors.New("cannot persist a nil room")
	}
	return m.saveRoomState(ctx, room.PersistedState())
}

func (m *RoomManager) saveRoomState(ctx context.Context, state PersistedRoomState) error {
	state.InstanceID = m.instanceID
	started := time.Now()
	err := m.stateStore.SaveRoomState(ctx, state)
	m.recordRegistryOperation("save_room_state", started, err)
	return err
}

func (m *RoomManager) enqueueRoomState(state PersistedRoomState) {
	m.stateMu.Lock()
	if current, ok := m.pendingRoomStates[state.Code]; !ok || current.Revision < state.Revision {
		m.pendingRoomStates[state.Code] = state
	}
	select {
	case m.stateWake <- struct{}{}:
	default:
	}
	m.stateMu.Unlock()
}

// StartStatePersistence starts one bounded worker for timer/computer-driven
// mutations. Calling it more than once is safe.
func (m *RoomManager) StartStatePersistence(ctx context.Context) {
	m.stateWorker.Do(func() {
		go m.runStatePersistence(ctx)
	})
}

func (m *RoomManager) runStatePersistence(ctx context.Context) {
	for {
		state, ok := m.nextPendingRoomState()
		if !ok {
			select {
			case <-ctx.Done():
				m.flushPendingRoomStates()
				return
			case <-m.stateWake:
			}
			continue
		}
		persistCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := m.saveRoomState(persistCtx, state)
		cancel()
		if err == nil {
			continue
		}
		if m.hasLocalRoom(state.Code) {
			m.enqueueRoomState(state)
		}
		select {
		case <-ctx.Done():
			m.flushPendingRoomStates()
			return
		case <-time.After(time.Second):
		}
	}
}

func (m *RoomManager) nextPendingRoomState() (PersistedRoomState, bool) {
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	for code, state := range m.pendingRoomStates {
		delete(m.pendingRoomStates, code)
		return state, true
	}
	return PersistedRoomState{}, false
}

func (m *RoomManager) flushPendingRoomStates() {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		state, ok := m.nextPendingRoomState()
		if !ok {
			return
		}
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		err := m.saveRoomState(ctx, state)
		cancel()
		if err != nil {
			m.enqueueRoomState(state)
			return
		}
	}
}

func (m *RoomManager) removeRoom(code string, expected *Room) {
	m.mu.Lock()
	if room := m.rooms[code]; room == expected {
		delete(m.rooms, code)
	}
	m.mu.Unlock()
	m.clearPendingRoomState(code)
}

func (m *RoomManager) releaseRoom(ctx context.Context, code string) {
	started := time.Now()
	err := m.registry.ReleaseRoom(ctx, code, m.instanceID)
	m.recordRegistryOperation("release_room", started, err)
}

func (m *RoomManager) revokeRoom(code string) {
	m.mu.Lock()
	room := m.rooms[code]
	delete(m.rooms, code)
	m.mu.Unlock()
	m.clearPendingRoomState(code)
	if room != nil {
		room.Shutdown()
	}
}

func (m *RoomManager) hasLocalRoom(code string) bool {
	m.mu.RLock()
	_, ok := m.rooms[code]
	m.mu.RUnlock()
	return ok
}

func (m *RoomManager) clearPendingRoomState(code string) {
	m.stateMu.Lock()
	delete(m.pendingRoomStates, code)
	m.stateMu.Unlock()
}

func (m *RoomManager) HeartbeatLocalRooms(ctx context.Context) {
	codes := m.LocalRoomCodes()
	for _, code := range codes {
		started := time.Now()
		err := m.registry.HeartbeatRoom(ctx, code, m.instanceID, m.roomLeaseTTL)
		m.recordRegistryOperation("heartbeat_room", started, err)
		if errors.Is(err, ErrRoomNotLocal) || errors.Is(err, ErrRoomNotFound) {
			m.revokeRoom(code)
		}
	}
}

func (m *RoomManager) StartHeartbeat(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.HeartbeatLocalRooms(ctx)
			}
		}
	}()
}

// Ready reports whether the manager's backing registry is reachable.
func (m *RoomManager) Ready(ctx context.Context) error {
	if m.Draining() {
		return ErrDraining
	}
	started := time.Now()
	err := m.registry.Ping(ctx)
	m.recordRegistryOperation("ping", started, err)
	return err
}

// StartJanitor periodically evicts rooms that have no connected players and
// have been idle past the idle TTL, freeing memory and stopping their timers.
func (m *RoomManager) StartJanitor(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.EvictIdleRooms(time.Now().UTC())
			}
		}
	}()
}

// EvictIdleRooms removes abandoned rooms from memory. A room is evictable when
// it has zero connected clients and has not changed since now-idleTTL.
func (m *RoomManager) EvictIdleRooms(now time.Time) int {
	cutoff := now.Add(-m.roomIdleTTL)

	m.mu.Lock()
	evicted := make(map[string]*Room)
	for code, room := range m.rooms {
		if room.Evictable(cutoff) {
			delete(m.rooms, code)
			evicted[code] = room
		}
	}
	m.mu.Unlock()

	for code, room := range evicted {
		room.Shutdown()
		m.clearPendingRoomState(code)
		m.deleteRoomState(context.Background(), code)
	}
	return len(evicted)
}

func (m *RoomManager) LocalRoomCodes() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	codes := make([]string, 0, len(m.rooms))
	for code := range m.rooms {
		codes = append(codes, code)
	}
	return codes
}

func (m *RoomManager) StartDraining() {
	m.mu.Lock()
	if m.draining {
		m.mu.Unlock()
		return
	}
	m.draining = true
	rooms := make([]*Room, 0, len(m.rooms))
	for _, room := range m.rooms {
		rooms = append(rooms, room)
	}
	m.mu.Unlock()

	for _, room := range rooms {
		room.NotifyDraining()
	}
}

func (m *RoomManager) Draining() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.draining
}

// Shutdown closes all WebSocket connections after the server has had its drain
// grace period. Durable snapshots remain available for reconnecting clients.
func (m *RoomManager) Shutdown() {
	m.mu.Lock()
	rooms := make(map[string]*Room, len(m.rooms))
	for code, room := range m.rooms {
		delete(m.rooms, code)
		rooms[code] = room
	}
	m.mu.Unlock()
	for _, room := range rooms {
		room.Shutdown()
	}
	m.flushPendingRoomStates()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for code := range rooms {
		m.clearPendingRoomState(code)
		m.releaseRoom(ctx, code)
	}
}

func (m *RoomManager) Stats() RoomManagerStats {
	m.mu.RLock()
	connectedPlayers := 0
	for _, room := range m.rooms {
		connectedPlayers += room.ConnectedHumanPlayers()
	}
	stats := RoomManagerStats{
		InstanceID:       m.instanceID,
		RoomRegistry:     m.registry.RegistryName(),
		RoomStateStore:   m.stateStore.StateStoreName(),
		LocalRoomCount:   len(m.rooms),
		ConnectedPlayers: connectedPlayers,
		RoomLeaseTTL:     m.roomLeaseTTL,
		Draining:         m.draining,
	}
	m.mu.RUnlock()

	if reporter, ok := m.registry.(DatabaseStatsReporter); ok {
		database := reporter.DatabaseStats()
		stats.Database = &database
	}
	stats.RegistryOperations = m.registryOperationStats()
	return stats
}

type RoomManagerStats struct {
	InstanceID         string                   `json:"instance_id"`
	RoomRegistry       string                   `json:"room_registry"`
	RoomStateStore     string                   `json:"room_state_store"`
	LocalRoomCount     int                      `json:"local_room_count"`
	ConnectedPlayers   int                      `json:"connected_players"`
	RoomLeaseTTL       time.Duration            `json:"-"`
	Draining           bool                     `json:"draining"`
	Database           *DatabaseStats           `json:"-"`
	RegistryOperations []RegistryOperationStats `json:"-"`
}

func (m *RoomManager) deleteRoomState(ctx context.Context, code string) {
	started := time.Now()
	err := m.stateStore.DeleteRoomState(ctx, code, m.instanceID)
	m.recordRegistryOperation("delete_room_state", started, err)
}

func (m *RoomManager) resetRoomState(ctx context.Context, code string) error {
	started := time.Now()
	err := m.stateStore.ResetRoomState(ctx, code, m.instanceID)
	m.recordRegistryOperation("reset_room_state", started, err)
	return err
}

type RegistryOperationStats struct {
	Operation       string
	Result          string
	Count           uint64
	DurationSeconds float64
}

func (m *RoomManager) recordRegistryOperation(operation string, started time.Time, err error) {
	m.metricsMu.Lock()
	defer m.metricsMu.Unlock()

	key := registryMetricKey{Operation: operation, Result: registryResult(err)}
	metric := m.registryMetrics[key]
	metric.Count++
	metric.DurationSeconds += time.Since(started).Seconds()
	m.registryMetrics[key] = metric
}

func (m *RoomManager) registryOperationStats() []RegistryOperationStats {
	m.metricsMu.Lock()
	defer m.metricsMu.Unlock()

	out := make([]RegistryOperationStats, 0, len(m.registryMetrics))
	for key, metric := range m.registryMetrics {
		out = append(out, RegistryOperationStats{
			Operation:       key.Operation,
			Result:          key.Result,
			Count:           metric.Count,
			DurationSeconds: metric.DurationSeconds,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Operation != out[j].Operation {
			return out[i].Operation < out[j].Operation
		}
		return out[i].Result < out[j].Result
	})
	return out
}

func registryResult(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "canceled"
	case errors.Is(err, ErrRoomNotFound):
		return "not_found"
	case errors.Is(err, ErrRoomCodeReserved):
		return "reserved"
	case errors.Is(err, ErrRoomNotLocal):
		return "owned_elsewhere"
	case errors.Is(err, ErrRoomStateUnavailable):
		return "state_unavailable"
	default:
		return "error"
	}
}

func randomCode(n int) string {
	alphabet := "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	out := make([]byte, n)
	for i := range out {
		x, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			panic("secure random source unavailable")
		}
		out[i] = alphabet[x.Int64()]
	}
	return string(out)
}

func defaultInstanceID() string {
	if h, err := os.Hostname(); err == nil && strings.TrimSpace(h) != "" {
		return h
	}
	return "local"
}
