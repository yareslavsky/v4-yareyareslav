package service

import (
	"context"
	"errors"
	"marketplace/orders-svc/internal/orders/dto"
	"marketplace/orders-svc/internal/orders/model"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestCreate_Validation(t *testing.T) {
	type testCase struct {
		name      string
		buyerID   string
		listingID string
		qty       int
		wantErr   error
	}
	tests := []testCase{
		{name: "zero quantity", buyerID: testBuyerID, listingID: testListingID, wantErr: ErrInvalidQty},
		{name: "negative quantity", buyerID: testBuyerID, listingID: testListingID, qty: -1, wantErr: ErrInvalidQty},
	}
	for _, value := range []string{"", "invalid", "z50e8400-e29b-41d4-a716-446655440000", strings.ReplaceAll(testBuyerID, "-", ""), "{" + testBuyerID + "}", "urn:uuid:" + testBuyerID} {
		tests = append(tests,
			testCase{name: "buyer " + value, buyerID: value, listingID: testListingID, qty: 1, wantErr: ErrInvalidBuyerID},
			testCase{name: "listing " + value, buyerID: testBuyerID, listingID: value, qty: 1, wantErr: ErrInvalidListingID},
		)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewOrdersService(nil, nil)
			response, err := svc.Create(context.Background(), dto.CreateOrderRequest{ListingID: tt.listingID, Qty: tt.qty}, tt.buyerID, "order-1")
			if !errors.Is(err, tt.wantErr) || response != nil {
				t.Fatalf("response = %+v, error = %v, want nil and %v", response, err, tt.wantErr)
			}
		})
	}
}

func TestCreate_Workflow(t *testing.T) {
	failure := errors.New("repository failure")
	cleanupFailure := errors.New("cleanup failure")
	catalogFailure := status.Error(codes.FailedPrecondition, "insufficient stock")
	tests := []struct {
		name     string
		key      string
		step     string
		wantErr  error
		replayed bool
		calls    []string
	}{
		{name: "without key", calls: []string{"catalog", "create"}},
		{name: "with key", key: "order-1", calls: []string{"reserve", "catalog", "create"}},
		{name: "key reservation fails", key: "order-1", step: "reserve", wantErr: failure, calls: []string{"reserve"}},
		{name: "catalog fails without key", step: "catalog", wantErr: catalogFailure, calls: []string{"catalog"}},
		{name: "catalog fails with key", key: "order-1", step: "catalog", wantErr: catalogFailure, calls: []string{"reserve", "catalog", "release"}},
		{name: "create fails without key", step: "create", wantErr: failure, calls: []string{"catalog", "create"}},
		{name: "create fails with key", key: "order-1", step: "create", wantErr: failure, calls: []string{"reserve", "catalog", "create", "release"}},
		{name: "cleanup failure preserves original error", key: "order-1", step: "cleanup", wantErr: failure, calls: []string{"reserve", "catalog", "create", "release"}},
		{name: "cleanup survives canceled request", key: "order-1", step: "cancel create", wantErr: context.Canceled, calls: []string{"reserve", "catalog", "create", "release"}},
		{name: "replay", key: "order-1", step: "replay", replayed: true, calls: []string{"reserve", "state", "get"}},
		{name: "state lookup fails", key: "order-1", step: "state", wantErr: failure, calls: []string{"reserve", "state"}},
		{name: "order lookup fails", key: "order-1", step: "get", wantErr: failure, calls: []string{"reserve", "state", "get"}},
		{name: "released key acquired again", key: "order-1", step: "reacquire", calls: []string{"reserve", "state", "reserve", "catalog", "create"}},
		{name: "reacquisition fails", key: "order-1", step: "reacquire error", wantErr: failure, calls: []string{"reserve", "state", "reserve"}},
		{name: "waiting canceled", key: "order-1", step: "wait", wantErr: context.Canceled, calls: []string{"reserve", "state"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var calls []string
			created := model.Order{ID: testOrderID, BuyerID: testBuyerID, ListingID: testListingID, Qty: 5, CreatedAt: "2026-10-09T12:30:00Z"}
			checkKey := func(buyerID, key string) {
				t.Helper()
				if buyerID != testBuyerID || key != tt.key {
					t.Errorf("buyer = %q, key = %q, want %q and %q", buyerID, key, testBuyerID, tt.key)
				}
			}
			reserveCount := 0
			repo := &repositoryStub{}
			repo.reserve = func(_ context.Context, buyerID, key string) (bool, error) {
				calls = append(calls, "reserve")
				checkKey(buyerID, key)
				reserveCount++
				if tt.step == "reserve" || (tt.step == "reacquire error" && reserveCount == 2) {
					return false, failure
				}
				if reserveCount == 1 {
					switch tt.step {
					case "replay", "state", "get", "reacquire", "reacquire error", "wait":
						return false, nil
					}
				}
				return true, nil
			}
			repo.state = func(ctx context.Context, buyerID, key string) (*model.IdempotencyKeyState, error) {
				calls = append(calls, "state")
				checkKey(buyerID, key)
				assertOperationContext(t, ctx)
				switch tt.step {
				case "state":
					return nil, failure
				case "reacquire", "reacquire error":
					return nil, nil
				case "wait":
					cancel()
					return &model.IdempotencyKeyState{}, nil
				}
				return &model.IdempotencyKeyState{OrderID: &created.ID}, nil
			}
			repo.get = func(_ context.Context, orderID, buyerID string) (*model.Order, error) {
				calls = append(calls, "get")
				if orderID != created.ID || buyerID != testBuyerID {
					t.Errorf("unexpected order lookup: %q, %q", orderID, buyerID)
				}
				if tt.step == "get" {
					return nil, failure
				}
				return &created, nil
			}
			repo.create = func(ctx context.Context, order model.Order, key string) (*model.Order, error) {
				calls = append(calls, "create")
				assertOperationContext(t, ctx)
				if order != (model.Order{BuyerID: testBuyerID, ListingID: testListingID, Qty: 5}) || key != tt.key {
					t.Errorf("unexpected create arguments: %+v, key %q", order, key)
				}
				switch tt.step {
				case "create", "cleanup":
					return nil, failure
				case "cancel create":
					cancel()
					return nil, context.Canceled
				}
				return &created, nil
			}
			repo.release = func(ctx context.Context, buyerID, key string) (bool, error) {
				calls = append(calls, "release")
				checkKey(buyerID, key)
				assertOperationContext(t, ctx)
				if tt.step == "cleanup" {
					return false, cleanupFailure
				}
				return true, nil
			}
			catalog := catalogFunc(func(ctx context.Context, listingID string, qty int) error {
				calls = append(calls, "catalog")
				assertOperationContext(t, ctx)
				if listingID != testListingID || qty != 5 {
					t.Errorf("unexpected reservation: %q, %d", listingID, qty)
				}
				if tt.step == "catalog" {
					return catalogFailure
				}
				return nil
			})

			svc := NewOrdersService(repo, catalog)
			response, err := svc.Create(ctx, dto.CreateOrderRequest{ListingID: testListingID, Qty: 5}, testBuyerID, tt.key)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				if response != nil {
					t.Errorf("response = %+v, want nil", response)
				}
			} else {
				want := &dto.CreateOrderResponse{ID: created.ID, BuyerID: created.BuyerID, ListingID: created.ListingID, Qty: created.Qty, CreatedAt: created.CreatedAt, Replayed: tt.replayed}
				if !reflect.DeepEqual(response, want) {
					t.Errorf("response = %+v, want %+v", response, want)
				}
			}
			if !reflect.DeepEqual(calls, tt.calls) {
				t.Errorf("calls = %v, want %v", calls, tt.calls)
			}
		})
	}
}
