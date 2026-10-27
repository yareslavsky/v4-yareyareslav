CREATE TABLE idempotency_keys (
    buyer_id UUID NOT NULL,
    key VARCHAR(200) NOT NULL CHECK (char_length(key) > 0),
    order_id UUID REFERENCES orders (id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (buyer_id, key)
);

DELETE FROM idempotency_keys
WHERE created_at < NOW() - INTERVAL '24 hours';