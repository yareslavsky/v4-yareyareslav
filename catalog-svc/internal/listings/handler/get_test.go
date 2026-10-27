package handler

import (
	"context"
	"errors"
	"fmt"
	"marketplace/catalog-svc/internal/listings/dto"
	"marketplace/catalog-svc/internal/listings/repository"
	"marketplace/gen/catalogpb"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type getServiceStub struct {
	Service
	get func(context.Context, string) (*dto.GetListingResponse, error)
}

func (s getServiceStub) Get(ctx context.Context, id string) (*dto.GetListingResponse, error) {
	return s.get(ctx, id)
}

type headerStream struct {
	headers   metadata.MD
	headerErr error
}

func (s *headerStream) Method() string { return catalogpb.CatalogService_Get_FullMethodName }
func (s *headerStream) SetHeader(md metadata.MD) error {
	if s.headerErr != nil {
		return s.headerErr
	}
	s.headers = metadata.Join(s.headers, md)
	return nil
}

func TestGetHeaderErrors(t *testing.T) {
	for _, name := range []string{"missing transport stream", "transport rejects headers"} {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			stream := &headerStream{headerErr: errors.New("transport failure")}
			if name == "transport rejects headers" {
				ctx = grpc.NewContextWithServerTransportStream(ctx, stream)
			}
			calls := 0
			h := NewGRPCHandler(getServiceStub{get: func(gotCtx context.Context, id string) (*dto.GetListingResponse, error) {
				calls++
				if gotCtx != ctx || id != "listing-id" {
					t.Fatal("unexpected service arguments")
				}
				return &dto.GetListingResponse{
					Listing: dto.ListingResponse{ID: id, CreatedAt: "2026-10-13T09:00:00Z"},
					Cache:   "HIT",
				}, nil
			}})
			response, err := h.Get(ctx, &catalogpb.GetCatalogRequest{Id: "listing-id"})
			st := status.Convert(err)
			if response != nil || st.Code() != codes.Internal || st.Message() != "internal server error" {
				t.Fatalf("response = %v, error = %v, want Internal without transport details", response, err)
			}
			if calls != 1 || len(stream.headers) != 0 {
				t.Fatalf("service calls = %d, headers = %v", calls, stream.headers)
			}
		})
	}
}
func (s *headerStream) SendHeader(md metadata.MD) error { return s.SetHeader(md) }
func (s *headerStream) SetTrailer(metadata.MD) error    { return nil }

func TestGetCacheMetadata(t *testing.T) {
	for _, cacheStatus := range []string{"HIT", "MISS", "BYPASS"} {
		t.Run(cacheStatus, func(t *testing.T) {
			listing := dto.ListingResponse{
				ID: "listing-id", SellerID: "seller-id", Title: "listing",
				Price: 100, Stock: 1 << 40, Sold: 1 << 41, CreatedAt: "2026-10-13T12:00:00.123456789+03:00",
			}
			stream := &headerStream{}
			ctx := grpc.NewContextWithServerTransportStream(context.Background(), stream)
			h := NewGRPCHandler(getServiceStub{get: func(gotCtx context.Context, id string) (*dto.GetListingResponse, error) {
				if gotCtx != ctx || id != listing.ID {
					t.Fatalf("unexpected service arguments: %v, %q", gotCtx, id)
				}
				return &dto.GetListingResponse{Listing: listing, Cache: cacheStatus}, nil
			}})
			response, err := h.Get(ctx, &catalogpb.GetCatalogRequest{Id: listing.ID})
			if err != nil {
				t.Fatal(err)
			}
			got := response.GetListing()
			wantTime, err := time.Parse(time.RFC3339Nano, listing.CreatedAt)
			if err != nil {
				t.Fatal(err)
			}
			if got.Id != listing.ID || got.SellerId != listing.SellerID || got.Title != listing.Title ||
				got.Price != listing.Price || got.Stock != listing.Stock || got.Sold != listing.Sold ||
				!got.CreatedAt.AsTime().Equal(wantTime) {
				t.Fatalf("unexpected listing: %v", got)
			}
			if values := stream.headers.Get("x-cache"); len(values) != 1 || values[0] != cacheStatus {
				t.Fatalf("unexpected headers: %v", stream.headers)
			}
		})
	}
}

func TestGetErrors(t *testing.T) {
	tests := []struct {
		name     string
		response *dto.GetListingResponse
		err      error
		code     codes.Code
	}{
		{name: "missing", err: fmt.Errorf("service get: %w", repository.ErrNotFound), code: codes.NotFound},
		{name: "invalid date", response: &dto.GetListingResponse{Listing: dto.ListingResponse{CreatedAt: "invalid"}}, code: codes.Internal},
		{name: "out of range date", response: &dto.GetListingResponse{Listing: dto.ListingResponse{CreatedAt: "0000-01-01T00:00:00Z"}}, code: codes.Internal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stream := &headerStream{}
			ctx := grpc.NewContextWithServerTransportStream(context.Background(), stream)
			h := NewGRPCHandler(getServiceStub{get: func(context.Context, string) (*dto.GetListingResponse, error) {
				return tt.response, tt.err
			}})
			response, err := h.Get(ctx, &catalogpb.GetCatalogRequest{Id: "listing-id"})
			if response != nil || status.Code(err) != tt.code {
				t.Fatalf("response = %v, error = %v, want %v", response, err, tt.code)
			}
			if len(stream.headers) != 0 {
				t.Fatalf("headers set on error: %v", stream.headers)
			}
		})
	}
}
