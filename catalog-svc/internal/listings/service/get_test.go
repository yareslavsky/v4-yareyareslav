package service

import (
	"context"
	"errors"
	"fmt"
	"marketplace/catalog-svc/internal/cache"
	"marketplace/catalog-svc/internal/listings/dto"
	"marketplace/catalog-svc/internal/listings/model"
	"marketplace/catalog-svc/internal/listings/repository"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestGetInvalidID(t *testing.T) {
	for _, id := range []string{"", "invalid", strings.ReplaceAll(testListingID, "-", ""), "{" + testListingID + "}", "urn:uuid:" + testListingID, "z65e8411-e29b-41d4-a716-446655440000"} {
		t.Run(id, func(t *testing.T) {
			response, err := NewCatalogService(nil, nil).Get(context.Background(), id)
			if response != nil || !errors.Is(err, repository.ErrNotFound) {
				t.Fatalf("response = %v, error = %v", response, err)
			}
		})
	}
}

func TestGetCacheScenarios(t *testing.T) {
	type requestContextKey struct{}
	failure := errors.New("Redis unavailable")
	tests := []struct {
		name                    string
		getErr, setErr, repoErr error
		status                  string
		calls                   []string
	}{
		{"hit", nil, nil, nil, "HIT", []string{"cache get"}},
		{"miss", fmt.Errorf("Redis: %w", cache.ErrCacheMiss), nil, nil, "MISS", []string{"cache get", "repository", "cache set"}},
		{"read error", failure, nil, nil, "BYPASS", []string{"cache get", "repository"}},
		{"write error", cache.ErrCacheMiss, failure, nil, "BYPASS", []string{"cache get", "repository", "cache set"}},
		{"not found", cache.ErrCacheMiss, nil, repository.ErrNotFound, "", []string{"cache get", "repository"}},
		{"database error", failure, nil, context.DeadlineExceeded, "", []string{"cache get", "repository"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), requestContextKey{}, "request")
			listing := testListing()
			var calls []string
			var cacheContexts []context.Context
			repo := &repositoryStub{get: func(gotCtx context.Context, id string) (*model.Listing, error) {
				calls = append(calls, "repository")
				if gotCtx != ctx || id != testListingID {
					t.Fatal("unexpected repository arguments")
				}
				return &listing, tt.repoErr
			}}
			c := &cacheStub{
				get: func(cacheCtx context.Context, key string) (*model.Listing, error) {
					calls = append(calls, "cache get")
					assertCacheContext(t, cacheCtx)
					if key != "listing:"+testListingID || cacheCtx.Value(requestContextKey{}) != "request" {
						t.Fatal("unexpected cache get arguments")
					}
					cacheContexts = append(cacheContexts, cacheCtx)
					return &listing, tt.getErr
				},
				set: func(cacheCtx context.Context, key string, got model.Listing, ttl time.Duration) error {
					calls = append(calls, "cache set")
					assertCacheContext(t, cacheCtx)
					if key != "listing:"+testListingID || got != listing || ttl != 30*time.Second {
						t.Fatal("unexpected cache set arguments")
					}
					cacheContexts = append(cacheContexts, cacheCtx)
					return tt.setErr
				},
			}
			response, err := NewCatalogService(repo, c).Get(ctx, testListingID)
			if !errors.Is(err, tt.repoErr) {
				t.Fatalf("error = %v, want %v", err, tt.repoErr)
			}
			if tt.repoErr != nil {
				if response != nil {
					t.Fatal("response on repository error")
				}
			} else if want := (&dto.GetListingResponse{Listing: testListingResponse(), Cache: tt.status}); !reflect.DeepEqual(response, want) {
				t.Fatalf("response = %+v, want %+v", response, want)
			}
			if !reflect.DeepEqual(calls, tt.calls) {
				t.Fatalf("calls = %v, want %v", calls, tt.calls)
			}
			for _, cacheCtx := range cacheContexts {
				if cacheCtx.Err() == nil {
					t.Fatal("cache context not canceled after return")
				}
			}
			if ctx.Err() != nil {
				t.Fatal("request context canceled by cache")
			}
		})
	}
}

func TestGetTimeoutDoesNotCancelDatabaseContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		listing := testListing()
		repo := &repositoryStub{get: func(gotCtx context.Context, id string) (*model.Listing, error) {
			if gotCtx != ctx || gotCtx.Err() != nil {
				t.Fatal("database received expired cache context")
			}
			return &listing, nil
		}}
		c := &cacheStub{get: func(cacheCtx context.Context, key string) (*model.Listing, error) {
			<-cacheCtx.Done()
			return nil, cacheCtx.Err()
		}}
		started := time.Now()
		response, err := NewCatalogService(repo, c).Get(ctx, testListingID)
		if err != nil || response.Cache != "BYPASS" {
			t.Fatalf("response = %v, error = %v", response, err)
		}
		if elapsed := time.Since(started); elapsed != 200*time.Millisecond {
			t.Fatalf("get took %v, want 200ms", elapsed)
		}
	})
}
