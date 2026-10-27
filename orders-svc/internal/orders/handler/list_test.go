package handler

import (
	"context"
	"errors"
	"fmt"
	"marketplace/gen/orderspb"
	"marketplace/orders-svc/internal/orders/dto"
	"marketplace/orders-svc/internal/orders/service"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestList_Pagination(t *testing.T) {
	tests := []struct {
		name       string
		limit      *int32
		offset     *int32
		wantLimit  int
		wantOffset int
	}{
		{name: "defaults", wantLimit: 20},
		{name: "limit only", limit: proto.Int32(100), wantLimit: 100},
		{name: "offset only", offset: proto.Int32(5), wantLimit: 20, wantOffset: 5},
		{name: "both parameters", limit: proto.Int32(1), offset: proto.Int32(7), wantLimit: 1, wantOffset: 7},
		{name: "explicit zero forwarded for validation", limit: proto.Int32(0)},
		{name: "negative offset forwarded for validation", offset: proto.Int32(-1), wantLimit: 20, wantOffset: -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			svc := &serviceStub{list: func(receivedCtx context.Context, buyerID string, limit, offset int) (*dto.ListResponse, error) {
				calls++
				if receivedCtx != ctx || buyerID != testBuyerID || limit != tt.wantLimit || offset != tt.wantOffset {
					t.Errorf("unexpected service arguments: buyer %q, limit %d, offset %d, same context %v", buyerID, limit, offset, receivedCtx == ctx)
				}
				return &dto.ListResponse{Total: 42}, nil
			}}
			response, err := NewOrdersServer(svc).List(ctx, &orderspb.ListOrdersRequest{BuyerId: testBuyerID, Limit: tt.limit, Offset: tt.offset})
			if err != nil {
				t.Fatal(err)
			}
			if !proto.Equal(response, &orderspb.ListOrdersResponse{Total: 42}) {
				t.Errorf("response = %v, want empty page with total 42", response)
			}
			if calls != 1 {
				t.Errorf("service calls = %d, want 1", calls)
			}
		})
	}
}

func TestList_MapsOrders(t *testing.T) {
	svc := &serviceStub{list: func(context.Context, string, int, int) (*dto.ListResponse, error) {
		return &dto.ListResponse{
			Total: 10,
			Orders: []dto.OrderResponse{
				{ID: testOrderID, BuyerID: testBuyerID, ListingID: testListingID, Qty: 5, CreatedAt: "2026-10-09T15:30:00.123456789+03:00"},
				{ID: "550e8400-e29b-41d4-a716-446655440002", BuyerID: testBuyerID, ListingID: "665e8411-e29b-41d4-a716-446655440001", Qty: 2, CreatedAt: "2026-10-08T12:30:00Z"},
			},
		}, nil
	}}
	response, err := NewOrdersServer(svc).List(context.Background(), &orderspb.ListOrdersRequest{BuyerId: testBuyerID})
	if err != nil {
		t.Fatal(err)
	}
	want := &orderspb.ListOrdersResponse{
		Total: 10,
		Orders: []*orderspb.Order{
			{Id: testOrderID, BuyerId: testBuyerID, ListingId: testListingID, Qty: 5, CreatedAt: timestamppb.New(time.Date(2026, 10, 9, 12, 30, 0, 123456789, time.UTC))},
			{Id: "550e8400-e29b-41d4-a716-446655440002", BuyerId: testBuyerID, ListingId: "665e8411-e29b-41d4-a716-446655440001", Qty: 2, CreatedAt: timestamppb.New(time.Date(2026, 10, 8, 12, 30, 0, 0, time.UTC))},
		},
	}
	if !proto.Equal(response, want) {
		t.Errorf("response = %v, want %v", response, want)
	}
}

func TestList_ServiceErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code codes.Code
	}{
		{name: "invalid limit", err: service.ErrInvalidLimit, code: codes.InvalidArgument},
		{name: "invalid offset", err: service.ErrInvalidOffset, code: codes.InvalidArgument},
		{name: "invalid buyer", err: service.ErrInvalidBuyerID, code: codes.InvalidArgument},
		{name: "canceled", err: context.Canceled, code: codes.Canceled},
		{name: "deadline", err: context.DeadlineExceeded, code: codes.DeadlineExceeded},
		{name: "unavailable", err: status.Error(codes.Unavailable, "service unavailable"), code: codes.Unavailable},
		{name: "internal error", err: errors.New("private database details"), code: codes.Internal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &serviceStub{list: func(context.Context, string, int, int) (*dto.ListResponse, error) {
				return nil, fmt.Errorf("list orders: %w", tt.err)
			}}
			response, err := NewOrdersServer(svc).List(context.Background(), &orderspb.ListOrdersRequest{BuyerId: testBuyerID})
			if response != nil || status.Code(err) != tt.code {
				t.Fatalf("response = %v, error = %v, want nil and %v", response, err, tt.code)
			}
			if tt.code == codes.Internal && status.Convert(err).Message() != "internal server error" {
				t.Errorf("internal error exposed service details: %v", err)
			}
		})
	}
}

func TestList_InvalidTimestamp(t *testing.T) {
	svc := &serviceStub{list: func(context.Context, string, int, int) (*dto.ListResponse, error) {
		return &dto.ListResponse{Total: 2, Orders: []dto.OrderResponse{
			{ID: testOrderID, CreatedAt: "2026-10-09T12:30:00Z"},
			{ID: "550e8400-e29b-41d4-a716-446655440002", CreatedAt: "invalid timestamp"},
		}}, nil
	}}
	response, err := NewOrdersServer(svc).List(context.Background(), &orderspb.ListOrdersRequest{BuyerId: testBuyerID})
	if response != nil || status.Code(err) != codes.Internal || status.Convert(err).Message() != "invalid order timestamp" {
		t.Fatalf("response = %v, error = %v, want nil and invalid timestamp error", response, err)
	}
}
