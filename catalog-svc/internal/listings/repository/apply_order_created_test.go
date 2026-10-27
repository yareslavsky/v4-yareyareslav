package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/pashagolub/pgxmock/v5"
)

func TestApplyOrderCreated(t *testing.T) {
	const eventID = "550e8400-e29b-41d4-a716-446655440000"
	const listingID = "550e8400-e29b-41d4-a716-446655440001"
	failure := errors.New("database failure")

	for _, tc := range []struct {
		name        string
		qty         int64
		failedStep  string
		wantApplied bool
		wantErr     error
	}{
		{name: "new event", qty: 3, wantApplied: true},
		{name: "duplicate", qty: 3, failedStep: "duplicate"},
		{name: "zero quantity", wantErr: ErrInvalidQuantity},
		{name: "negative quantity", qty: -1, wantErr: ErrInvalidQuantity},
		{name: "begin failure", qty: 3, failedStep: "begin", wantErr: failure},
		{name: "insert failure", qty: 3, failedStep: "insert", wantErr: failure},
		{name: "update failure", qty: 3, failedStep: "update", wantErr: failure},
		{name: "missing listing", qty: 3, failedStep: "missing", wantErr: ErrNotFound},
		{name: "commit failure", qty: 3, failedStep: "commit", wantErr: failure},
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
					insert := pool.ExpectExec(`INSERT INTO processed_events \(event_id\) VALUES \(\$1\) ON CONFLICT \(event_id\) DO NOTHING`).WithArgs(eventID)
					switch tc.failedStep {
					case "insert":
						insert.WillReturnError(failure)
					case "duplicate":
						insert.WillReturnResult(pgxmock.NewResult("INSERT", 0))
					default:
						insert.WillReturnResult(pgxmock.NewResult("INSERT", 1))
						update := pool.ExpectExec(`UPDATE listings SET sold = sold \+ \$2 WHERE id = \$1`).WithArgs(listingID, tc.qty)
						switch tc.failedStep {
						case "update":
							update.WillReturnError(failure)
						case "missing":
							update.WillReturnResult(pgxmock.NewResult("UPDATE", 0))
						default:
							update.WillReturnResult(pgxmock.NewResult("UPDATE", 1))
							commit := pool.ExpectCommit()
							if tc.failedStep == "commit" {
								commit.WillReturnError(failure)
							}
						}
					}
					pool.ExpectRollback()
				}
			}

			applied, err := NewPGRepository(pool).ApplyOrderCreated(context.Background(), eventID, listingID, tc.qty)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if applied != tc.wantApplied {
				t.Fatalf("applied = %v, want %v", applied, tc.wantApplied)
			}
			if err := pool.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
