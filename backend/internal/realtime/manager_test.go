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

func TestManagerRestoresDurableRoomStateAfterRestart(t *testing.T) {
	ctx := context.Background()
	registry := NewMemoryRoomRegistry()
	store := NewMemoryRoomStateStore()
	owner := NewRoomManager(
		cards.NewSeedDeck(),
		WithRoomRegistry(registry),
		WithRoomStateStore(store),
		WithInstanceID("instance-a"),
	)

	room, err := owner.CreateRoom(ctx)
	if err != nil {
		t.Fatal(err)
	}
	host, err := room.TryJoin("Alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := room.TryJoin("Bob"); err != nil {
		t.Fatal(err)
	}
	if _, err := room.TryJoin("Carol"); err != nil {
		t.Fatal(err)
	}
	if err := room.StartGame(host.ID); err != nil {
		t.Fatal(err)
	}
	if err := owner.PersistRoom(ctx, room); err != nil {
		t.Fatal(err)
	}

	restarted := NewRoomManager(
		cards.NewSeedDeck(),
		WithRoomRegistry(registry),
		WithRoomStateStore(store),
		WithInstanceID("instance-a"),
	)
	restored, err := restarted.GetRoom(ctx, room.SnapshotFor("").Code)
	if err != nil {
		t.Fatalf("restore room: %v", err)
	}
	snapshot := restored.SnapshotFor(host.ID)
	if snapshot.Phase != PhaseSubmitting || snapshot.RoundNumber != 1 || snapshot.Prompt == nil {
		t.Fatalf("restored snapshot = %+v, want first submitting round", snapshot)
	}
	if len(snapshot.Players) != 3 {
		t.Fatalf("restored players = %d, want 3", len(snapshot.Players))
	}
	restored.mu.Lock()
	restoredHost := restored.players[host.ID]
	restored.mu.Unlock()
	if restoredHost == nil || restoredHost.GuestToken != host.GuestToken {
		t.Fatal("guest token was not restored")
	}
	if restoredHost.Connected {
		t.Fatal("human players should reconnect after restore")
	}
}

func TestManagerClaimsDurableStateAfterLeaseExpires(t *testing.T) {
	ctx := context.Background()
	registry := NewMemoryRoomRegistry()
	store := NewMemoryRoomStateStore()
	owner := NewRoomManager(
		cards.NewSeedDeck(),
		WithRoomRegistry(registry),
		WithRoomStateStore(store),
		WithInstanceID("instance-a"),
		WithRoomLeaseTTL(time.Nanosecond),
	)
	room, err := owner.CreateRoom(ctx)
	if err != nil {
		t.Fatal(err)
	}
	code := room.SnapshotFor("").Code
	time.Sleep(time.Millisecond)

	replacement := NewRoomManager(
		cards.NewSeedDeck(),
		WithRoomRegistry(registry),
		WithRoomStateStore(store),
		WithInstanceID("instance-b"),
	)
	if _, err := replacement.GetRoom(ctx, code); err != nil {
		t.Fatalf("claim expired durable room: %v", err)
	}
	record, err := registry.LookupRoom(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	if record.InstanceID != "instance-b" {
		t.Fatalf("replacement owner = %q, want instance-b", record.InstanceID)
	}
}

func TestManagerShutdownReleasesLeaseForImmediateRecovery(t *testing.T) {
	ctx := context.Background()
	registry := NewMemoryRoomRegistry()
	store := NewMemoryRoomStateStore()
	owner := NewRoomManager(
		cards.NewSeedDeck(),
		WithRoomRegistry(registry),
		WithRoomStateStore(store),
		WithInstanceID("instance-a"),
	)
	room, err := owner.CreateRoom(ctx)
	if err != nil {
		t.Fatal(err)
	}
	code := room.SnapshotFor("").Code
	owner.StartDraining()
	owner.Shutdown()

	if _, err := registry.LookupRoom(ctx, code); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("lease lookup after shutdown = %v, want not found", err)
	}
	replacement := NewRoomManager(
		cards.NewSeedDeck(),
		WithRoomRegistry(registry),
		WithRoomStateStore(store),
		WithInstanceID("instance-b"),
	)
	if _, err := replacement.GetRoom(ctx, code); err != nil {
		t.Fatalf("recover immediately after graceful shutdown: %v", err)
	}
}

func TestManagerShutdownFlushesPendingRoomState(t *testing.T) {
	ctx := context.Background()
	registry := NewMemoryRoomRegistry()
	store := NewMemoryRoomStateStore()
	owner := NewRoomManager(
		cards.NewSeedDeck(),
		WithRoomRegistry(registry),
		WithRoomStateStore(store),
		WithInstanceID("instance-a"),
	)
	room, err := owner.CreateRoom(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pending := room.PersistedState()
	pending.Revision += 10
	pending.RoundNumber = 7
	owner.enqueueRoomState(pending)

	owner.Shutdown()

	restored, err := store.LoadRoomState(ctx, pending.Code)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Revision != pending.Revision || restored.RoundNumber != pending.RoundNumber {
		t.Fatalf("restored state = revision %d round %d, want revision %d round %d", restored.Revision, restored.RoundNumber, pending.Revision, pending.RoundNumber)
	}
}

func TestManagerDrainingFailsReadyAndCreate(t *testing.T) {
	ctx := context.Background()
	manager := NewRoomManager(cards.NewSeedDeck())

	manager.StartDraining()

	if err := manager.Ready(ctx); !errors.Is(err, ErrDraining) {
		t.Fatalf("ready err = %v, want ErrDraining", err)
	}
	if _, err := manager.CreateRoom(ctx); !errors.Is(err, ErrDraining) {
		t.Fatalf("create err = %v, want ErrDraining", err)
	}
	if !manager.Stats().Draining {
		t.Fatal("stats should report draining")
	}
}

func TestManagerRecordsRegistryOperationStats(t *testing.T) {
	ctx := context.Background()
	manager := NewRoomManager(cards.NewSeedDeck())

	room, err := manager.CreateRoom(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.GetRoom(ctx, room.SnapshotFor("").Code); err != nil {
		t.Fatal(err)
	}
	if err := manager.Ready(ctx); err != nil {
		t.Fatal(err)
	}

	stats := manager.Stats().RegistryOperations
	want := map[string]bool{"reserve_room:ok": false, "ping:ok": false}
	for _, stat := range stats {
		key := stat.Operation + ":" + stat.Result
		if _, ok := want[key]; ok {
			want[key] = stat.Count > 0 && stat.DurationSeconds >= 0
		}
	}
	for key, ok := range want {
		if !ok {
			t.Fatalf("missing registry metric %s in %+v", key, stats)
		}
	}
}
