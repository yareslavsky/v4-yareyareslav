package service

import (
	"context"
	"errors"
	"marketplace/catalog-svc/internal/listings/dto"
	"marketplace/catalog-svc/internal/listings/model"
	"marketplace/catalog-svc/internal/listings/repository"
	"reflect"
	"strings"
	"testing"
)

func TestUpdateValidation(t *testing.T) {
	negative, zero := int64(-1), int64(0)
	empty, long, invalid := "", strings.Repeat("я", 201), string([]byte{0xff})
	tests := []struct {
		name     string
		request  dto.UpdateListingRequest
		fields   []string
		notFound bool
	}{
		{"invalid id", dto.UpdateListingRequest{ID: "invalid", SellerID: testSellerID}, nil, true},
		{"invalid seller", dto.UpdateListingRequest{ID: testListingID, SellerID: "invalid"}, []string{"seller_id"}, false},
		{"empty title", dto.UpdateListingRequest{ID: testListingID, SellerID: testSellerID, Title: &empty}, []string{"title"}, false},
		{"long title", dto.UpdateListingRequest{ID: testListingID, SellerID: testSellerID, Title: &long}, []string{"title"}, false},
		{"invalid UTF8", dto.UpdateListingRequest{ID: testListingID, SellerID: testSellerID, Title: &invalid}, []string{"title"}, false},
		{"zero price", dto.UpdateListingRequest{ID: testListingID, SellerID: testSellerID, Price: &zero}, []string{"price"}, false},
		{"negative stock", dto.UpdateListingRequest{ID: testListingID, SellerID: testSellerID, Stock: &negative}, []string{"stock"}, false},
		{"multiple invalid fields", dto.UpdateListingRequest{ID: testListingID, Title: &empty, Price: &negative, Stock: &negative}, []string{"seller_id", "title", "price", "stock"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response, err := NewCatalogService(nil, nil).Update(context.Background(), tt.request)
			if response != nil {
				t.Fatal("response on validation error")
			}
			if tt.notFound {
				if !errors.Is(err, repository.ErrNotFound) {
					t.Fatalf("error = %v, want not found", err)
				}
			} else {
				assertValidationFields(t, err, tt.fields)
			}
		})
	}
}

func TestReserveValidation(t *testing.T) {
	tests := []struct {
		name    string
		request dto.ReserveListingRequest
		fields  []string
	}{
		{"invalid id", dto.ReserveListingRequest{ListingID: "invalid", Qty: 1}, []string{"listing_id"}},
		{"zero quantity", dto.ReserveListingRequest{ListingID: testListingID}, []string{"qty"}},
		{"negative quantity", dto.ReserveListingRequest{ListingID: testListingID, Qty: -1}, []string{"qty"}},
		{"both invalid", dto.ReserveListingRequest{}, []string{"listing_id", "qty"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response, err := NewCatalogService(nil, nil).Reserve(context.Background(), tt.request)
			if response != nil {
				t.Fatal("response on validation error")
			}
			assertValidationFields(t, err, tt.fields)
		})
	}
}

func TestDeleteValidation(t *testing.T) {
	for _, tt := range []struct {
		name, id, seller string
		notFound         bool
	}{
		{"invalid listing", "invalid", testSellerID, true},
		{"invalid seller", testListingID, "invalid", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := NewCatalogService(nil, nil).Delete(context.Background(), tt.id, tt.seller)
			if tt.notFound {
				if !errors.Is(err, repository.ErrNotFound) {
					t.Fatalf("error = %v, want not found", err)
				}
			} else {
				assertValidationFields(t, err, []string{"seller_id"})
			}
		})
	}
}

func TestApplyOrderCreatedValidation(t *testing.T) {
	tests := []struct {
		name   string
		edit   func(*dto.OrderCreatedEvent)
		fields []string
	}{
		{"event id", func(e *dto.OrderCreatedEvent) { e.EventID = "invalid" }, []string{"event_id"}},
		{"order id", func(e *dto.OrderCreatedEvent) { e.OrderID = "invalid" }, []string{"order_id"}},
		{"buyer id", func(e *dto.OrderCreatedEvent) { e.BuyerID = "invalid" }, []string{"buyer_id"}},
		{"listing id", func(e *dto.OrderCreatedEvent) { e.ListingID = "invalid" }, []string{"listing_id"}},
		{"zero quantity", func(e *dto.OrderCreatedEvent) { e.Qty = 0 }, []string{"qty"}},
		{"negative quantity", func(e *dto.OrderCreatedEvent) { e.Qty = -1 }, []string{"qty"}},
		{"invalid timestamp", func(e *dto.OrderCreatedEvent) { e.CreatedAt = "invalid" }, []string{"created_at"}},
		{"all invalid", func(e *dto.OrderCreatedEvent) { *e = dto.OrderCreatedEvent{} }, []string{"event_id", "order_id", "buyer_id", "listing_id", "qty", "created_at"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := testOrderCreated()
			tt.edit(&event)
			applied, err := NewCatalogService(nil, nil).ApplyOrderCreated(context.Background(), event)
			if applied {
				t.Fatal("invalid event applied")
			}
			assertValidationFields(t, err, tt.fields)
		})
	}
}

func TestMutationsInvalidateAfterRepositorySuccess(t *testing.T) {
	for _, method := range []string{"Update", "Delete", "Reserve", "ApplyOrderCreated"} {
		t.Run(method, func(t *testing.T) {
			for _, scenario := range []string{"success", "cache miss", "cache failure", "repository failure", "duplicate"} {
				if scenario == "duplicate" && method != "ApplyOrderCreated" {
					continue
				}
				t.Run(scenario, func(t *testing.T) {
					ctx := context.Background()
					listing := testListing()
					var repoErr, cacheErr error
					if scenario == "repository failure" {
						switch method {
						case "Update", "Delete":
							repoErr = repository.ErrForbidden
						case "Reserve":
							repoErr = repository.ErrInsufficientStock
						case "ApplyOrderCreated":
							repoErr = repository.ErrNotFound
						}
					}
					if scenario == "cache failure" {
						cacheErr = errors.New("Redis unavailable")
					}
					var calls []string
					checkRepository := func(gotCtx context.Context) {
						if gotCtx != ctx {
							t.Fatal("repository context changed")
						}
						calls = append(calls, "repository")
					}
					var invalidationCtx context.Context
					c := &cacheStub{delete: func(cacheCtx context.Context, keys ...string) (bool, error) {
						assertCacheContext(t, cacheCtx)
						if !reflect.DeepEqual(calls, []string{"repository"}) {
							t.Fatal("cache invalidated before repository success")
						}
						if !reflect.DeepEqual(keys, []string{"listing:" + testListingID}) {
							t.Fatalf("invalidated keys = %v", keys)
						}
						calls = append(calls, "cache delete")
						invalidationCtx = cacheCtx
						return scenario != "cache miss", cacheErr
					}}
					repo := &repositoryStub{}
					svc := NewCatalogService(repo, c)
					var err error
					switch method {
					case "Update":
						title := "Новое название"
						request := dto.UpdateListingRequest{ID: testListingID, SellerID: testSellerID, Title: &title}
						repo.update = func(gotCtx context.Context, patch model.UpdateListing) (*model.Listing, error) {
							checkRepository(gotCtx)
							want := model.UpdateListing{ID: testListingID, SellerID: testSellerID, Title: &title}
							if !reflect.DeepEqual(patch, want) {
								t.Fatalf("unexpected partial patch: %+v", patch)
							}
							return &listing, repoErr
						}
						var response *dto.ListingResponse
						response, err = svc.Update(ctx, request)
						if repoErr != nil {
							if response != nil {
								t.Fatal("response on repository error")
							}
						} else if response == nil || *response != testListingResponse() {
							t.Fatalf("response = %+v", response)
						}
					case "Delete":
						repo.delete = func(gotCtx context.Context, id, sellerID string) error {
							checkRepository(gotCtx)
							if id != testListingID || sellerID != testSellerID {
								t.Fatal("unexpected delete arguments")
							}
							return repoErr
						}
						err = svc.Delete(ctx, testListingID, testSellerID)
					case "Reserve":
						repo.reserve = func(gotCtx context.Context, id string, qty int64) (int64, error) {
							checkRepository(gotCtx)
							if id != testListingID || qty != 1<<40 {
								t.Fatal("unexpected reserve arguments")
							}
							return 1 << 41, repoErr
						}
						var response *dto.ReserveListingResponse
						response, err = svc.Reserve(ctx, dto.ReserveListingRequest{ListingID: testListingID, Qty: 1 << 40})
						if repoErr != nil {
							if response != nil {
								t.Fatal("response on repository error")
							}
						} else if response == nil || response.Remaining != 1<<41 {
							t.Fatalf("response = %+v", response)
						}
					case "ApplyOrderCreated":
						event := testOrderCreated()
						repo.apply = func(gotCtx context.Context, eventID, id string, qty int64) (bool, error) {
							checkRepository(gotCtx)
							if eventID != event.EventID || id != event.ListingID || qty != event.Qty {
								t.Fatal("unexpected event arguments")
							}
							return scenario != "duplicate", repoErr
						}
						var applied bool
						applied, err = svc.ApplyOrderCreated(ctx, event)
						if applied != (repoErr == nil && scenario != "duplicate") {
							t.Fatalf("applied = %v", applied)
						}
					}
					if !errors.Is(err, repoErr) {
						t.Fatalf("error = %v, want %v", err, repoErr)
					}
					wantCalls := []string{"repository"}
					if repoErr == nil {
						wantCalls = append(wantCalls, "cache delete")
					}
					if !reflect.DeepEqual(calls, wantCalls) {
						t.Fatalf("calls = %v, want %v", calls, wantCalls)
					}
					if invalidationCtx != nil && invalidationCtx.Err() == nil {
						t.Fatal("invalidation context not canceled")
					}
				})
			}
		})
	}
}

func TestUpdateExplicitZeroStock(t *testing.T) {
	zero, price := int64(0), int64(250)
	listing := testListing()
	listing.Stock, listing.Price = zero, price
	repo := &repositoryStub{update: func(ctx context.Context, patch model.UpdateListing) (*model.Listing, error) {
		if patch.Stock == nil || *patch.Stock != 0 || patch.Price == nil || *patch.Price != price || patch.Title != nil {
			t.Fatalf("optional fields lost: %+v", patch)
		}
		return &listing, nil
	}}
	c := &cacheStub{delete: func(context.Context, ...string) (bool, error) { return true, nil }}
	response, err := NewCatalogService(repo, c).Update(context.Background(), dto.UpdateListingRequest{ID: testListingID, SellerID: testSellerID, Stock: &zero, Price: &price})
	if err != nil || response.Stock != 0 || response.Price != price {
		t.Fatalf("response = %v, error = %v", response, err)
	}
}
