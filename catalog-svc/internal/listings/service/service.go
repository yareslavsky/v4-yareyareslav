package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"marketplace/catalog-svc/internal/cache"
	"marketplace/catalog-svc/internal/listings/dto"
	"marketplace/catalog-svc/internal/listings/mapper"
	"marketplace/catalog-svc/internal/listings/model"
	"marketplace/catalog-svc/internal/listings/repository"
	"time"
)

var listingCache = "listing:"

type Repository interface {
	Create(context.Context, model.Listing) (*model.Listing, error)
	Get(context.Context, string) (*model.Listing, error)
	List(context.Context, int, int) ([]model.Listing, int64, error)
	Update(context.Context, model.UpdateListing) (*model.Listing, error)
	Delete(context.Context, string, string) error
	Reserve(context.Context, string, int64) (int64, error)
	ReserveReservation(context.Context, string, string, int64) (int64, error)
	ReleaseReservation(context.Context, string) (string, error)
	ApplyOrderCreated(context.Context, string, string, int64) (bool, error)
}

type CatalogCache interface {
	Get(ctx context.Context, key string) (*model.Listing, error)
	Set(ctx context.Context, key string, value model.Listing, ttl time.Duration) error
	Delete(ctx context.Context, keys ...string) (bool, error)
}

type CatalogService struct {
	repository Repository
	cache      CatalogCache
}

func NewCatalogService(repository Repository, cache CatalogCache) *CatalogService {
	return &CatalogService{
		repository: repository,
		cache:      cache,
	}
}

func (s *CatalogService) Create(
	ctx context.Context,
	createDto dto.CreateListingRequest,
) (*dto.ListingResponse, error) {
	if err := validateCreate(createDto); err != nil {
		return nil, fmt.Errorf("service create: %w", err)
	}

	listing := model.Listing{
		Title:    createDto.Title,
		Price:    createDto.Price,
		SellerID: createDto.SellerID,
		Stock:    *createDto.Stock,
	}
	createdListing, err := s.repository.Create(ctx, listing)
	if err != nil {
		return nil, fmt.Errorf("service create: %w", err)
	}

	response := mapper.ToListingResponse(*createdListing)
	return &response, nil
}

func (s *CatalogService) Get(
	ctx context.Context,
	id string,
) (*dto.GetListingResponse, error) {
	if !isValidUUID(id) {
		return nil, fmt.Errorf("service get: %w", repository.ErrNotFound)
	}

	getCacheCtx, cancel := context.WithTimeout(ctx, time.Millisecond*200)
	defer cancel()
	val, cacheErr := s.cache.Get(getCacheCtx, listingCache+id)

	if cacheErr == nil {
		return &dto.GetListingResponse{
			Listing: mapper.ToListingResponse(*val),
			Cache:   "HIT",
		}, nil
	}

	cacheStatus := "MISS"
	if !errors.Is(cacheErr, cache.ErrCacheMiss) {
		cacheStatus = "BYPASS"
		slog.WarnContext(ctx, "cache get failed", "error", cacheErr)
	}

	listing, err := s.repository.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("service get: %w", err)
	}

	if cacheStatus == "MISS" {
		setCacheCtx, cancelSet := context.WithTimeout(ctx, time.Millisecond*200)
		err = s.cache.Set(setCacheCtx, listingCache+id, *listing, time.Second*30)
		cancelSet()

		if err != nil {
			cacheStatus = "BYPASS"
			slog.WarnContext(ctx, "cache set failed", "error", err)
		}
	}

	return &dto.GetListingResponse{
		Listing: mapper.ToListingResponse(*listing),
		Cache:   cacheStatus,
	}, nil
}

func (s *CatalogService) List(
	ctx context.Context,
	limit,
	offset int,
) (*dto.ListResponse, error) {
	if err := validateList(limit, offset); err != nil {
		return nil, fmt.Errorf("service list: %w", err)
	}

	listings, total, err := s.repository.List(ctx, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("service list: %w", err)
	}

	responseListings := make([]dto.ListingResponse, 0, len(listings))
	for _, listing := range listings {
		responseListings = append(
			responseListings,
			mapper.ToListingResponse(listing),
		)
	}
	return &dto.ListResponse{
		Listings: responseListings,
		Total:    total,
	}, nil
}

func (s *CatalogService) Update(
	ctx context.Context,
	request dto.UpdateListingRequest,
) (*dto.ListingResponse, error) {
	if !isValidUUID(request.ID) {
		return nil, fmt.Errorf("service update: %w", repository.ErrNotFound)
	}
	if err := validateUpdate(request); err != nil {
		return nil, fmt.Errorf("service update: %w", err)
	}

	listing := model.UpdateListing{
		ID:       request.ID,
		SellerID: request.SellerID,
		Title:    request.Title,
		Price:    request.Price,
		Stock:    request.Stock,
	}
	updated, err := s.repository.Update(ctx, listing)
	if err != nil {
		return nil, fmt.Errorf("service update: %w", err)
	}

	s.invalidateCache(ctx, request.ID)

	response := mapper.ToListingResponse(*updated)
	return &response, nil
}

func (s *CatalogService) Reserve(
	ctx context.Context,
	request dto.ReserveListingRequest,
) (*dto.ReserveListingResponse, error) {
	if err := validateReserve(request); err != nil {
		return nil, fmt.Errorf("service reserve: %w", err)
	}

	var remaining int64
	var err error
	if request.ReservationID != "" {
		remaining, err = s.repository.ReserveReservation(ctx, request.ReservationID, request.ListingID, request.Qty)
	} else {
		remaining, err = s.repository.Reserve(ctx, request.ListingID, request.Qty)
	}
	if err != nil {
		return nil, fmt.Errorf("service reserve: %w", err)
	}

	s.invalidateCache(ctx, request.ListingID)

	return &dto.ReserveListingResponse{Remaining: remaining}, nil
}

func (s *CatalogService) ApplyOrderCreated(
	ctx context.Context,
	event dto.OrderCreatedEvent,
) (bool, error) {
	if err := validateOrderCreated(event); err != nil {
		return false, fmt.Errorf("service apply order created: %w", err)
	}

	applied, err := s.repository.ApplyOrderCreated(ctx, event.EventID, event.ListingID, event.Qty)
	if err != nil {
		return false, fmt.Errorf("service apply order created: %w", err)
	}

	s.invalidateCache(ctx, event.ListingID)

	return applied, nil
}

func (s *CatalogService) Delete(
	ctx context.Context,
	id,
	sellerID string,
) error {
	if !isValidUUID(id) {
		return fmt.Errorf("service delete: %w", repository.ErrNotFound)
	}
	if !isValidUUID(sellerID) {
		return fmt.Errorf("service delete: %w", &ValidationError{
			Fields: []FieldError{{Field: "seller_id", Message: "must be a valid UUID"}},
		})
	}

	if err := s.repository.Delete(ctx, id, sellerID); err != nil {
		return fmt.Errorf("service delete: %w", err)
	}

	s.invalidateCache(ctx, id)

	return nil
}

func (s *CatalogService) invalidateCache(ctx context.Context, key string) {
	deleteCtx, cancel := context.WithTimeout(ctx, time.Millisecond*200)
	defer cancel()

	_, err := s.cache.Delete(deleteCtx, listingCache+key)
	if err != nil {
		slog.WarnContext(ctx, "cache invalidation on update", "error", err)
	}
}
