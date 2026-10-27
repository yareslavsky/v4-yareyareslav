package service

import (
	"context"
	"marketplace/orders-svc/internal/orders/model"
	"testing"
	"time"
)

const testBuyerID = "550e8400-e29b-41d4-a716-446655440000"
const testListingID = "665e8411-e29b-41d4-a716-446655440000"
const testOrderID = "550e8400-e29b-41d4-a716-446655440001"

type repositoryStub struct {
	Repository
	create  func(context.Context, model.Order, string) (*model.Order, error)
	reserve func(context.Context, string, string) (bool, error)
	release func(context.Context, string, string) (bool, error)
	state   func(context.Context, string, string) (*model.IdempotencyKeyState, error)
	get     func(context.Context, string, string) (*model.Order, error)
	list    func(context.Context, string, int, int) ([]model.Order, int64, error)
}

func (r *repositoryStub) Create(ctx context.Context, order model.Order, key string) (*model.Order, error) {
	return r.create(ctx, order, key)
}

func (r *repositoryStub) CreateReserved(ctx context.Context, order model.Order, key string, reserve, release func(context.Context, string) error) (*model.Order, error) {
	reserveCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	err := reserve(reserveCtx, testOrderID)
	cancel()
	var created *model.Order
	if err == nil {
		createCtx, cancelCreate := context.WithTimeout(ctx, 2*time.Second)
		created, err = r.create(createCtx, order, key)
		cancelCreate()
	}
	if err != nil && key != "" {
		cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancelCleanup()
		_, _ = r.release(cleanupCtx, order.BuyerID, key)
	}
	return created, err
}

func (r *repositoryStub) ReserveIdempotencyKey(ctx context.Context, buyerID, key string) (bool, error) {
	return r.reserve(ctx, buyerID, key)
}

func (r *repositoryStub) ReleaseIdempotencyKey(ctx context.Context, buyerID, key string) (bool, error) {
	return r.release(ctx, buyerID, key)
}

func (r *repositoryStub) GetIdempotencyKeyState(ctx context.Context, buyerID, key string) (*model.IdempotencyKeyState, error) {
	return r.state(ctx, buyerID, key)
}

func (r *repositoryStub) GetByID(ctx context.Context, orderID, buyerID string) (*model.Order, error) {
	return r.get(ctx, orderID, buyerID)
}

func (r *repositoryStub) List(ctx context.Context, buyerID string, limit, offset int) ([]model.Order, int64, error) {
	return r.list(ctx, buyerID, limit, offset)
}

type catalogFunc func(context.Context, string, int) error

func (f catalogFunc) Reserve(ctx context.Context, listingID string, qty int) error {
	return f(ctx, listingID, qty)
}

func (f catalogFunc) ReserveReservation(ctx context.Context, listingID string, qty int, reservationID string) error {
	return f(ctx, listingID, qty)
}

func (f catalogFunc) ReleaseReservation(context.Context, string) error { return nil }

func assertOperationContext(t *testing.T, ctx context.Context) {
	t.Helper()
	if err := ctx.Err(); err != nil {
		t.Errorf("operation context is already canceled: %v", err)
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 2*time.Second {
		t.Errorf("expected deadline within 2 seconds, got %v", deadline)
	}
}
