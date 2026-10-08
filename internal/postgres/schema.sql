CREATE TABLE IF NOT EXISTS events (
    id BIGSERIAL PRIMARY KEY,
    title TEXT NOT NULL CHECK (length(title) > 0),
    starts_at TIMESTAMPTZ NOT NULL,
    capacity INTEGER NOT NULL CHECK (capacity > 0)
);

CREATE TABLE IF NOT EXISTS reservations (
    id UUID PRIMARY KEY,
    event_id BIGINT NOT NULL REFERENCES events(id),
    name TEXT NOT NULL,
    idempotency_key TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL CHECK (status IN ('pending', 'confirmed', 'cancelled', 'expired')),
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS reservations_active_event_idx
    ON reservations(event_id, expires_at) WHERE status IN ('pending', 'confirmed');
CREATE INDEX IF NOT EXISTS reservations_expiration_idx
    ON reservations(expires_at) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS events_starts_at_idx ON events(starts_at, id);
