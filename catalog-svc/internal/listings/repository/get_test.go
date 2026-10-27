package repository

import (
	"context"
	"errors"
	"marketplace/catalog-svc/internal/listings/model"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestGet(t *testing.T) {
	failure := errors.New("read failed")
	for _, tc := range []struct {
		name           string
		dbErr, wantErr error
	}{
		{name: "success"},
		{name: "missing", dbErr: pgx.ErrNoRows, wantErr: ErrNotFound},
		{name: "database failure", dbErr: failure, wantErr: failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := newCatalogTestPool(t)
			query := pool.ExpectQuery(`SELECT id::text, title, price, stock, sold, seller_id::text, created_at FROM listings WHERE id = \$1`).WithArgs(testListingID)
			if tc.dbErr != nil {
				query.WillReturnError(tc.dbErr)
			} else {
				query.WillReturnRows(listingRows().AddRow(testListingID, "milk", int64(5000), int64(5), int64(2), testSellerID, testCreatedAt))
			}
			got, err := NewPGRepository(pool).Get(context.Background(), testListingID)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr != nil {
				if got != nil {
					t.Fatal("returned listing on failure")
				}
				return
			}
			want := model.Listing{ID: testListingID, Title: "milk", Price: 5000, Stock: 5, Sold: 2, SellerID: testSellerID, CreatedAt: testCreatedAt.UTC().Format(time.RFC3339Nano)}
			if got == nil || *got != want {
				t.Fatalf("listing = %+v, want %+v", got, want)
			}
		})
	}
}
