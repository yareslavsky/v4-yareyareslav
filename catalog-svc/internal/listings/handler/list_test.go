package handler

import (
	"context"
	"fmt"
	"marketplace/catalog-svc/internal/listings/dto"
	"marketplace/catalog-svc/internal/listings/service"
	"marketplace/gen/catalogpb"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type listServiceStub struct {
	Service
	list func(context.Context, int, int) (*dto.ListResponse, error)
}

func (s listServiceStub) List(ctx context.Context, limit, offset int) (*dto.ListResponse, error) {
	return s.list(ctx, limit, offset)
}

func TestListPagination(t *testing.T) {
	limit, offset := int64(5), int64(10)
	zero := int64(0)
	tests := []struct {
		name          string
		request       *catalogpb.ListCatalogRequest
		limit, offset int
	}{
		{"defaults", &catalogpb.ListCatalogRequest{}, 20, 0},
		{"limit only", &catalogpb.ListCatalogRequest{Limit: &limit}, 5, 0},
		{"offset only", &catalogpb.ListCatalogRequest{Offset: &offset}, 20, 10},
		{"explicit", &catalogpb.ListCatalogRequest{Limit: &limit, Offset: &offset}, 5, 10},
		{"explicit zero", &catalogpb.ListCatalogRequest{Limit: &zero}, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			h := NewGRPCHandler(listServiceStub{list: func(gotCtx context.Context, limit, offset int) (*dto.ListResponse, error) {
				if gotCtx != ctx || limit != tt.limit || offset != tt.offset {
					t.Fatalf("unexpected arguments: %v, %d, %d", gotCtx, limit, offset)
				}
				return &dto.ListResponse{
					Listings: []dto.ListingResponse{{ID: "first", CreatedAt: "2026-10-13T09:00:00Z"}, {ID: "second", CreatedAt: "2026-10-13T10:00:00Z"}},
					Total:    42,
				}, nil
			}})
			response, err := h.List(ctx, tt.request)
			if err != nil {
				t.Fatal(err)
			}
			if response.Total != 42 || len(response.Items) != 2 || response.Items[0].Id != "first" || response.Items[1].Id != "second" {
				t.Fatalf("unexpected page: %v", response)
			}
		})
	}
}

func TestListEmptyAndErrors(t *testing.T) {
	tests := []struct {
		name     string
		response *dto.ListResponse
		err      error
		code     codes.Code
	}{
		{name: "empty", response: &dto.ListResponse{Total: 42}, code: codes.OK},
		{name: "validation", err: fmt.Errorf("service list: %w", &service.ValidationError{Fields: []service.FieldError{{Field: "limit", Message: "must be between 1 and 100"}}}), code: codes.InvalidArgument},
		{name: "invalid timestamp", response: &dto.ListResponse{Listings: []dto.ListingResponse{{CreatedAt: "invalid"}}}, code: codes.Internal},
		{name: "invalid second item", response: &dto.ListResponse{Listings: []dto.ListingResponse{{ID: "valid", CreatedAt: "2026-10-13T09:00:00Z"}, {ID: "invalid", CreatedAt: "invalid"}}, Total: 2}, code: codes.Internal},
		{name: "out of range timestamp", response: &dto.ListResponse{Listings: []dto.ListingResponse{{CreatedAt: "0000-01-01T00:00:00Z"}}}, code: codes.Internal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewGRPCHandler(listServiceStub{list: func(context.Context, int, int) (*dto.ListResponse, error) {
				return tt.response, tt.err
			}})
			response, err := h.List(context.Background(), &catalogpb.ListCatalogRequest{})
			if status.Code(err) != tt.code {
				t.Fatalf("error = %v, want %v", err, tt.code)
			}
			if tt.code == codes.OK {
				if response.Items == nil || len(response.Items) != 0 || response.Total != 42 {
					t.Fatalf("unexpected empty page: %v", response)
				}
			} else if response != nil {
				t.Fatalf("response on error: %v", response)
			}
		})
	}
}
