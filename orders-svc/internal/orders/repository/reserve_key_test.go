package repository

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/pashagolub/pgxmock/v5"
)

func TestReserveIdempotencyKey(t *testing.T) {
	databaseErr := errors.New("database unavailable")
	tests := []struct {
		name         string
		rowsAffected int64
		databaseErr  error
		wantReserved bool
	}{
		{name: "new key", rowsAffected: 1, wantReserved: true},
		{name: "existing key", rowsAffected: 0},
		{name: "database error", databaseErr: databaseErr},
		{name: "deadline exceeded", databaseErr: context.DeadlineExceeded},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := newTestPool(t, pgxmock.QueryMatcherRegexp)

			key := "order-1"
			expected := pool.ExpectExec(regexp.QuoteMeta(
				"INSERT INTO idempotency_keys (buyer_id, key) VALUES ($1, $2) ON CONFLICT (buyer_id, key) DO NOTHING",
			)).WithArgs(ValidBuyerID, key)
			if tt.databaseErr != nil {
				expected.WillReturnError(tt.databaseErr)
			} else {
				expected.WillReturnResult(pgxmock.NewResult("INSERT", tt.rowsAffected))
			}

			repo := NewPGRepository(pool, nil)
			reserved, err := repo.ReserveIdempotencyKey(context.Background(), ValidBuyerID, key)
			if !errors.Is(err, tt.databaseErr) {
				t.Errorf("error = %v, want %v", err, tt.databaseErr)
			}
			if reserved != tt.wantReserved {
				t.Errorf("reserved = %v, want %v", reserved, tt.wantReserved)
			}
		})
	}
}
