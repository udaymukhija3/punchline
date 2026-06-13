package realtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"punchline/backend/internal/cards"
)

func TestManagerReservesRoomsWithInstanceOwnership(t *testing.T) {
	ctx := context.Background()
	registry := NewMemoryRoomRegistry()
	manager := NewRoomManager(
		cards.NewSeedDeck(),
		WithRoomRegistry(registry),
		WithInstanceID("instance-a"),
	)

	room, err := manager.CreateRoom(ctx)
	if err != nil {
		t.Fatalf("create room failed: %v", err)
	}
	code := room.SnapshotFor("").Code

	record, err := registry.LookupRoom(ctx, code)
	if err != nil {
		t.Fatalf("room was not reserved in registry: %v", err)
	}
	if record.InstanceID != "instance-a" {
		t.Fatalf("instance id = %q, want instance-a", record.InstanceID)
	}
}

func TestManagerReportsRoomsOwnedByAnotherInstance(t *testing.T) {
	ctx := context.Background()
	registry := NewMemoryRoomRegistry()
	owner := NewRoomManager(
		cards.NewSeedDeck(),
		WithRoomRegistry(registry),
		WithInstanceID("owner-instance"),
	)
	other := NewRoomManager(
		cards.NewSeedDeck(),
		WithRoomRegistry(registry),
		WithInstanceID("other-instance"),
	)

	room, err := owner.CreateRoom(ctx)
	if err != nil {
		t.Fatalf("create room failed: %v", err)
	}
	code := room.SnapshotFor("").Code

	_, err = other.GetRoom(ctx, code)
	var ownedElsewhere *RoomOwnedElsewhereError
	if !errors.As(err, &ownedElsewhere) {
		t.Fatalf("err = %v, want RoomOwnedElsewhereError", err)
	}
	if ownedElsewhere.InstanceID != "owner-instance" {
		t.Fatalf("owner = %q, want owner-instance", ownedElsewhere.InstanceID)
	}
}

func TestCreateRoomRespectsCapacity(t *testing.T) {
	ctx := context.Background()
	manager := NewRoomManager(cards.NewSeedDeck(), WithMaxLocalRooms(1))

	if _, err := manager.CreateRoom(ctx); err != nil {
		t.Fatalf("first create failed: %v", err)
	}
	_, err := manager.CreateRoom(ctx)
	if !errors.Is(err, ErrRoomCapacity) {
		t.Fatalf("err = %v, want ErrRoomCapacity", err)
	}
}

func TestEvictIdleRoomsReclaimsAbandonedRooms(t *testing.T) {
	ctx := context.Background()
	manager := NewRoomManager(cards.NewSeedDeck(), WithRoomIdleTTL(30*time.Minute))

	room, err := manager.CreateRoom(ctx)
	if err != nil {
		t.Fatal(err)
	}
	code := room.SnapshotFor("").Code

	// Fresh room with no idle history is kept.
	if n := manager.EvictIdleRooms(time.Now().UTC()); n != 0 {
		t.Fatalf("evicted %d fresh rooms, want 0", n)
	}

	// Age the room past the idle TTL and it should be reclaimed.
	room.mu.Lock()
	room.updatedAt = time.Now().UTC().Add(-time.Hour)
	room.mu.Unlock()

	if n := manager.EvictIdleRooms(time.Now().UTC()); n != 1 {
		t.Fatalf("evicted %d idle rooms, want 1", n)
	}
	if _, err := manager.GetRoom(ctx, code); err == nil {
		t.Fatal("evicted room should no longer be locally available")
	}
}

func TestManagerReportsUnavailableLocalStateAfterRestart(t *testing.T) {
	ctx := context.Background()
	registry := NewMemoryRoomRegistry()
	if err := registry.ReserveRoom(ctx, "TEST", "instance-a", time.Hour); err != nil {
		t.Fatal(err)
	}
	restarted := NewRoomManager(
		cards.NewSeedDeck(),
		WithRoomRegistry(registry),
		WithInstanceID("instance-a"),
	)

	_, err := restarted.GetRoom(ctx, "TEST")
	if !errors.Is(err, ErrRoomStateUnavailable) {
		t.Fatalf("err = %v, want ErrRoomStateUnavailable", err)
	}
}
