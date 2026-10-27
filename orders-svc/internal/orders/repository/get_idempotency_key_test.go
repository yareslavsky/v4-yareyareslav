package repository

import (
	"context"
	"errors"
	"marketplace/orders-svc/internal/orders/model"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v5"
)

func TestGetIdempotencyKeyState(t *testing.T) {
	orderID := createdOrderID
	databaseErr := errors.New("database unavailable")
	tests := []struct {
		name      string
		orderID   *string
		queryErr  error
		wantState *model.IdempotencyKeyState
		wantErr   error
	}{
		{
			name: "completed key", orderID: &orderID,
			wantState: &model.IdempotencyKeyState{OrderID: &orderID},
		},
		{name: "unfinished key", wantState: &model.IdempotencyKeyState{}},
		{name: "key not found", queryErr: pgx.ErrNoRows},
		{name: "database error", queryErr: databaseErr, wantErr: databaseErr},
		{name: "deadline exceeded", queryErr: context.DeadlineExceeded, wantErr: context.DeadlineExceeded},
		{name: "request canceled", queryErr: context.Canceled, wantErr: context.Canceled},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := newTestPool(t, pgxmock.QueryMatcherEqual)

			key := "order-1"
			query := pool.ExpectQuery(
				"SELECT order_id::text FROM idempotency_keys WHERE buyer_id = $1 AND key = $2",
			).WithArgs(ValidBuyerID, key)
			if tt.queryErr != nil {
				query.WillReturnError(tt.queryErr)
			} else {
				var value any
				if tt.orderID != nil {
					value = tt.orderID
				}
				query.WillReturnRows(pgxmock.NewRows([]string{"order_id"}).AddRow(value))
			}

			repo := NewPGRepository(pool, nil)
			state, err := repo.GetIdempotencyKeyState(context.Background(), ValidBuyerID, key)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("error = %v, want %v", err, tt.wantErr)
			}
			if !reflect.DeepEqual(state, tt.wantState) {
				t.Errorf("state = %+v, want %+v", state, tt.wantState)
			}
		})
	}
}
