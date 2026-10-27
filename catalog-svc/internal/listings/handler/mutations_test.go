package handler

import (
	"context"
	"errors"
	"fmt"
	"marketplace/catalog-svc/internal/listings/dto"
	"marketplace/catalog-svc/internal/listings/repository"
	"marketplace/catalog-svc/internal/listings/service"
	"marketplace/gen/catalogpb"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type mutationServiceStub struct {
	Service
	create func(context.Context, dto.CreateListingRequest) (*dto.ListingResponse, error)
	update func(context.Context, dto.UpdateListingRequest) (*dto.ListingResponse, error)
	delete func(context.Context, string, string) error
}

func (s mutationServiceStub) Create(ctx context.Context, req dto.CreateListingRequest) (*dto.ListingResponse, error) {
	return s.create(ctx, req)
}

func (s mutationServiceStub) Update(ctx context.Context, req dto.UpdateListingRequest) (*dto.ListingResponse, error) {
	return s.update(ctx, req)
}

func (s mutationServiceStub) Delete(ctx context.Context, id, sellerID string) error {
	return s.delete(ctx, id, sellerID)
}

func mutationListing() dto.ListingResponse {
	return dto.ListingResponse{
		ID: "listing", SellerID: "seller", Title: "Молоко", Price: 1 << 40,
		Stock: 1 << 41, Sold: 1 << 42, CreatedAt: "2026-10-13T12:00:00.123456789+03:00",
	}
}

func assertProtoListing(t *testing.T, got *catalogpb.Listing, want dto.ListingResponse) {
	t.Helper()
	wantTime, err := time.Parse(time.RFC3339Nano, want.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Id != want.ID || got.SellerId != want.SellerID || got.Title != want.Title ||
		got.Price != want.Price || got.Stock != want.Stock || got.Sold != want.Sold ||
		got.CreatedAt == nil || !got.CreatedAt.AsTime().Equal(wantTime) {
		t.Fatalf("listing = %v, want %+v", got, want)
	}
}

func TestCreate(t *testing.T) {
	zero, large := int64(0), int64(1<<40)
	for _, tc := range []struct {
		name  string
		stock *int64
	}{
		{name: "omitted stock"},
		{name: "zero stock", stock: &zero},
		{name: "large stock", stock: &large},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stock := tc.stock
			ctx := t.Context()
			want := mutationListing()
			calls := 0
			h := NewGRPCHandler(mutationServiceStub{create: func(gotCtx context.Context, req dto.CreateListingRequest) (*dto.ListingResponse, error) {
				calls++
				if gotCtx != ctx || req != (dto.CreateListingRequest{SellerID: want.SellerID, Title: want.Title, Price: want.Price, Stock: stock}) {
					t.Fatalf("unexpected request: %+v", req)
				}
				return &want, nil
			}})
			got, err := h.Create(ctx, &catalogpb.CreateCatalogRequest{SellerId: want.SellerID, Title: want.Title, Price: want.Price, Stock: stock})
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("service calls = %d, want 1", calls)
			}
			assertProtoListing(t, got.Listing, want)
		})
	}
}

func TestUpdate(t *testing.T) {
	title, price, zero := "updated", int64(1<<40), int64(0)
	for _, tc := range []struct {
		name    string
		request dto.UpdateListingRequest
	}{
		{name: "omitted fields"},
		{name: "title only", request: dto.UpdateListingRequest{Title: &title}},
		{name: "price only", request: dto.UpdateListingRequest{Price: &price}},
		{name: "zero stock", request: dto.UpdateListingRequest{Stock: &zero}},
		{name: "all fields", request: dto.UpdateListingRequest{Title: &title, Price: &price, Stock: &zero}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			tc.request.ID, tc.request.SellerID = "listing", "seller"
			want := mutationListing()
			calls := 0
			h := NewGRPCHandler(mutationServiceStub{update: func(gotCtx context.Context, req dto.UpdateListingRequest) (*dto.ListingResponse, error) {
				calls++
				if gotCtx != ctx || req != tc.request {
					t.Fatalf("unexpected request: %+v", req)
				}
				return &want, nil
			}})
			got, err := h.Update(ctx, &catalogpb.UpdateCatalogRequest{
				Id: tc.request.ID, SellerId: tc.request.SellerID, Title: tc.request.Title, Price: tc.request.Price, Stock: tc.request.Stock,
			})
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("service calls = %d, want 1", calls)
			}
			assertProtoListing(t, got.Listing, want)
		})
	}
}

func TestCreateAndUpdateErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		listing *dto.ListingResponse
		err     error
		code    codes.Code
	}{
		{name: "validation", err: fmt.Errorf("service: %w", service.ErrValidation), code: codes.InvalidArgument},
		{name: "not found", err: fmt.Errorf("service: %w", repository.ErrNotFound), code: codes.NotFound},
		{name: "forbidden", err: fmt.Errorf("service: %w", repository.ErrForbidden), code: codes.PermissionDenied},
		{name: "internal", err: errors.New("database password must not leak"), code: codes.Internal},
		{name: "invalid timestamp", listing: &dto.ListingResponse{CreatedAt: "invalid"}, code: codes.Internal},
		{name: "timestamp out of range", listing: &dto.ListingResponse{CreatedAt: "0000-01-01T00:00:00Z"}, code: codes.Internal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := NewGRPCHandler(mutationServiceStub{
				create: func(context.Context, dto.CreateListingRequest) (*dto.ListingResponse, error) {
					return tc.listing, tc.err
				},
				update: func(context.Context, dto.UpdateListingRequest) (*dto.ListingResponse, error) {
					return tc.listing, tc.err
				},
			})
			created, err := h.Create(t.Context(), &catalogpb.CreateCatalogRequest{})
			if created != nil || status.Code(err) != tc.code {
				t.Fatalf("create response = %v, error = %v", created, err)
			}
			updated, err := h.Update(t.Context(), &catalogpb.UpdateCatalogRequest{})
			if updated != nil || status.Code(err) != tc.code {
				t.Fatalf("update response = %v, error = %v", updated, err)
			}
		})
	}
}

func TestDelete(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code codes.Code
	}{
		{name: "success", code: codes.OK},
		{name: "missing", err: fmt.Errorf("delete: %w", repository.ErrNotFound), code: codes.NotFound},
		{name: "another seller", err: fmt.Errorf("delete: %w", repository.ErrForbidden), code: codes.PermissionDenied},
		{name: "validation", err: service.ErrValidation, code: codes.InvalidArgument},
		{name: "canceled", err: context.Canceled, code: codes.Canceled},
		{name: "deadline", err: context.DeadlineExceeded, code: codes.DeadlineExceeded},
		{name: "database failure", err: errors.New("database unavailable"), code: codes.Internal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			calls := 0
			h := NewGRPCHandler(mutationServiceStub{delete: func(gotCtx context.Context, id, sellerID string) error {
				calls++
				if gotCtx != ctx || id != "listing" || sellerID != "seller" {
					t.Fatal("unexpected delete arguments")
				}
				return tc.err
			}})
			got, err := h.Delete(ctx, &catalogpb.DeleteCatalogRequest{Id: "listing", SellerId: "seller"})
			if calls != 1 || status.Code(err) != tc.code {
				t.Fatalf("calls = %d, error = %v, want %v", calls, err, tc.code)
			}
			if (got == nil) != (tc.err != nil) {
				t.Fatalf("response = %v, error = %v", got, err)
			}
		})
	}
}
