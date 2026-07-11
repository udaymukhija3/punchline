package roomstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"punchline/backend/internal/realtime"
)

type PostgresRoomRegistry struct {
	db *sql.DB
}

func NewPostgresRoomRegistry(db *sql.DB) *PostgresRoomRegistry {
	return &PostgresRoomRegistry{db: db}
}

func (r *PostgresRoomRegistry) ReserveRoom(ctx context.Context, code string, instanceID string, ttl time.Duration) error {
	const query = `
INSERT INTO rooms (code, instance_id, claimed_at, heartbeat_at, expires_at, updated_at)
VALUES (upper($1), $2, now(), now(), now() + ($3 * interval '1 second'), now())
ON CONFLICT (code) DO UPDATE SET
	instance_id = EXCLUDED.instance_id,
	claimed_at = now(),
	heartbeat_at = now(),
	expires_at = now() + ($3 * interval '1 second'),
	room_state = NULL,
	room_state_version = 0,
	state_updated_at = NULL,
	updated_at = now()
WHERE rooms.expires_at <= now()
RETURNING code`

	var reserved string
	err := r.db.QueryRowContext(ctx, query, code, instanceID, int(ttl.Seconds())).Scan(&reserved)
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return realtime.ErrRoomCodeReserved
	}
	return fmt.Errorf("reserve room: %w", err)
}

// ClaimRoom transfers an expired ownership lease without clearing the durable
// snapshot. It is only used after the manager has loaded and validated that
// snapshot for recovery.
func (r *PostgresRoomRegistry) ClaimRoom(ctx context.Context, code string, instanceID string, ttl time.Duration) error {
	const query = `
	INSERT INTO rooms (code, instance_id, claimed_at, heartbeat_at, expires_at, updated_at)
	VALUES (upper($1), $2, now(), now(), now() + ($3 * interval '1 second'), now())
	ON CONFLICT (code) DO UPDATE SET
		instance_id = EXCLUDED.instance_id,
		claimed_at = now(),
		heartbeat_at = now(),
		expires_at = now() + ($3 * interval '1 second'),
		updated_at = now()
	WHERE rooms.expires_at <= now()
	RETURNING code`

	var claimed string
	err := r.db.QueryRowContext(ctx, query, code, instanceID, int(ttl.Seconds())).Scan(&claimed)
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return realtime.ErrRoomCodeReserved
	}
	return fmt.Errorf("claim room: %w", err)
}

func (r *PostgresRoomRegistry) LookupRoom(ctx context.Context, code string) (realtime.RoomRecord, error) {
	const query = `
SELECT code, instance_id, claimed_at, heartbeat_at, expires_at, updated_at
FROM rooms
WHERE code = upper($1) AND expires_at > now()`

	var record realtime.RoomRecord
	err := r.db.QueryRowContext(ctx, query, code).Scan(
		&record.Code,
		&record.InstanceID,
		&record.ClaimedAt,
		&record.HeartbeatAt,
		&record.ExpiresAt,
		&record.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return realtime.RoomRecord{}, realtime.ErrRoomNotFound
	}
	if err != nil {
		return realtime.RoomRecord{}, fmt.Errorf("lookup room: %w", err)
	}
	return record, nil
}

func (r *PostgresRoomRegistry) HeartbeatRoom(ctx context.Context, code string, instanceID string, ttl time.Duration) error {
	const query = `
UPDATE rooms
SET heartbeat_at = now(),
	expires_at = now() + ($3 * interval '1 second'),
	updated_at = now()
WHERE code = upper($1) AND instance_id = $2 AND expires_at > now()`

	result, err := r.db.ExecContext(ctx, query, code, instanceID, int(ttl.Seconds()))
	if err != nil {
		return fmt.Errorf("heartbeat room: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("heartbeat room rows affected: %w", err)
	}
	if affected > 0 {
		return nil
	}

	record, err := r.LookupRoom(ctx, code)
	if err != nil {
		return err
	}
	return &realtime.RoomOwnedElsewhereError{Code: record.Code, InstanceID: record.InstanceID}
}

func (r *PostgresRoomRegistry) ReleaseRoom(ctx context.Context, code string, instanceID string) error {
	// Keep the durable snapshot in place while making the owner lease immediately
	// reclaimable. Deleting this row would make graceful deployments lose rooms.
	const query = `
	UPDATE rooms
	SET expires_at = now(), heartbeat_at = now(), updated_at = now()
	WHERE code = upper($1) AND instance_id = $2 AND expires_at > now()`
	result, err := r.db.ExecContext(ctx, query, code, instanceID)
	if err != nil {
		return fmt.Errorf("release room: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("release room rows affected: %w", err)
	}
	if affected > 0 {
		return nil
	}
	record, err := r.LookupRoom(ctx, code)
	if err != nil {
		return err
	}
	return &realtime.RoomOwnedElsewhereError{Code: record.Code, InstanceID: record.InstanceID}
}

func (r *PostgresRoomRegistry) SaveRoomState(ctx context.Context, state realtime.PersistedRoomState) error {
	payload, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode room state: %w", err)
	}
	const query = `
	UPDATE rooms
	SET room_state = $3::jsonb,
		room_state_version = $4,
		state_updated_at = now(),
		updated_at = now()
	WHERE code = upper($1)
		AND instance_id = $2
		AND expires_at > now()
		AND room_state_version <= $4
	RETURNING room_state_version`

	var storedVersion int64
	err = r.db.QueryRowContext(ctx, query, state.Code, state.InstanceID, payload, int64(state.Revision)).Scan(&storedVersion)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("save room state: %w", err)
	}

	// A newer snapshot may already be committed by the background writer. If
	// this instance still owns the lease, that is a successful stale write.
	record, lookupErr := r.LookupRoom(ctx, state.Code)
	if lookupErr != nil {
		return lookupErr
	}
	if record.InstanceID != state.InstanceID {
		return &realtime.RoomOwnedElsewhereError{Code: record.Code, InstanceID: record.InstanceID}
	}
	return nil
}

func (r *PostgresRoomRegistry) LoadRoomState(ctx context.Context, code string) (realtime.PersistedRoomState, error) {
	const query = `
	SELECT room_state
	FROM rooms
	WHERE code = upper($1) AND room_state IS NOT NULL`

	var payload []byte
	err := r.db.QueryRowContext(ctx, query, code).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return realtime.PersistedRoomState{}, realtime.ErrRoomStateNotFound
	}
	if err != nil {
		return realtime.PersistedRoomState{}, fmt.Errorf("load room state: %w", err)
	}
	var state realtime.PersistedRoomState
	if err := json.Unmarshal(payload, &state); err != nil {
		return realtime.PersistedRoomState{}, fmt.Errorf("decode room state: %w", err)
	}
	return state, nil
}

func (r *PostgresRoomRegistry) DeleteRoomState(ctx context.Context, code string, instanceID string) error {
	const query = `DELETE FROM rooms WHERE code = upper($1) AND instance_id = $2`
	_, err := r.db.ExecContext(ctx, query, code, instanceID)
	if err != nil {
		return fmt.Errorf("delete room state: %w", err)
	}
	return nil
}

func (r *PostgresRoomRegistry) ResetRoomState(ctx context.Context, code string, instanceID string) error {
	const query = `
	UPDATE rooms
	SET room_state = NULL, room_state_version = 0, state_updated_at = NULL, updated_at = now()
	WHERE code = upper($1) AND instance_id = $2`
	_, err := r.db.ExecContext(ctx, query, code, instanceID)
	if err != nil {
		return fmt.Errorf("reset room state: %w", err)
	}
	return nil
}

func (r *PostgresRoomRegistry) Ping(ctx context.Context) error {
	return r.db.PingContext(ctx)
}

func (r *PostgresRoomRegistry) RegistryName() string {
	return "postgres"
}

func (r *PostgresRoomRegistry) StateStoreName() string {
	return "postgres"
}

func (r *PostgresRoomRegistry) DatabaseStats() realtime.DatabaseStats {
	stats := r.db.Stats()
	return realtime.DatabaseStats{
		MaxOpenConnections: stats.MaxOpenConnections,
		OpenConnections:    stats.OpenConnections,
		InUse:              stats.InUse,
		Idle:               stats.Idle,
		WaitCount:          stats.WaitCount,
		WaitDuration:       stats.WaitDuration,
	}
}
