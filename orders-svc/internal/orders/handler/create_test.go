package handler

import (
	"context"
	"errors"
	"fmt"
	"marketplace/gen/orderspb"
	"marketplace/orders-svc/internal/orders/dto"
	"marketplace/orders-svc/internal/orders/service"
	"reflect"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func testCreateResponse() *dto.CreateOrderResponse {
	return &dto.CreateOrderResponse{
		ID: testOrderID, BuyerID: testBuyerID, ListingID: testListingID, Qty: 5,
		CreatedAt: "2026-10-09T15:30:00.123456789+03:00",
	}
}

func TestCreate_Success(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		replayed bool
	}{
		{name: "new order without key"},
		{name: "new order with key", key: "order-1"},
		{name: "replayed order", key: "order-1", replayed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stream := &transportStreamStub{}
			ctx := grpc.NewContextWithServerTransportStream(context.Background(), stream)
			calls := 0
			svc := &serviceStub{create: func(receivedCtx context.Context, request dto.CreateOrderRequest, buyerID, key string) (*dto.CreateOrderResponse, error) {
				calls++
				if receivedCtx != ctx || request != (dto.CreateOrderRequest{ListingID: testListingID, Qty: 5}) || buyerID != testBuyerID || key != tt.key {
					t.Errorf("unexpected service arguments: %+v, buyer %q, key %q, same context %v", request, buyerID, key, receivedCtx == ctx)
				}
				order := testCreateResponse()
				order.Replayed = tt.replayed
				return order, nil
			}}
			server := NewOrdersServer(svc)
			response, err := server.Create(ctx, &orderspb.CreateOrderRequest{
				BuyerId: testBuyerID, ListingId: testListingID, Qty: 5, IdempotencyKey: tt.key,
			})
			if err != nil {
				t.Fatal(err)
			}
			want := &orderspb.CreateOrderResponse{Order: &orderspb.Order{
				Id: testOrderID, BuyerId: testBuyerID, ListingId: testListingID, Qty: 5,
				CreatedAt: timestamppb.New(time.Date(2026, 10, 9, 12, 30, 0, 123456789, time.UTC)),
			}}
			if !proto.Equal(response, want) {
				t.Errorf("response = %v, want %v", response, want)
			}
			var wantHeader []string
			wantHeaderCalls := 0
			if tt.replayed {
				wantHeader = []string{"true"}
				wantHeaderCalls = 1
			}
			if !reflect.DeepEqual(stream.headers.Get("x-idempotent-replay"), wantHeader) || stream.headerCalls != wantHeaderCalls {
				t.Errorf("replay header = %v, calls = %d, want %v and %d", stream.headers.Get("x-idempotent-replay"), stream.headerCalls, wantHeader, wantHeaderCalls)
			}
			if calls != 1 {
				t.Errorf("service calls = %d, want 1", calls)
			}
		})
	}
}

func TestCreate_ServiceErrors(t *testing.T) {
	type testCase struct {
		name string
		err  error
		code codes.Code
	}
	tests := []testCase{
		{name: "invalid quantity", err: service.ErrInvalidQty, code: codes.InvalidArgument},
		{name: "invalid buyer", err: service.ErrInvalidBuyerID, code: codes.InvalidArgument},
		{name: "invalid listing", err: service.ErrInvalidListingID, code: codes.InvalidArgument},
		{name: "request canceled", err: context.Canceled, code: codes.Canceled},
		{name: "deadline exceeded", err: context.DeadlineExceeded, code: codes.DeadlineExceeded},
		{name: "repository failure", err: errors.New("database credentials must not escape"), code: codes.Internal},
	}
	for _, code := range []codes.Code{codes.InvalidArgument, codes.NotFound, codes.FailedPrecondition, codes.AlreadyExists, codes.Aborted, codes.Unavailable, codes.DeadlineExceeded, codes.Canceled} {
		tests = append(tests, testCase{name: code.String(), err: status.Error(code, "catalog failure"), code: code})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stream := &transportStreamStub{}
			ctx := grpc.NewContextWithServerTransportStream(context.Background(), stream)
			svc := &serviceStub{create: func(context.Context, dto.CreateOrderRequest, string, string) (*dto.CreateOrderResponse, error) {
				return nil, fmt.Errorf("create order: %w", tt.err)
			}}
			response, err := NewOrdersServer(svc).Create(ctx, &orderspb.CreateOrderRequest{})
			if response != nil || status.Code(err) != tt.code {
				t.Fatalf("response = %v, error = %v, want nil and %v", response, err, tt.code)
			}
			if tt.code == codes.Internal && status.Convert(err).Message() != "internal server error" {
				t.Errorf("internal error exposed service details: %v", err)
			}
			if stream.headerCalls != 0 {
				t.Error("headers set on service error")
			}
		})
	}
}

func TestCreate_InvalidTimestamp(t *testing.T) {
	stream := &transportStreamStub{}
	ctx := grpc.NewContextWithServerTransportStream(context.Background(), stream)
	svc := &serviceStub{create: func(context.Context, dto.CreateOrderRequest, string, string) (*dto.CreateOrderResponse, error) {
		order := testCreateResponse()
		order.CreatedAt = "invalid timestamp"
		order.Replayed = true
		return order, nil
	}}
	response, err := NewOrdersServer(svc).Create(ctx, &orderspb.CreateOrderRequest{})
	if response != nil || status.Code(err) != codes.Internal || status.Convert(err).Message() != "invalid order timestamp" {
		t.Fatalf("response = %v, error = %v, want nil and invalid timestamp error", response, err)
	}
	if stream.headerCalls != 0 {
		t.Error("replay header set before timestamp validation")
	}
}

func TestCreate_ReplayHeaderFailure(t *testing.T) {
	stream := &transportStreamStub{headerErr: errors.New("transport failed")}
	ctx := grpc.NewContextWithServerTransportStream(context.Background(), stream)
	svc := &serviceStub{create: func(context.Context, dto.CreateOrderRequest, string, string) (*dto.CreateOrderResponse, error) {
		order := testCreateResponse()
		order.Replayed = true
		return order, nil
	}}
	response, err := NewOrdersServer(svc).Create(ctx, &orderspb.CreateOrderRequest{})
	if response != nil || status.Code(err) != codes.Internal || status.Convert(err).Message() != "set replay header failed" {
		t.Fatalf("response = %v, error = %v, want nil and replay header error", response, err)
	}
	if stream.headerCalls != 1 {
		t.Errorf("header calls = %d, want 1", stream.headerCalls)
	}
}
