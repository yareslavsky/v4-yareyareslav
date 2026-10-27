CREATE TABLE listings (
  id UUID PRIMARY KEY,
  title VARCHAR(200) NOT NULL,
  price BIGINT NOT NULL CHECK (price >= 0),
  stock BIGINT NOT NULL CHECK (stock >= 0),
  sold BIGINT NOT NULL CHECK (sold >= 0),
  seller_id UUID NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
