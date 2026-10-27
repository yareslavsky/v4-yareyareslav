package repository

import (
	"context"
	"errors"
	"marketplace/orders-svc/internal/orders/model"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v5"
)

func TestGetByID(t *testing.T) {
	orderID := createdOrderID
	createdAt := orderCreatedAt
	databaseErr := errors.New("database unavailable")
	tests := []struct {
		name        string
		rows        *pgxmock.Rows
		queryErr    error
		wantOrder   *model.Order
		wantErr     error
		wantScanErr bool
	}{
		{
			name: "order found",
			rows: newOrderRows().AddRow(orderID, ValidBuyerID, ValidListingID, 5, createdAt),
			wantOrder: &model.Order{
				ID: orderID, BuyerID: ValidBuyerID, ListingID: ValidListingID, Qty: 5,
				CreatedAt: createdAt.UTC().Format(time.RFC3339Nano),
			},
		},
		{name: "no order for buyer", rows: newOrderRows(), wantErr: pgx.ErrNoRows},
		{name: "database error", queryErr: databaseErr, wantErr: databaseErr},
		{name: "deadline exceeded", queryErr: context.DeadlineExceeded, wantErr: context.DeadlineExceeded},
		{name: "request canceled", queryErr: context.Canceled, wantErr: context.Canceled},
		{
			name:        "scan error",
			rows:        newOrderRows().AddRow(orderID, ValidBuyerID, ValidListingID, "invalid quantity", createdAt),
			wantScanErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := newTestPool(t, pgxmock.QueryMatcherEqual)

			query := pool.ExpectQuery(
				"SELECT id, buyer_id, listing_id, qty, created_at FROM orders WHERE id = $1 AND buyer_id = $2",
			).WithArgs(orderID, ValidBuyerID)
			if tt.queryErr != nil {
				query.WillReturnError(tt.queryErr)
			} else {
				query.WillReturnRows(tt.rows)
			}

			repo := NewPGRepository(pool, nil)
			order, err := repo.GetByID(context.Background(), orderID, ValidBuyerID)
			if tt.wantScanErr {
				if err == nil {
					t.Error("expected scan error")
				}
			} else if !errors.Is(err, tt.wantErr) {
				t.Errorf("error = %v, want %v", err, tt.wantErr)
			}
			if err != nil && !strings.HasPrefix(err.Error(), "get order by id: ") {
				t.Errorf("error = %v, want get order by id prefix", err)
			}
			if !reflect.DeepEqual(order, tt.wantOrder) {
				t.Errorf("order = %+v, want %+v", order, tt.wantOrder)
			}
		})
	}
}
