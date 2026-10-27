package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/pashagolub/pgxmock/v5"
)

func TestReleaseIdempotencyKey(t *testing.T) {
	databaseErr := errors.New("database unavailable")
	tests := []struct {
		name         string
		rowsAffected int64
		databaseErr  error
		wantReleased bool
	}{
		{name: "unfinished key deleted", rowsAffected: 1, wantReleased: true},
		{name: "no unfinished key found", rowsAffected: 0},
		{name: "database error", databaseErr: databaseErr},
		{name: "deadline exceeded", databaseErr: context.DeadlineExceeded},
		{name: "request canceled", databaseErr: context.Canceled},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := newTestPool(t, pgxmock.QueryMatcherEqual)

			key := "order-1"
			expected := pool.ExpectExec(
				"DELETE FROM idempotency_keys WHERE buyer_id = $1 AND key = $2 AND order_id is NULL",
			).WithArgs(ValidBuyerID, key)
			if tt.databaseErr != nil {
				expected.WillReturnError(tt.databaseErr)
			} else {
				expected.WillReturnResult(pgxmock.NewResult("DELETE", tt.rowsAffected))
			}

			repo := NewPGRepository(pool, nil)
			released, err := repo.ReleaseIdempotencyKey(context.Background(), ValidBuyerID, key)
			if !errors.Is(err, tt.databaseErr) {
				t.Errorf("error = %v, want %v", err, tt.databaseErr)
			}
			if released != tt.wantReleased {
				t.Errorf("released = %v, want %v", released, tt.wantReleased)
			}
		})
	}
}
