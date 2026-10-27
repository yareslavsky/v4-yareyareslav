package service

import (
	"marketplace/catalog-svc/internal/listings/dto"
	"testing"
)

func TestValidationAcceptsInt64Values(t *testing.T) {
	id := "665e8411-e29b-41d4-a716-446655440000"
	stock := int64(1 << 40)
	if err := validateCreate(dto.CreateListingRequest{SellerID: id, Title: "listing", Price: 100, Stock: &stock}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := validateUpdate(dto.UpdateListingRequest{ID: id, SellerID: id, Stock: &stock}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := validateReserve(dto.ReserveListingRequest{ListingID: id, Qty: stock}); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if err := validateOrderCreated(dto.OrderCreatedEvent{
		EventID: id, OrderID: id, BuyerID: id, ListingID: id,
		Qty: stock, CreatedAt: "2026-10-13T09:00:00Z",
	}); err != nil {
		t.Fatalf("order created: %v", err)
	}
}
