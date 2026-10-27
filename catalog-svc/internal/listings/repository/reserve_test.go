package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v5"
)

func TestReserve(t *testing.T) {
	failure := errors.New("database failure")
	const listingID = "550e8400-e29b-41d4-a716-446655440000"

	for _, tc := range []struct {
		name          string
		qty           int64
		stock         int64
		failedStep    string
		wantRemaining int64
		wantErr       error
	}{
		{name: "reserve part", qty: 3, stock: 10, wantRemaining: 7},
		{name: "reserve last units", qty: 3, stock: 3},
		{name: "insufficient stock", qty: 4, stock: 3, wantErr: ErrInsufficientStock},
		{name: "empty stock", qty: 1, wantErr: ErrInsufficientStock},
		{name: "zero quantity", wantErr: ErrInvalidQuantity},
		{name: "negative quantity", qty: -1, wantErr: ErrInvalidQuantity},
		{name: "missing listing", qty: 1, failedStep: "missing", wantErr: ErrNotFound},
		{name: "begin failure", qty: 1, failedStep: "begin", wantErr: failure},
		{name: "read failure", qty: 1, failedStep: "read", wantErr: failure},
		{name: "update failure", qty: 1, stock: 3, failedStep: "update", wantErr: failure},
		{name: "commit failure", qty: 1, stock: 3, failedStep: "commit", wantErr: failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool, err := pgxmock.NewPool()
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			if tc.qty > 0 {
				begin := pool.ExpectBegin()
				if tc.failedStep == "begin" {
					begin.WillReturnError(failure)
				} else {
					read := pool.ExpectQuery(`SELECT stock FROM listings WHERE id = \$1 FOR UPDATE`).WithArgs(listingID)
					switch tc.failedStep {
					case "missing":
						read.WillReturnError(pgx.ErrNoRows)
					case "read":
						read.WillReturnError(failure)
					default:
						read.WillReturnRows(pgxmock.NewRows([]string{"stock"}).AddRow(tc.stock))
						if tc.stock >= tc.qty {
							update := pool.ExpectQuery(`UPDATE listings SET stock = stock - \$2 WHERE id = \$1 RETURNING stock`).WithArgs(listingID, tc.qty)
							if tc.failedStep == "update" {
								update.WillReturnError(failure)
							} else {
								update.WillReturnRows(pgxmock.NewRows([]string{"stock"}).AddRow(tc.stock - tc.qty))
								commit := pool.ExpectCommit()
								if tc.failedStep == "commit" {
									commit.WillReturnError(failure)
								}
							}
						}
					}
					pool.ExpectRollback()
				}
			}

			remaining, err := NewPGRepository(pool).Reserve(context.Background(), listingID, tc.qty)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if remaining != tc.wantRemaining {
				t.Fatalf("remaining = %d, want %d", remaining, tc.wantRemaining)
			}
			if err := pool.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
