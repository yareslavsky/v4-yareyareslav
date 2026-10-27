CREATE TABLE reservation_sagas (
    id uuid PRIMARY KEY,
    buyer_id uuid NOT NULL,
    idempotency_key text NOT NULL,
    completed boolean NOT NULL DEFAULT false,
    retry_at timestamptz NOT NULL DEFAULT now() + interval '10 seconds'
);
CREATE INDEX reservation_sagas_retry_idx ON reservation_sagas (retry_at) WHERE NOT completed;
ALTER TABLE idempotency_keys ADD COLUMN reservation_id uuid;
