package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v5"
)

func TestDelete(t *testing.T) {
	failure := errors.New("database failure")
	for _, tc := range []struct {
		name, step string
		wantErr    error
	}{
		{name: "success"},
		{name: "missing", step: "missing", wantErr: ErrNotFound},
		{name: "another seller", step: "forbidden", wantErr: ErrForbidden},
		{name: "begin failure", step: "begin", wantErr: failure},
		{name: "read failure", step: "read", wantErr: failure},
		{name: "delete failure", step: "delete", wantErr: failure},
		{name: "no rows deleted", step: "zero", wantErr: ErrNotFound},
		{name: "commit failure", step: "commit", wantErr: failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := newCatalogTestPool(t)
			begin := pool.ExpectBegin()
			if tc.step == "begin" {
				begin.WillReturnError(failure)
			} else {
				lookup := pool.ExpectQuery(`SELECT seller_id = \$2::uuid FROM listings WHERE id = \$1 FOR UPDATE`).WithArgs(testListingID, testSellerID)
				switch tc.step {
				case "missing":
					lookup.WillReturnError(pgx.ErrNoRows)
				case "read":
					lookup.WillReturnError(failure)
				case "forbidden":
					lookup.WillReturnRows(pgxmock.NewRows([]string{"owner"}).AddRow(false))
				default:
					lookup.WillReturnRows(pgxmock.NewRows([]string{"owner"}).AddRow(true))
					query := pool.ExpectExec(`DELETE FROM listings WHERE id = \$1 AND seller_id = \$2`).WithArgs(testListingID, testSellerID)
					switch tc.step {
					case "delete":
						query.WillReturnError(failure)
					case "zero":
						query.WillReturnResult(pgxmock.NewResult("DELETE", 0))
					default:
						query.WillReturnResult(pgxmock.NewResult("DELETE", 1))
						commit := pool.ExpectCommit()
						if tc.step == "commit" {
							commit.WillReturnError(failure)
						}
					}
				}
				pool.ExpectRollback()
			}
			if err := NewPGRepository(pool).Delete(context.Background(), testListingID, testSellerID); !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}
