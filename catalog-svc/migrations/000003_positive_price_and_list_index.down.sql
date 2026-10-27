DROP INDEX listings_created_at_id_idx;
ALTER TABLE listings DROP CONSTRAINT listings_price_check;
ALTER TABLE listings ADD CONSTRAINT listings_price_check CHECK (price >= 0);
