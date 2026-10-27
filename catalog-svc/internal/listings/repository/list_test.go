package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v5"
)

func TestList(t *testing.T) {
	failure := errors.New("database failure")
	for _, tc := range []struct {
		name    string
		step    string
		wantLen int
		wantErr error
	}{
		{name: "page", wantLen: 1},
		{name: "empty page", step: "empty"},
		{name: "query failure", step: "query", wantErr: failure},
		{name: "scan failure", step: "scan"},
		{name: "iteration failure", step: "rows", wantErr: failure},
		{name: "count failure", step: "count", wantErr: failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := newCatalogTestPool(t)
			query := pool.ExpectQuery(`SELECT id::text, title, price, stock, sold, seller_id::text, created_at FROM listings ORDER BY created_at ASC, id ASC LIMIT \$1 OFFSET \$2`).WithArgs(20, 5)
			rows := listingRows()
			switch tc.step {
			case "query":
				query.WillReturnError(failure)
			case "scan":
				query.WillReturnRows(rows.AddRow(testListingID, "milk", "invalid price", int64(5), int64(2), testSellerID, testCreatedAt))
			case "rows":
				query.WillReturnRows(rows.AddRow(testListingID, "milk", int64(5000), int64(5), int64(2), testSellerID, testCreatedAt).RowError(1, failure)).RowsWillBeClosed()
			default:
				if tc.step != "empty" {
					rows.AddRow(testListingID, "milk", int64(5000), int64(5), int64(2), testSellerID, testCreatedAt)
				}
				query.WillReturnRows(rows).RowsWillBeClosed()
				count := pool.ExpectQuery(`SELECT COUNT\(\*\) FROM listings`)
				if tc.step == "count" {
					count.WillReturnError(failure)
				} else {
					count.WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(int64(42)))
				}
			}
			got, total, err := NewPGRepository(pool).List(context.Background(), 20, 5)
			if tc.step == "scan" {
				if err == nil {
					t.Fatal("expected scan error")
				}
				if got != nil || total != 0 {
					t.Fatalf("page returned on scan failure: %+v, total = %d", got, total)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr != nil {
				if got != nil || total != 0 {
					t.Fatalf("page returned on failure: %+v, total = %d", got, total)
				}
				return
			}
			if got == nil || len(got) != tc.wantLen || total != 42 {
				t.Fatalf("page = %+v, total = %d", got, total)
			}
			if len(got) > 0 && got[0].CreatedAt != testCreatedAt.UTC().Format(time.RFC3339Nano) {
				t.Fatalf("unexpected timestamp: %s", got[0].CreatedAt)
			}
		})
	}
}
