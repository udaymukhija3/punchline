-- Durable active-room snapshots. The registry lease still decides which
-- process owns a room; this JSONB payload lets that owner be reconstructed
-- after a restart and lets a new owner take over only after lease expiry.
ALTER TABLE rooms
ADD COLUMN IF NOT EXISTS room_state JSONB;

ALTER TABLE rooms
ADD COLUMN IF NOT EXISTS room_state_version BIGINT NOT NULL DEFAULT 0;

ALTER TABLE rooms
ADD COLUMN IF NOT EXISTS state_updated_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_rooms_state_updated_at
ON rooms(state_updated_at)
WHERE room_state IS NOT NULL;
