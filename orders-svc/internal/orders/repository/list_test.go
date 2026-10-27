package repository

import (
	"context"
	"errors"
	"marketplace/orders-svc/internal/orders/model"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v5"
)

const listOrdersSQL = "SELECT id, buyer_id, listing_id, qty, created_at FROM orders WHERE buyer_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2 OFFSET $3"
const countOrdersSQL = "SELECT COUNT(id) FROM orders WHERE buyer_id = $1"

func TestList_Success(t *testing.T) {
	newer := orderCreatedAt
	older := newer.Add(-time.Hour)
	tests := []struct {
		name   string
		limit  int
		offset int
		rows   *pgxmock.Rows
		want   []model.Order
		total  int64
	}{
		{
			name: "page of orders", limit: 2, offset: 3, total: 10,
			rows: newOrderRows().
				AddRow("550e8400-e29b-41d4-a716-446655440001", ValidBuyerID, ValidListingID, 5, newer).
				AddRow("550e8400-e29b-41d4-a716-446655440002", ValidBuyerID, ValidListingID, 2, older),
			want: []model.Order{
				{ID: "550e8400-e29b-41d4-a716-446655440001", BuyerID: ValidBuyerID, ListingID: ValidListingID, Qty: 5, CreatedAt: newer.UTC().Format(time.RFC3339Nano)},
				{ID: "550e8400-e29b-41d4-a716-446655440002", BuyerID: ValidBuyerID, ListingID: ValidListingID, Qty: 2, CreatedAt: older.UTC().Format(time.RFC3339Nano)},
			},
		},
		{name: "buyer has no orders", limit: 20, rows: newOrderRows()},
		{name: "offset beyond last order", limit: 20, offset: 100, rows: newOrderRows(), total: 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := newTestPool(t, pgxmock.QueryMatcherEqual)
			pool.ExpectQuery(listOrdersSQL).
				WithArgs(ValidBuyerID, tt.limit, tt.offset).
				WillReturnRows(tt.rows).RowsWillBeClosed()
			pool.ExpectQuery(countOrdersSQL).
				WithArgs(ValidBuyerID).
				WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(tt.total))

			repo := NewPGRepository(pool, nil)
			orders, total, err := repo.List(context.Background(), ValidBuyerID, tt.limit, tt.offset)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(orders, tt.want) {
				t.Errorf("orders = %+v, want %+v", orders, tt.want)
			}
			if total != tt.total {
				t.Errorf("total = %d, want %d", total, tt.total)
			}
		})
	}
}

func TestList_Failures(t *testing.T) {
	for _, step := range []string{"query", "scan", "iteration", "count query", "count scan"} {
		t.Run(step, func(t *testing.T) {
			pool := newTestPool(t, pgxmock.QueryMatcherEqual)
			failure := errors.New("database failure")
			query := pool.ExpectQuery(listOrdersSQL).WithArgs(ValidBuyerID, 20, 0)
			rows := newOrderRows().AddRow(ValidBuyerID, ValidBuyerID, ValidListingID, 1, time.Now())
			wantPrefix := ""
			switch step {
			case "query":
				query.WillReturnError(failure)
				wantPrefix = "get orders:"
			case "scan":
				query.WillReturnRows(newOrderRows().AddRow(ValidBuyerID, ValidBuyerID, ValidListingID, "invalid quantity", time.Now())).RowsWillBeClosed()
				wantPrefix = "orders rows scan:"
			case "iteration":
				rows.AddRow(ValidListingID, ValidBuyerID, ValidListingID, 2, time.Now()).RowError(2, failure)
				query.WillReturnRows(rows).RowsWillBeClosed()
				wantPrefix = "orders rows error:"
			case "count query", "count scan":
				query.WillReturnRows(rows).RowsWillBeClosed()
				count := pool.ExpectQuery(countOrdersSQL).WithArgs(ValidBuyerID)
				if step == "count query" {
					count.WillReturnError(failure)
				} else {
					count.WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow("invalid count"))
				}
				wantPrefix = "total orders count row scan:"
			}

			repo := NewPGRepository(pool, nil)
			orders, total, err := repo.List(context.Background(), ValidBuyerID, 20, 0)
			if err == nil || !strings.HasPrefix(err.Error(), wantPrefix) {
				t.Fatalf("error = %v, want prefix %q", err, wantPrefix)
			}
			if step != "scan" && step != "count scan" && !errors.Is(err, failure) {
				t.Errorf("error = %v, want wrapped database failure", err)
			}
			if orders != nil || total != 0 {
				t.Errorf("orders = %+v, total = %d, want nil and 0", orders, total)
			}
		})
	}
}
