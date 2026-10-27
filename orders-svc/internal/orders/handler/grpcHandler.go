package handler

import (
	"context"
	"errors"
	"marketplace/gen/orderspb"
	"marketplace/orders-svc/internal/orders/dto"
	"marketplace/orders-svc/internal/orders/service"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Service interface {
	Create(
		ctx context.Context,
		request dto.CreateOrderRequest,
		buyerID, idempotencyKey string,
	) (*dto.CreateOrderResponse, error)

	List(
		ctx context.Context,
		buyerID string,
		limit, offset int,
	) (*dto.ListResponse, error)
}
type OrdersServer struct {
	orderspb.UnimplementedOrdersServiceServer
	service Service
}

func NewOrdersServer(service Service) *OrdersServer {
	return &OrdersServer{
		service: service,
	}
}

func (s *OrdersServer) Create(
	ctx context.Context,
	req *orderspb.CreateOrderRequest,
) (*orderspb.CreateOrderResponse, error) {
	order, err := s.service.Create(
		ctx,
		dto.CreateOrderRequest{
			ListingID: req.GetListingId(),
			Qty:       int(req.GetQty()),
		},
		req.GetBuyerId(),
		req.GetIdempotencyKey(),
	)
	if err != nil {
		return nil, toGRPCError(err)
	}

	result, err := toProtoOrder(dto.OrderResponse{
		ID:        order.ID,
		BuyerID:   order.BuyerID,
		ListingID: order.ListingID,
		Qty:       order.Qty,
		CreatedAt: order.CreatedAt,
	})
	if err != nil {
		return nil, status.Error(codes.Internal, "invalid order timestamp")
	}

	if order.Replayed {
		if err := grpc.SetHeader(ctx, metadata.Pairs("x-idempotent-replay", "true")); err != nil {
			return nil, status.Error(codes.Internal, "set replay header failed")
		}
	}

	return &orderspb.CreateOrderResponse{Order: result}, nil
}

func (s *OrdersServer) List(
	ctx context.Context,
	req *orderspb.ListOrdersRequest,
) (*orderspb.ListOrdersResponse, error) {
	limit, offset := 20, 0
	if req.Limit != nil {
		limit = int(*req.Limit)
	}
	if req.Offset != nil {
		offset = int(*req.Offset)
	}

	result, err := s.service.List(ctx, req.GetBuyerId(), limit, offset)
	if err != nil {
		return nil, toGRPCError(err)
	}

	orders := make([]*orderspb.Order, 0, len(result.Orders))
	for _, order := range result.Orders {
		item, err := toProtoOrder(order)
		if err != nil {
			return nil, status.Error(codes.Internal, "invalid order timestamp")
		}
		orders = append(orders, item)
	}

	return &orderspb.ListOrdersResponse{
		Orders: orders,
		Total:  result.Total,
	}, nil
}

func toProtoOrder(order dto.OrderResponse) (*orderspb.Order, error) {
	createdAt, err := time.Parse(time.RFC3339Nano, order.CreatedAt)
	if err != nil {
		return nil, err
	}

	return &orderspb.Order{
		Id:        order.ID,
		BuyerId:   order.BuyerID,
		ListingId: order.ListingID,
		Qty:       int32(order.Qty),
		CreatedAt: timestamppb.New(createdAt),
	}, nil
}

func toGRPCError(err error) error {
	if err == nil {
		return nil
	}

	switch {
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "request canceled")

	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "request deadline exceeded")

	case errors.Is(err, service.ErrInvalidLimit):
		return status.Error(codes.InvalidArgument, "limit must be between 1 and 100")

	case errors.Is(err, service.ErrInvalidOffset):
		return status.Error(codes.InvalidArgument, "offset must be non-negative")

	case errors.Is(err, service.ErrInvalidQty):
		return status.Error(codes.InvalidArgument, "qty must be positive")

	case errors.Is(err, service.ErrInvalidBuyerID):
		return status.Error(codes.InvalidArgument, "buyer_id must be a valid UUID")

	case errors.Is(err, service.ErrInvalidListingID):
		return status.Error(codes.InvalidArgument, "listing_id must be a valid UUID")
	}

	if st, ok := status.FromError(err); ok {
		switch st.Code() {
		case codes.InvalidArgument,
			codes.NotFound,
			codes.FailedPrecondition,
			codes.AlreadyExists,
			codes.Aborted,
			codes.Unavailable,
			codes.DeadlineExceeded,
			codes.Canceled:
			return st.Err()
		}
	}

	return status.Error(codes.Internal, "internal server error")
}
