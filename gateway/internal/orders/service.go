package orders

import (
	"context"
	"marketplace/gen/orderspb"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

type Order struct {
	ID        string `json:"id"`
	BuyerID   string `json:"buyer_id"`
	ListingID string `json:"listing_id"`
	Qty       int32  `json:"qty"`
	CreatedAt string `json:"created_at"`
}

type OrdersService struct{ client orderspb.OrdersServiceClient }

func NewOrdersService(client orderspb.OrdersServiceClient) *OrdersService {
	return &OrdersService{client: client}
}

func toOrder(order *orderspb.Order) Order {
	return Order{
		ID: order.GetId(), BuyerID: order.GetBuyerId(), ListingID: order.GetListingId(),
		Qty: order.GetQty(), CreatedAt: order.GetCreatedAt().AsTime().UTC().Format(time.RFC3339Nano),
	}
}

func (s *OrdersService) CreateOrders(ctx context.Context, request *orderspb.CreateOrderRequest) (Order, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var headers metadata.MD
	result, err := s.client.Create(ctx, request, grpc.Header(&headers))
	if err != nil {
		return Order{}, false, err
	}
	replayed := false
	if values := headers.Get("x-idempotent-replay"); len(values) > 0 {
		replayed = values[0] == "true"
	}
	return toOrder(result.GetOrder()), replayed, nil
}

func (s *OrdersService) FindOrders(ctx context.Context, buyerID string, limit, offset int32) ([]Order, int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	result, err := s.client.List(ctx, &orderspb.ListOrdersRequest{BuyerId: buyerID, Limit: &limit, Offset: &offset})
	if err != nil {
		return nil, 0, err
	}
	orders := make([]Order, 0, len(result.GetOrders()))
	for _, order := range result.GetOrders() {
		orders = append(orders, toOrder(order))
	}
	return orders, result.GetTotal(), nil
}
