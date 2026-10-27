package handler

import (
	"context"
	"errors"
	"marketplace/catalog-svc/internal/listings/dto"
	"marketplace/catalog-svc/internal/listings/repository"
	"marketplace/catalog-svc/internal/listings/service"
	"marketplace/gen/catalogpb"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Service interface {
	Create(context.Context, dto.CreateListingRequest) (*dto.ListingResponse, error)
	Get(context.Context, string) (*dto.GetListingResponse, error)
	List(context.Context, int, int) (*dto.ListResponse, error)
	Update(context.Context, dto.UpdateListingRequest) (*dto.ListingResponse, error)
	Delete(context.Context, string, string) error
	Reserve(context.Context, dto.ReserveListingRequest) (*dto.ReserveListingResponse, error)
	ReleaseReservation(context.Context, string) error
}

type GRPCHandler struct {
	catalogpb.UnimplementedCatalogServiceServer
	service Service
}

func NewGRPCHandler(service Service) *GRPCHandler {
	return &GRPCHandler{
		service: service,
	}
}

func (h *GRPCHandler) Create(
	ctx context.Context,
	req *catalogpb.CreateCatalogRequest,
) (*catalogpb.CreateCatalogResponse, error) {
	listing, err := h.service.Create(ctx, dto.CreateListingRequest{
		SellerID: req.GetSellerId(),
		Title:    req.GetTitle(),
		Price:    req.GetPrice(),
		Stock:    req.Stock,
	})
	if err != nil {
		return nil, toGRPCError(err)
	}

	result, err := toProtoListing(*listing)
	if err != nil {
		return nil, toGRPCError(err)
	}

	return &catalogpb.CreateCatalogResponse{Listing: result}, nil
}

func (h *GRPCHandler) Get(
	ctx context.Context,
	req *catalogpb.GetCatalogRequest,
) (*catalogpb.GetCatalogResponse, error) {
	listing, err := h.service.Get(ctx, req.GetId())
	if err != nil {
		return nil, toGRPCError(err)
	}

	result, err := toProtoListing(listing.Listing)
	if err != nil {
		return nil, toGRPCError(err)
	}
	if err := grpc.SetHeader(ctx, metadata.Pairs("x-cache", listing.Cache)); err != nil {
		return nil, toGRPCError(err)
	}

	return &catalogpb.GetCatalogResponse{Listing: result}, nil
}

func (h *GRPCHandler) List(
	ctx context.Context,
	req *catalogpb.ListCatalogRequest,
) (*catalogpb.ListCatalogResponse, error) {
	limit, offset := 20, 0
	if req.Limit != nil {
		limit = int(*req.Limit)
	}
	if req.Offset != nil {
		offset = int(*req.Offset)
	}

	result, err := h.service.List(ctx, limit, offset)
	if err != nil {
		return nil, toGRPCError(err)
	}

	items := make([]*catalogpb.Listing, 0, len(result.Listings))
	for _, listing := range result.Listings {
		item, err := toProtoListing(listing)
		if err != nil {
			return nil, toGRPCError(err)
		}
		items = append(items, item)
	}

	return &catalogpb.ListCatalogResponse{
		Items: items,
		Total: result.Total,
	}, nil
}

func (h *GRPCHandler) Update(
	ctx context.Context,
	req *catalogpb.UpdateCatalogRequest,
) (*catalogpb.UpdateCatalogResponse, error) {
	listing, err := h.service.Update(ctx, dto.UpdateListingRequest{
		ID:       req.GetId(),
		SellerID: req.GetSellerId(),
		Title:    req.Title,
		Price:    req.Price,
		Stock:    req.Stock,
	})
	if err != nil {
		return nil, toGRPCError(err)
	}

	result, err := toProtoListing(*listing)
	if err != nil {
		return nil, toGRPCError(err)
	}

	return &catalogpb.UpdateCatalogResponse{Listing: result}, nil
}

func (h *GRPCHandler) Delete(
	ctx context.Context,
	req *catalogpb.DeleteCatalogRequest,
) (*emptypb.Empty, error) {
	if err := h.service.Delete(ctx, req.GetId(), req.GetSellerId()); err != nil {
		return nil, toGRPCError(err)
	}

	return &emptypb.Empty{}, nil
}

func (h *GRPCHandler) Reserve(
	ctx context.Context,
	req *catalogpb.ReserveCatalogRequest,
) (*catalogpb.ReserveCatalogResponse, error) {
	result, err := h.service.Reserve(ctx, dto.ReserveListingRequest{
		ReservationID: req.GetReservationId(),
		ListingID:     req.GetListingId(),
		Qty:           req.GetQty(),
	})
	if err != nil {
		return nil, toGRPCError(err)
	}

	return &catalogpb.ReserveCatalogResponse{
		Remaining: result.Remaining,
	}, nil
}

func toProtoListing(listing dto.ListingResponse) (*catalogpb.Listing, error) {
	createdAt, err := time.Parse(time.RFC3339Nano, listing.CreatedAt)
	if err != nil {
		return nil, err
	}
	createdAtTimestamp := timestamppb.New(createdAt)
	if err := createdAtTimestamp.CheckValid(); err != nil {
		return nil, err
	}

	return &catalogpb.Listing{
		Id:        listing.ID,
		SellerId:  listing.SellerID,
		Title:     listing.Title,
		Price:     listing.Price,
		Stock:     listing.Stock,
		Sold:      listing.Sold,
		CreatedAt: createdAtTimestamp,
	}, nil
}

func toGRPCError(err error) error {
	if err == nil {
		return nil
	}

	switch {
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "request canceled")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "request deadline exceeded")
	case errors.Is(err, service.ErrValidation):
		st := status.New(codes.InvalidArgument, "listing validation failed")
		var validationErr *service.ValidationError
		if errors.As(err, &validationErr) {
			details := &errdetails.BadRequest{}
			for _, field := range validationErr.Fields {
				details.FieldViolations = append(details.FieldViolations, &errdetails.BadRequest_FieldViolation{
					Field:       field.Field,
					Description: field.Message,
				})
			}
			if withDetails, detailsErr := st.WithDetails(details); detailsErr == nil {
				st = withDetails
			}
		}
		return st.Err()
	case errors.Is(err, repository.ErrInvalidQuantity):
		return status.Error(codes.InvalidArgument, "quantity must be positive")
	case errors.Is(err, repository.ErrNotFound):
		return status.Error(codes.NotFound, "listing not found")
	case errors.Is(err, repository.ErrForbidden):
		return status.Error(codes.PermissionDenied, "listing belongs to another seller")
	case errors.Is(err, repository.ErrInsufficientStock):
		return status.Error(codes.FailedPrecondition, "insufficient listing stock")
	case errors.Is(err, repository.ErrReservationConflict):
		return status.Error(codes.FailedPrecondition, err.Error())
	default:
		return status.Error(codes.Internal, "internal server error")
	}
}
