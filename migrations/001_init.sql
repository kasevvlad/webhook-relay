CREATE TABLE IF NOT EXISTS subscriptions (
    id BIGSERIAL PRIMARY KEY,
    url TEXT NOT NULL,
    event_type TEXT NOT NULL,
    secret TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS events (
    id BIGSERIAL PRIMARY KEY,
    type TEXT NOT NULL,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS deliveries (
    id BIGSERIAL PRIMARY KEY,
    event_id BIGINT NOT NULL REFERENCES events (id),
    subscription_id BIGINT NOT NULL REFERENCES subscriptions (id) ON DELETE CASCADE,
    status TEXT NOT NULL DEFAULT 'pending',
    attempts INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_status_code INT,
    last_error TEXT,
    delivered_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS deliveries_pending_idx ON deliveries (next_attempt_at) WHERE status = 'pending';
