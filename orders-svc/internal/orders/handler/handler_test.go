package handler

import (
	"context"
	"marketplace/orders-svc/internal/orders/dto"

	"google.golang.org/grpc/metadata"
)

const testBuyerID = "550e8400-e29b-41d4-a716-446655440000"
const testListingID = "665e8411-e29b-41d4-a716-446655440000"
const testOrderID = "550e8400-e29b-41d4-a716-446655440001"

type serviceStub struct {
	Service
	create func(context.Context, dto.CreateOrderRequest, string, string) (*dto.CreateOrderResponse, error)
	list   func(context.Context, string, int, int) (*dto.ListResponse, error)
}

func (s *serviceStub) Create(ctx context.Context, request dto.CreateOrderRequest, buyerID, key string) (*dto.CreateOrderResponse, error) {
	return s.create(ctx, request, buyerID, key)
}

func (s *serviceStub) List(ctx context.Context, buyerID string, limit, offset int) (*dto.ListResponse, error) {
	return s.list(ctx, buyerID, limit, offset)
}

type transportStreamStub struct {
	headers     metadata.MD
	headerErr   error
	headerCalls int
}

func (s *transportStreamStub) Method() string {
	return "/marketplace.orders.v1.OrdersService/Create"
}

func (s *transportStreamStub) SetHeader(md metadata.MD) error {
	s.headerCalls++
	if s.headerErr != nil {
		return s.headerErr
	}
	s.headers = metadata.Join(s.headers, md)
	return nil
}

func (s *transportStreamStub) SendHeader(md metadata.MD) error {
	return s.SetHeader(md)
}

func (s *transportStreamStub) SetTrailer(metadata.MD) error {
	return nil
}
