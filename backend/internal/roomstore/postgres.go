package roomstore

import (
	"context"
	"database/sql"
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

func (r *PostgresRoomRegistry) Ping(ctx context.Context) error {
	return r.db.PingContext(ctx)
}

func (r *PostgresRoomRegistry) RegistryName() string {
	return "postgres"
}
