ALTER TABLE rooms
ADD COLUMN IF NOT EXISTS instance_id TEXT;

UPDATE rooms
SET instance_id = 'legacy-single-instance'
WHERE instance_id IS NULL;

ALTER TABLE rooms
ALTER COLUMN instance_id SET NOT NULL;

ALTER TABLE rooms
ADD COLUMN IF NOT EXISTS claimed_at TIMESTAMPTZ NOT NULL DEFAULT now();

ALTER TABLE rooms
ADD COLUMN IF NOT EXISTS heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT now();

ALTER TABLE rooms
ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ NOT NULL DEFAULT (now() + interval '6 hours');

CREATE INDEX IF NOT EXISTS idx_rooms_instance_expires
ON rooms(instance_id, expires_at);

CREATE INDEX IF NOT EXISTS idx_rooms_expires
ON rooms(expires_at);
