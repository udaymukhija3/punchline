package realtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

var (
	ErrRoomNotFound         = errors.New("room not found")
	ErrRoomCodeReserved     = errors.New("room code is already reserved")
	ErrRoomNotLocal         = errors.New("room is hosted by another instance")
	ErrRoomStateUnavailable = errors.New("room state is not loaded on this instance")
)

type RoomRecord struct {
	Code        string
	InstanceID  string
	ClaimedAt   time.Time
	HeartbeatAt time.Time
	ExpiresAt   time.Time
	UpdatedAt   time.Time
}

type RoomRegistry interface {
	ReserveRoom(ctx context.Context, code string, instanceID string, ttl time.Duration) error
	LookupRoom(ctx context.Context, code string) (RoomRecord, error)
	HeartbeatRoom(ctx context.Context, code string, instanceID string, ttl time.Duration) error
	// Ping reports whether the registry's backing store is reachable. Used by
	// the readiness probe.
	Ping(ctx context.Context) error
	RegistryName() string
}

type RoomOwnedElsewhereError struct {
	Code       string
	InstanceID string
}

func (e *RoomOwnedElsewhereError) Error() string {
	return fmt.Sprintf("room %s is hosted by instance %s", e.Code, e.InstanceID)
}

func (e *RoomOwnedElsewhereError) Unwrap() error {
	return ErrRoomNotLocal
}

type MemoryRoomRegistry struct {
	mu    sync.RWMutex
	rooms map[string]RoomRecord
}

func NewMemoryRoomRegistry() *MemoryRoomRegistry {
	return &MemoryRoomRegistry{rooms: map[string]RoomRecord{}}
}

func (r *MemoryRoomRegistry) ReserveRoom(ctx context.Context, code string, instanceID string, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	code = normalizeRoomCode(code)
	now := time.Now().UTC()
	expiresAt := now.Add(ttl)

	r.mu.Lock()
	defer r.mu.Unlock()

	if existing, ok := r.rooms[code]; ok && existing.ExpiresAt.After(now) {
		return ErrRoomCodeReserved
	}
	r.rooms[code] = RoomRecord{
		Code:        code,
		InstanceID:  instanceID,
		ClaimedAt:   now,
		HeartbeatAt: now,
		ExpiresAt:   expiresAt,
		UpdatedAt:   now,
	}
	return nil
}

func (r *MemoryRoomRegistry) LookupRoom(ctx context.Context, code string) (RoomRecord, error) {
	if err := ctx.Err(); err != nil {
		return RoomRecord{}, err
	}
	code = normalizeRoomCode(code)
	now := time.Now().UTC()

	r.mu.RLock()
	record, ok := r.rooms[code]
	r.mu.RUnlock()
	if !ok {
		return RoomRecord{}, ErrRoomNotFound
	}
	if !record.ExpiresAt.After(now) {
		r.mu.Lock()
		if current, ok := r.rooms[code]; ok && !current.ExpiresAt.After(now) {
			delete(r.rooms, code)
		}
		r.mu.Unlock()
		return RoomRecord{}, ErrRoomNotFound
	}
	return record, nil
}

func (r *MemoryRoomRegistry) HeartbeatRoom(ctx context.Context, code string, instanceID string, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	code = normalizeRoomCode(code)
	now := time.Now().UTC()

	r.mu.Lock()
	defer r.mu.Unlock()

	record, ok := r.rooms[code]
	if !ok || !record.ExpiresAt.After(now) {
		return ErrRoomNotFound
	}
	if record.InstanceID != instanceID {
		return &RoomOwnedElsewhereError{Code: code, InstanceID: record.InstanceID}
	}
	record.HeartbeatAt = now
	record.ExpiresAt = now.Add(ttl)
	record.UpdatedAt = now
	r.rooms[code] = record
	return nil
}

func (r *MemoryRoomRegistry) Ping(ctx context.Context) error {
	return ctx.Err()
}

func (r *MemoryRoomRegistry) RegistryName() string {
	return "memory"
}

func normalizeRoomCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}
