package realtime

import (
	"context"
	"crypto/rand"
	"errors"
	"math/big"
	"os"
	"strings"
	"sync"
	"time"

	"punchline/backend/internal/cards"
)

const (
	defaultRoomLeaseTTL  = 6 * time.Hour
	defaultRoomIdleTTL   = 30 * time.Minute
	defaultMaxLocalRooms = 5000
	roomCodeAttempts     = 64
)

// ErrRoomCapacity is returned when this instance is already hosting the maximum
// number of local rooms.
var ErrRoomCapacity = errors.New("room capacity reached")

type RoomManager struct {
	mu            sync.RWMutex
	deck          cards.Deck
	rooms         map[string]*Room
	registry      RoomRegistry
	instanceID    string
	roomLeaseTTL  time.Duration
	roomIdleTTL   time.Duration
	maxLocalRooms int
}

type RoomManagerOption func(*RoomManager)

func WithRoomRegistry(registry RoomRegistry) RoomManagerOption {
	return func(m *RoomManager) {
		if registry != nil {
			m.registry = registry
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
		deck:          deck,
		rooms:         map[string]*Room{},
		registry:      NewMemoryRoomRegistry(),
		instanceID:    defaultInstanceID(),
		roomLeaseTTL:  defaultRoomLeaseTTL,
		roomIdleTTL:   defaultRoomIdleTTL,
		maxLocalRooms: defaultMaxLocalRooms,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

func (m *RoomManager) CreateRoom(ctx context.Context) (*Room, error) {
	m.mu.RLock()
	atCapacity := len(m.rooms) >= m.maxLocalRooms
	m.mu.RUnlock()
	if atCapacity {
		return nil, ErrRoomCapacity
	}

	for attempt := 0; attempt < roomCodeAttempts; attempt++ {
		code := randomCode(4)

		m.mu.RLock()
		_, exists := m.rooms[code]
		m.mu.RUnlock()
		if exists {
			continue
		}

		// Reserve in the registry WITHOUT holding the manager lock — this may be
		// a network round-trip to Postgres and must not serialise other rooms.
		if err := m.registry.ReserveRoom(ctx, code, m.instanceID, m.roomLeaseTTL); err != nil {
			if errors.Is(err, ErrRoomCodeReserved) {
				continue
			}
			return nil, err
		}

		m.mu.Lock()
		if _, exists := m.rooms[code]; exists {
			m.mu.Unlock()
			continue // lost a local race for this code; try another
		}
		room := NewRoom(code, m.deck)
		m.rooms[code] = room
		m.mu.Unlock()
		return room, nil
	}
	return nil, errors.New("could not reserve a unique room code")
}

func (m *RoomManager) GetRoom(ctx context.Context, code string) (*Room, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	m.mu.RLock()
	r := m.rooms[code]
	m.mu.RUnlock()
	if r != nil {
		return r, nil
	}

	record, err := m.registry.LookupRoom(ctx, code)
	if err != nil {
		return nil, err
	}
	if record.InstanceID != m.instanceID {
		return nil, &RoomOwnedElsewhereError{Code: code, InstanceID: record.InstanceID}
	}
	return nil, ErrRoomStateUnavailable
}

func (m *RoomManager) HeartbeatLocalRooms(ctx context.Context) {
	codes := m.LocalRoomCodes()
	for _, code := range codes {
		_ = m.registry.HeartbeatRoom(ctx, code, m.instanceID, m.roomLeaseTTL)
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
	return m.registry.Ping(ctx)
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
	evicted := make([]*Room, 0)
	for code, room := range m.rooms {
		if room.Evictable(cutoff) {
			delete(m.rooms, code)
			evicted = append(evicted, room)
		}
	}
	m.mu.Unlock()

	for _, room := range evicted {
		room.Shutdown()
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

func (m *RoomManager) Stats() RoomManagerStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return RoomManagerStats{
		InstanceID:     m.instanceID,
		RoomRegistry:   m.registry.RegistryName(),
		LocalRoomCount: len(m.rooms),
		RoomLeaseTTL:   m.roomLeaseTTL,
	}
}

type RoomManagerStats struct {
	InstanceID     string        `json:"instance_id"`
	RoomRegistry   string        `json:"room_registry"`
	LocalRoomCount int           `json:"local_room_count"`
	RoomLeaseTTL   time.Duration `json:"-"`
}

func randomCode(n int) string {
	alphabet := "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	out := make([]byte, n)
	for i := range out {
		x, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
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
