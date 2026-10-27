CREATE TABLE reservations (
    id uuid PRIMARY KEY,
    listing_id uuid,
    qty bigint CHECK (qty > 0),
    remaining bigint,
    released boolean NOT NULL DEFAULT false
);
