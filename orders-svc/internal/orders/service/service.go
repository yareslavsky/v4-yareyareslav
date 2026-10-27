package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"marketplace/orders-svc/internal/orders/dto"
	"marketplace/orders-svc/internal/orders/model"
	"time"

	"github.com/google/uuid"
)

type Repository interface {
	Create(ctx context.Context, order model.Order, idempKey string) (*model.Order, error)
	CreateReserved(context.Context, model.Order, string, func(context.Context, string) error, func(context.Context, string) error) (*model.Order, error)
	ReserveIdempotencyKey(ctx context.Context, buyerID, key string) (bool, error)
	ReleaseIdempotencyKey(ctx context.Context, buyerID, key string) (bool, error)
	GetIdempotencyKeyState(ctx context.Context, buyerID, key string) (*model.IdempotencyKeyState, error)
	GetByID(ctx context.Context, orderID, buyerID string) (*model.Order, error)
	List(ctx context.Context, buyerID string, limit, offset int) ([]model.Order, int64, error)
}

type Catalog interface {
	ReserveReservation(ctx context.Context, listingID string, qty int, reservationID string) error
	ReleaseReservation(context.Context, string) error
}

var ErrInvalidLimit = errors.New("limit out of range between 0 and 100")
var ErrInvalidOffset = errors.New("offset must be a positive number")
var ErrInvalidQty = errors.New("qty must be positive")
var ErrInvalidBuyerID = errors.New("buyer_id has invalid format")
var ErrInvalidListingID = errors.New("listing_id has invalid format")

type OrdersService struct {
	rep     Repository
	catalog Catalog
}

func NewOrdersService(rep Repository, catalog Catalog) *OrdersService {
	return &OrdersService{
		rep:     rep,
		catalog: catalog,
	}
}

func (s *OrdersService) Create(
	ctx context.Context,
	createDto dto.CreateOrderRequest,
	buyerID string,
	idempKey string,
) (response *dto.CreateOrderResponse, err error) {
	if createDto.Qty <= 0 {
		return nil, fmt.Errorf("create order: %w", ErrInvalidQty)
	}

	_, err = uuid.Parse(buyerID)
	if err != nil || len(buyerID) != 36 {
		return nil, fmt.Errorf("create order: %w", ErrInvalidBuyerID)
	}
	_, err = uuid.Parse(createDto.ListingID)
	if err != nil || len(createDto.ListingID) != 36 {
		return nil, fmt.Errorf("create order: %w", ErrInvalidListingID)
	}

	reservedKey := false
	defer func() {
		if !reservedKey || err == nil {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		if _, cleanupErr := s.rep.ReleaseIdempotencyKey(cleanupCtx, buyerID, idempKey); cleanupErr != nil {
			slog.WarnContext(cleanupCtx, "release idempotency key failed", "error", cleanupErr)
		}
	}()

	if idempKey != "" {
		res, err := s.rep.ReserveIdempotencyKey(ctx, buyerID, idempKey)
		if err != nil {
			return nil, fmt.Errorf("create order: %w", err)
		}
		reservedKey = res

		if !res {
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()

			for {
				if err := ctx.Err(); err != nil {
					return nil, fmt.Errorf("create order: %w", err)
				}

				stateCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
				state, err := s.rep.GetIdempotencyKeyState(stateCtx, buyerID, idempKey)
				cancel()
				if err != nil {
					return nil, fmt.Errorf("create order: %w", err)
				}

				if state != nil && state.OrderID != nil {
					order, err := s.rep.GetByID(ctx, *state.OrderID, buyerID)
					if err != nil {
						return nil, fmt.Errorf("create order: %w", err)
					}
					return &dto.CreateOrderResponse{
						ID:        order.ID,
						BuyerID:   order.BuyerID,
						ListingID: order.ListingID,
						Qty:       order.Qty,
						CreatedAt: order.CreatedAt,
						Replayed:  true,
					}, nil
				}

				if state == nil {
					reserved, err := s.rep.ReserveIdempotencyKey(ctx, buyerID, idempKey)
					if err != nil {
						return nil, fmt.Errorf("create order: %w", err)
					}
					if reserved {
						reservedKey = true
						break
					}
				}

				select {
				case <-ctx.Done():
					return nil, fmt.Errorf("create order: %w", ctx.Err())
				case <-ticker.C:
				}
			}
		}
	}

	order := model.Order{
		BuyerID:   buyerID,
		ListingID: createDto.ListingID,
		Qty:       createDto.Qty,
	}

	reservedKey = false
	createdOrder, err := s.rep.CreateReserved(ctx, order, idempKey, func(reserveCtx context.Context, reservationID string) error {
		return s.catalog.ReserveReservation(reserveCtx, createDto.ListingID, createDto.Qty, reservationID)
	}, s.catalog.ReleaseReservation)
	if err != nil {
		return nil, fmt.Errorf("create order: %w", err)
	}

	return &dto.CreateOrderResponse{
		ID:        createdOrder.ID,
		BuyerID:   createdOrder.BuyerID,
		ListingID: createdOrder.ListingID,
		Qty:       createdOrder.Qty,
		CreatedAt: createdOrder.CreatedAt,
	}, nil
}

func (s *OrdersService) List(
	ctx context.Context,
	buyerID string,
	limit,
	offset int,
) (*dto.ListResponse, error) {
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("list orders: %w", ErrInvalidLimit)
	}
	if offset < 0 {
		return nil, fmt.Errorf("list orders: %w", ErrInvalidOffset)
	}
	_, err := uuid.Parse(buyerID)
	if err != nil || len(buyerID) != 36 {
		return nil, fmt.Errorf("list orders: %w", ErrInvalidBuyerID)
	}

	orders, totalCount, err := s.rep.List(ctx, buyerID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list orders: %w", err)
	}

	listedOrders := make([]dto.OrderResponse, 0, len(orders))
	for _, order := range orders {
		listed := dto.OrderResponse{
			ID:        order.ID,
			BuyerID:   order.BuyerID,
			ListingID: order.ListingID,
			Qty:       order.Qty,
			CreatedAt: order.CreatedAt,
		}
		listedOrders = append(listedOrders, listed)
	}

	return &dto.ListResponse{
		Orders: listedOrders,
		Total:  totalCount,
	}, nil
}
