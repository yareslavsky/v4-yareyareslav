package service

import (
	"context"
	"errors"
	"marketplace/catalog-svc/internal/listings/dto"
	"marketplace/catalog-svc/internal/listings/model"
	"reflect"
	"testing"
	"time"
)

const testListingID = "665e8411-e29b-41d4-a716-446655440000"
const testSellerID = "550e8400-e29b-41d4-a716-446655440000"
const testEventID = "550e8400-e29b-41d4-a716-446655440001"

type repositoryStub struct {
	Repository
	create  func(context.Context, model.Listing) (*model.Listing, error)
	get     func(context.Context, string) (*model.Listing, error)
	list    func(context.Context, int, int) ([]model.Listing, int64, error)
	update  func(context.Context, model.UpdateListing) (*model.Listing, error)
	delete  func(context.Context, string, string) error
	reserve func(context.Context, string, int64) (int64, error)
	apply   func(context.Context, string, string, int64) (bool, error)
}

func (r *repositoryStub) Create(ctx context.Context, listing model.Listing) (*model.Listing, error) {
	return r.create(ctx, listing)
}
func (r *repositoryStub) Get(ctx context.Context, id string) (*model.Listing, error) {
	return r.get(ctx, id)
}
func (r *repositoryStub) List(ctx context.Context, limit, offset int) ([]model.Listing, int64, error) {
	return r.list(ctx, limit, offset)
}
func (r *repositoryStub) Update(ctx context.Context, listing model.UpdateListing) (*model.Listing, error) {
	return r.update(ctx, listing)
}
func (r *repositoryStub) Delete(ctx context.Context, id, sellerID string) error {
	return r.delete(ctx, id, sellerID)
}
func (r *repositoryStub) Reserve(ctx context.Context, id string, qty int64) (int64, error) {
	return r.reserve(ctx, id, qty)
}
func (r *repositoryStub) ApplyOrderCreated(ctx context.Context, eventID, listingID string, qty int64) (bool, error) {
	return r.apply(ctx, eventID, listingID, qty)
}

type cacheStub struct {
	get    func(context.Context, string) (*model.Listing, error)
	set    func(context.Context, string, model.Listing, time.Duration) error
	delete func(context.Context, ...string) (bool, error)
}

func (c *cacheStub) Get(ctx context.Context, key string) (*model.Listing, error) {
	return c.get(ctx, key)
}
func (c *cacheStub) Set(ctx context.Context, key string, listing model.Listing, ttl time.Duration) error {
	return c.set(ctx, key, listing, ttl)
}
func (c *cacheStub) Delete(ctx context.Context, keys ...string) (bool, error) {
	return c.delete(ctx, keys...)
}

func testListing() model.Listing {
	return model.Listing{
		ID: testListingID, SellerID: testSellerID, Title: "Молоко", Price: 12345,
		Stock: 1 << 40, Sold: 1 << 41, CreatedAt: "2026-10-13T09:00:00.123456789Z",
	}
}

func testListingResponse() dto.ListingResponse {
	return dto.ListingResponse{
		ID: testListingID, SellerID: testSellerID, Title: "Молоко", Price: 12345,
		Stock: 1 << 40, Sold: 1 << 41, CreatedAt: "2026-10-13T09:00:00.123456789Z",
	}
}

func testOrderCreated() dto.OrderCreatedEvent {
	return dto.OrderCreatedEvent{
		EventID: testEventID, OrderID: testSellerID, BuyerID: testSellerID,
		ListingID: testListingID, Qty: 1 << 40, CreatedAt: "2026-10-13T09:00:00Z",
	}
}

func assertCacheContext(t *testing.T, ctx context.Context) {
	t.Helper()
	deadline, ok := ctx.Deadline()
	if ctx.Err() != nil || !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 200*time.Millisecond {
		t.Fatalf("invalid cache context: error %v, deadline %v", ctx.Err(), deadline)
	}
}

func assertValidationFields(t *testing.T, err error, want []string) {
	t.Helper()
	var validationErr *ValidationError
	if !errors.Is(err, ErrValidation) || !errors.As(err, &validationErr) {
		t.Fatalf("error = %v, want ValidationError", err)
	}
	fields := make([]string, 0, len(validationErr.Fields))
	for _, field := range validationErr.Fields {
		if field.Message == "" {
			t.Fatal("empty validation message")
		}
		fields = append(fields, field.Field)
	}
	if !reflect.DeepEqual(fields, want) {
		t.Fatalf("validation fields = %v, want %v", fields, want)
	}
}
