package service

import (
	"context"
	"errors"
	"marketplace/catalog-svc/internal/listings/dto"
	"marketplace/catalog-svc/internal/listings/model"
	"reflect"
	"strings"
	"testing"
)

func TestCreateValidation(t *testing.T) {
	stock, negative := int64(0), int64(-1)
	valid := dto.CreateListingRequest{SellerID: testSellerID, Title: "Молоко", Price: 100, Stock: &stock}
	tests := []struct {
		name   string
		edit   func(*dto.CreateListingRequest)
		fields []string
	}{
		{"seller", func(r *dto.CreateListingRequest) { r.SellerID = "invalid" }, []string{"seller_id"}},
		{"empty title", func(r *dto.CreateListingRequest) { r.Title = "" }, []string{"title"}},
		{"blank title", func(r *dto.CreateListingRequest) { r.Title = " \t" }, []string{"title"}},
		{"invalid UTF8", func(r *dto.CreateListingRequest) { r.Title = string([]byte{0xff}) }, []string{"title"}},
		{"long title", func(r *dto.CreateListingRequest) { r.Title = strings.Repeat("я", 201) }, []string{"title"}},
		{"zero price", func(r *dto.CreateListingRequest) { r.Price = 0 }, []string{"price"}},
		{"negative price", func(r *dto.CreateListingRequest) { r.Price = -1 }, []string{"price"}},
		{"missing stock", func(r *dto.CreateListingRequest) { r.Stock = nil }, []string{"stock"}},
		{"negative stock", func(r *dto.CreateListingRequest) { r.Stock = &negative }, []string{"stock"}},
		{"all invalid", func(r *dto.CreateListingRequest) { *r = dto.CreateListingRequest{} }, []string{"seller_id", "title", "price", "stock"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := valid
			tt.edit(&request)
			response, err := NewCatalogService(nil, nil).Create(context.Background(), request)
			if response != nil {
				t.Fatalf("response on validation error: %+v", response)
			}
			assertValidationFields(t, err, tt.fields)
		})
	}
}

func TestCreateSuccessAndRepositoryErrors(t *testing.T) {
	for _, failure := range []error{nil, errors.New("database failed"), context.Canceled, context.DeadlineExceeded} {
		name := "success"
		if failure != nil {
			name = failure.Error()
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			listing := testListing()
			calls := 0
			repo := &repositoryStub{create: func(gotCtx context.Context, got model.Listing) (*model.Listing, error) {
				calls++
				want := model.Listing{Title: listing.Title, Price: listing.Price, Stock: listing.Stock, SellerID: listing.SellerID}
				if gotCtx != ctx || got != want {
					t.Fatalf("unexpected create arguments: %+v", got)
				}
				return &listing, failure
			}}
			response, err := NewCatalogService(repo, nil).Create(ctx, dto.CreateListingRequest{
				SellerID: listing.SellerID, Title: listing.Title, Price: listing.Price, Stock: &listing.Stock,
			})
			if !errors.Is(err, failure) {
				t.Fatalf("error = %v, want %v", err, failure)
			}
			if failure != nil {
				if response != nil {
					t.Fatal("response on repository error")
				}
			} else if want := testListingResponse(); response == nil || !reflect.DeepEqual(*response, want) {
				t.Fatalf("response = %+v, want %+v", response, want)
			}
			if calls != 1 {
				t.Fatalf("create calls = %d, want 1", calls)
			}
		})
	}
}
