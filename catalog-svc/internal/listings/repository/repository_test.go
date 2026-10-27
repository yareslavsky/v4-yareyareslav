package repository

import (
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v5"
)

const testListingID = "550e8400-e29b-41d4-a716-446655440000"
const testSellerID = "550e8400-e29b-41d4-a716-446655440001"

var testCreatedAt = time.Date(2026, 10, 12, 12, 0, 0, 123000000, time.FixedZone("MSK", 3*60*60))

func newCatalogTestPool(t *testing.T) pgxmock.PgxPoolIface {
	t.Helper()
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		defer pool.Close()
		if err := pool.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})
	return pool
}

func listingRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{"id", "title", "price", "stock", "sold", "seller_id", "created_at"})
}
