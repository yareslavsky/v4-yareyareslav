package repository

import (
	"context"
	"errors"
	"marketplace/catalog-svc/internal/listings/model"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v5"
)

func TestCreate(t *testing.T) {
	failure := errors.New("insert failed")
	for _, wantErr := range []error{nil, failure} {
		name := "success"
		if wantErr != nil {
			name = "database failure"
		}
		t.Run(name, func(t *testing.T) {
			pool := newCatalogTestPool(t)
			input := model.Listing{Title: "milk", Price: 5000, Stock: 0, Sold: 99, SellerID: testSellerID}
			query := pool.ExpectQuery(`INSERT INTO listings \(id, title, price, stock, sold, seller_id\) VALUES \(gen_random_uuid\(\), \$1, \$2, \$3, \$4, \$5\) RETURNING id::text, created_at`).
				WithArgs(input.Title, input.Price, input.Stock, 0, input.SellerID)
			if wantErr != nil {
				query.WillReturnError(wantErr)
			} else {
				query.WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(testListingID, testCreatedAt))
			}
			got, err := NewPGRepository(pool).Create(context.Background(), input)
			if !errors.Is(err, wantErr) {
				t.Fatalf("error = %v, want %v", err, wantErr)
			}
			if wantErr != nil {
				if got != nil {
					t.Fatal("returned listing on failure")
				}
				return
			}
			want := input
			want.ID, want.Sold, want.CreatedAt = testListingID, 0, testCreatedAt.UTC().Format(time.RFC3339Nano)
			if got == nil || *got != want {
				t.Fatalf("listing = %+v, want %+v", got, want)
			}
		})
	}
}
