package service

import (
	"context"
	"errors"
	"marketplace/orders-svc/internal/orders/dto"
	"marketplace/orders-svc/internal/orders/model"
	"reflect"
	"strings"
	"testing"
)

func TestList_Validation(t *testing.T) {
	type testCase struct {
		name    string
		buyerID string
		limit   int
		offset  int
		wantErr error
	}
	tests := []testCase{
		{name: "zero limit", buyerID: testBuyerID, wantErr: ErrInvalidLimit},
		{name: "negative limit", buyerID: testBuyerID, limit: -1, wantErr: ErrInvalidLimit},
		{name: "limit above maximum", buyerID: testBuyerID, limit: 101, wantErr: ErrInvalidLimit},
		{name: "negative offset", buyerID: testBuyerID, limit: 20, offset: -1, wantErr: ErrInvalidOffset},
	}
	for _, buyerID := range []string{"", "invalid", "z50e8400-e29b-41d4-a716-446655440000", strings.ReplaceAll(testBuyerID, "-", ""), "{" + testBuyerID + "}", "urn:uuid:" + testBuyerID} {
		tests = append(tests, testCase{name: "buyer " + buyerID, buyerID: buyerID, limit: 20, wantErr: ErrInvalidBuyerID})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewOrdersService(nil, nil)
			response, err := svc.List(context.Background(), tt.buyerID, tt.limit, tt.offset)
			if !errors.Is(err, tt.wantErr) || response != nil {
				t.Fatalf("response = %+v, error = %v, want nil and %v", response, err, tt.wantErr)
			}
		})
	}
}

func TestList_Success(t *testing.T) {
	orders := []model.Order{
		{ID: testOrderID, BuyerID: testBuyerID, ListingID: testListingID, Qty: 5, CreatedAt: "2026-10-09T12:30:00Z"},
		{ID: "550e8400-e29b-41d4-a716-446655440002", BuyerID: testBuyerID, ListingID: "665e8411-e29b-41d4-a716-446655440001", Qty: 2, CreatedAt: "2026-10-08T12:30:00Z"},
	}
	tests := []struct {
		name   string
		limit  int
		offset int
		orders []model.Order
		total  int64
		want   []dto.OrderResponse
	}{
		{
			name: "maps page and preserves order", limit: 20, offset: 3, orders: orders, total: 42,
			want: []dto.OrderResponse{
				{ID: testOrderID, BuyerID: testBuyerID, ListingID: testListingID, Qty: 5, CreatedAt: "2026-10-09T12:30:00Z"},
				{ID: "550e8400-e29b-41d4-a716-446655440002", BuyerID: testBuyerID, ListingID: "665e8411-e29b-41d4-a716-446655440001", Qty: 2, CreatedAt: "2026-10-08T12:30:00Z"},
			},
		},
		{name: "minimum limit and zero offset", limit: 1, want: []dto.OrderResponse{}},
		{name: "maximum limit", limit: 100, orders: []model.Order{}, want: []dto.OrderResponse{}},
		{name: "offset beyond last order", limit: 20, offset: 100, total: 42, want: []dto.OrderResponse{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			repo := &repositoryStub{list: func(receivedCtx context.Context, buyerID string, limit, offset int) ([]model.Order, int64, error) {
				calls++
				if receivedCtx != ctx || buyerID != testBuyerID || limit != tt.limit || offset != tt.offset {
					t.Errorf("unexpected repository arguments: buyer %q, limit %d, offset %d, same context %v", buyerID, limit, offset, receivedCtx == ctx)
				}
				return tt.orders, tt.total, nil
			}}
			svc := NewOrdersService(repo, nil)
			response, err := svc.List(ctx, testBuyerID, tt.limit, tt.offset)
			if err != nil {
				t.Fatal(err)
			}
			want := &dto.ListResponse{Orders: tt.want, Total: tt.total}
			if !reflect.DeepEqual(response, want) {
				t.Errorf("response = %+v, want %+v", response, want)
			}
			if calls != 1 {
				t.Errorf("repository calls = %d, want 1", calls)
			}
		})
	}
}

func TestList_RepositoryError(t *testing.T) {
	for _, failure := range []error{errors.New("repository failure"), context.Canceled, context.DeadlineExceeded} {
		t.Run(failure.Error(), func(t *testing.T) {
			calls := 0
			repo := &repositoryStub{list: func(context.Context, string, int, int) ([]model.Order, int64, error) {
				calls++
				return []model.Order{{ID: testOrderID}}, 42, failure
			}}
			svc := NewOrdersService(repo, nil)
			response, err := svc.List(context.Background(), testBuyerID, 20, 0)
			if !errors.Is(err, failure) || response != nil {
				t.Fatalf("response = %+v, error = %v, want nil and wrapped %v", response, err, failure)
			}
			if !strings.HasPrefix(err.Error(), "list orders: ") {
				t.Errorf("error = %v, want list orders prefix", err)
			}
			if calls != 1 {
				t.Errorf("repository calls = %d, want 1", calls)
			}
		})
	}
}
