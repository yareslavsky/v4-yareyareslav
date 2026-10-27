package listings

import (
	"context"
	"encoding/json"
	"fmt"
	"marketplace/gen/catalogpb"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

type ListingsService struct {
	client catalogpb.CatalogServiceClient
}

func NewListingsService(client catalogpb.CatalogServiceClient) *ListingsService {
	return &ListingsService{client: client}
}

func toListing(item *catalogpb.Listing) Listing {
	return Listing{
		ID: item.GetId(), Title: item.GetTitle(),
		Price: json.Number(fmt.Sprintf("%d.%02d", item.GetPrice()/100, item.GetPrice()%100)),
		Stock: item.GetStock(), Sold: item.GetSold(), SellerID: item.GetSellerId(),
		CreatedAt: item.GetCreatedAt().AsTime().UTC().Format(time.RFC3339Nano),
	}
}

func (s *ListingsService) FindAll(ctx context.Context, limit, offset int64) ([]Listing, int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	result, err := s.client.List(ctx, &catalogpb.ListCatalogRequest{Limit: &limit, Offset: &offset})
	if err != nil {
		return nil, 0, err
	}
	items := make([]Listing, 0, len(result.GetItems()))
	for _, item := range result.GetItems() {
		items = append(items, toListing(item))
	}
	return items, result.GetTotal(), nil
}

func (s *ListingsService) FindOne(ctx context.Context, id string) (Listing, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var headers metadata.MD
	result, err := s.client.Get(ctx, &catalogpb.GetCatalogRequest{Id: id}, grpc.Header(&headers))
	if err != nil {
		return Listing{}, "", err
	}
	cache := ""
	if values := headers.Get("x-cache"); len(values) > 0 {
		cache = values[0]
	}
	return toListing(result.GetListing()), cache, nil
}

func (s *ListingsService) CreateOne(ctx context.Context, request *catalogpb.CreateCatalogRequest) (Listing, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	result, err := s.client.Create(ctx, request)
	if err != nil {
		return Listing{}, err
	}
	return toListing(result.GetListing()), nil
}

func (s *ListingsService) PatchOne(ctx context.Context, request *catalogpb.UpdateCatalogRequest) (Listing, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	result, err := s.client.Update(ctx, request)
	if err != nil {
		return Listing{}, err
	}
	return toListing(result.GetListing()), nil
}

func (s *ListingsService) DeleteOne(ctx context.Context, id, sellerID string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err := s.client.Delete(ctx, &catalogpb.DeleteCatalogRequest{Id: id, SellerId: sellerID})
	return err
}
