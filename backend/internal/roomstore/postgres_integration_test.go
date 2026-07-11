package roomstore

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"punchline/backend/internal/realtime"
)

func TestPostgresReleaseRetainsRoomState(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not configured")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	registry := NewPostgresRoomRegistry(db)
	code := "IT" + time.Now().UTC().Format("150405.000000000")
	defer func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM rooms WHERE code = upper($1)`, code)
	}()

	if err := registry.ReserveRoom(ctx, code, "instance-a", time.Minute); err != nil {
		t.Fatal(err)
	}
	state := realtime.PersistedRoomState{
		SchemaVersion: 1,
		Revision:      1,
		InstanceID:    "instance-a",
		Code:          code,
		Phase:         realtime.PhaseLobby,
		ContentTier:   "party",
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	if err := registry.SaveRoomState(ctx, state); err != nil {
		t.Fatal(err)
	}
	if err := registry.ReleaseRoom(ctx, code, "instance-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.LookupRoom(ctx, code); !errors.Is(err, realtime.ErrRoomNotFound) {
		t.Fatalf("lookup after release = %v, want not found", err)
	}
	restored, err := registry.LoadRoomState(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Code != code || restored.Revision != state.Revision {
		t.Fatalf("restored state = %+v, want code %s revision %d", restored, code, state.Revision)
	}
	if err := registry.ClaimRoom(ctx, code, "instance-b", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.LoadRoomState(ctx, code); err != nil {
		t.Fatalf("state after claim: %v", err)
	}
	if err := registry.ReleaseRoom(ctx, code, "instance-b"); err != nil {
		t.Fatal(err)
	}
	if err := registry.ReserveRoom(ctx, code, "instance-c", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.LoadRoomState(ctx, code); !errors.Is(err, realtime.ErrRoomStateNotFound) {
		t.Fatalf("state after new reservation = %v, want not found", err)
	}
}
