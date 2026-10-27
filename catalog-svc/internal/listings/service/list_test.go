package service

import (
	"context"
	"errors"
	"marketplace/catalog-svc/internal/listings/dto"
	"marketplace/catalog-svc/internal/listings/model"
	"reflect"
	"testing"
)

func TestListValidation(t *testing.T) {
	tests := []struct {
		name          string
		limit, offset int
		fields        []string
	}{
		{"zero limit", 0, 0, []string{"limit"}},
		{"negative limit", -1, 0, []string{"limit"}},
		{"large limit", 101, 0, []string{"limit"}},
		{"negative offset", 20, -1, []string{"offset"}},
		{"both invalid", 0, -1, []string{"limit", "offset"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response, err := NewCatalogService(nil, nil).List(context.Background(), tt.limit, tt.offset)
			if response != nil {
				t.Fatal("response on validation error")
			}
			assertValidationFields(t, err, tt.fields)
		})
	}
}

func TestListPageAndRepositoryErrors(t *testing.T) {
	first, second := testListing(), testListing()
	second.ID = testEventID
	second.Title = "Другой листинг"
	second.CreatedAt = "2026-10-13T10:00:00Z"
	secondResponse := testListingResponse()
	secondResponse.ID, secondResponse.Title, secondResponse.CreatedAt = second.ID, second.Title, second.CreatedAt
	tests := []struct {
		name          string
		limit, offset int
		items         []model.Listing
		want          []dto.ListingResponse
		total         int64
		err           error
	}{
		{"page", 20, 5, []model.Listing{first, second}, []dto.ListingResponse{testListingResponse(), secondResponse}, 42, nil},
		{"minimum limit", 1, 0, nil, []dto.ListingResponse{}, 0, nil},
		{"maximum limit", 100, 0, []model.Listing{}, []dto.ListingResponse{}, 0, nil},
		{"offset beyond page", 20, 100, nil, []dto.ListingResponse{}, 42, nil},
		{"database error", 20, 0, nil, nil, 0, errors.New("database failed")},
		{"canceled", 20, 0, nil, nil, 0, context.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			calls := 0
			repo := &repositoryStub{list: func(gotCtx context.Context, limit, offset int) ([]model.Listing, int64, error) {
				calls++
				if gotCtx != ctx || limit != tt.limit || offset != tt.offset {
					t.Fatal("unexpected pagination arguments")
				}
				return tt.items, tt.total, tt.err
			}}
			response, err := NewCatalogService(repo, nil).List(ctx, tt.limit, tt.offset)
			if !errors.Is(err, tt.err) {
				t.Fatalf("error = %v, want %v", err, tt.err)
			}
			if tt.err != nil {
				if response != nil {
					t.Fatal("response on repository error")
				}
			} else if want := (&dto.ListResponse{Listings: tt.want, Total: tt.total}); !reflect.DeepEqual(response, want) {
				t.Fatalf("response = %+v, want %+v", response, want)
			}
			if calls != 1 {
				t.Fatalf("list calls = %d, want 1", calls)
			}
		})
	}
}
