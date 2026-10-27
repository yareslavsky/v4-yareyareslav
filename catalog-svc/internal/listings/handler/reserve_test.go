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

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type reserveServiceStub struct {
	Service
	reserve func(context.Context, dto.ReserveListingRequest) (*dto.ReserveListingResponse, error)
}

func (s reserveServiceStub) Reserve(ctx context.Context, request dto.ReserveListingRequest) (*dto.ReserveListingResponse, error) {
	return s.reserve(ctx, request)
}

func TestReserveInt64RoundTrip(t *testing.T) {
	ctx := t.Context()
	qty, remaining := int64(1<<40), int64(1<<41)
	request := &catalogpb.ReserveCatalogRequest{ListingId: "listing", Qty: qty}
	data, err := proto.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	decoded := &catalogpb.ReserveCatalogRequest{}
	if err := proto.Unmarshal(data, decoded); err != nil {
		t.Fatal(err)
	}
	h := NewGRPCHandler(reserveServiceStub{reserve: func(gotCtx context.Context, request dto.ReserveListingRequest) (*dto.ReserveListingResponse, error) {
		if gotCtx != ctx || request.ListingID != "listing" || request.Qty != qty {
			t.Fatalf("unexpected request: %+v", request)
		}
		return &dto.ReserveListingResponse{Remaining: remaining}, nil
	}})
	response, err := h.Reserve(ctx, decoded)
	if err != nil {
		t.Fatal(err)
	}
	data, err = proto.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	decodedResponse := &catalogpb.ReserveCatalogResponse{}
	if err := proto.Unmarshal(data, decodedResponse); err != nil {
		t.Fatal(err)
	}
	if decodedResponse.Remaining != remaining {
		t.Fatalf("remaining = %d, want %d", decodedResponse.Remaining, remaining)
	}
}

func TestReserveErrors(t *testing.T) {
	validationErr := &service.ValidationError{Fields: []service.FieldError{
		{Field: "listing_id", Message: "must be a UUID"},
		{Field: "qty", Message: "must be positive"},
	}}
	for _, tc := range []struct {
		name    string
		err     error
		code    codes.Code
		message string
	}{
		{name: "validation", err: validationErr, code: codes.InvalidArgument, message: "listing validation failed"},
		{name: "invalid quantity", err: repository.ErrInvalidQuantity, code: codes.InvalidArgument, message: "quantity must be positive"},
		{name: "missing listing", err: repository.ErrNotFound, code: codes.NotFound, message: "listing not found"},
		{name: "insufficient stock", err: repository.ErrInsufficientStock, code: codes.FailedPrecondition, message: "insufficient listing stock"},
		{name: "canceled", err: context.Canceled, code: codes.Canceled, message: "request canceled"},
		{name: "deadline", err: context.DeadlineExceeded, code: codes.DeadlineExceeded, message: "request deadline exceeded"},
		{name: "internal", err: errors.New("database credentials"), code: codes.Internal, message: "internal server error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			calls := 0
			h := NewGRPCHandler(reserveServiceStub{reserve: func(gotCtx context.Context, request dto.ReserveListingRequest) (*dto.ReserveListingResponse, error) {
				calls++
				if gotCtx != ctx || request != (dto.ReserveListingRequest{ListingID: "listing", Qty: 2}) {
					t.Fatalf("unexpected request: %+v", request)
				}
				return nil, fmt.Errorf("reserve listing: %w", tc.err)
			}})
			response, err := h.Reserve(ctx, &catalogpb.ReserveCatalogRequest{ListingId: "listing", Qty: 2})
			st := status.Convert(err)
			if response != nil || calls != 1 || st.Code() != tc.code || st.Message() != tc.message {
				t.Fatalf("response = %v, calls = %d, error = %v, want %v: %s", response, calls, err, tc.code, tc.message)
			}
			if tc.err == validationErr {
				details := st.Details()
				if len(details) != 1 {
					t.Fatalf("validation details = %v, want one BadRequest", details)
				}
				badRequest, ok := details[0].(*errdetails.BadRequest)
				if !ok || len(badRequest.FieldViolations) != len(validationErr.Fields) {
					t.Fatalf("unexpected validation details: %v", details)
				}
				for i, field := range validationErr.Fields {
					got := badRequest.FieldViolations[i]
					if got.Field != field.Field || got.Description != field.Message {
						t.Fatalf("violation = %v, want %+v", got, field)
					}
				}
			}
		})
	}
}

func TestReserveLastUnits(t *testing.T) {
	ctx := t.Context()
	calls := 0
	h := NewGRPCHandler(reserveServiceStub{reserve: func(gotCtx context.Context, request dto.ReserveListingRequest) (*dto.ReserveListingResponse, error) {
		calls++
		if gotCtx != ctx || request != (dto.ReserveListingRequest{ListingID: "listing", Qty: 1}) {
			t.Fatalf("unexpected request: %+v", request)
		}
		return &dto.ReserveListingResponse{Remaining: 0}, nil
	}})
	response, err := h.Reserve(ctx, &catalogpb.ReserveCatalogRequest{ListingId: "listing", Qty: 1})
	if err != nil || response == nil || response.Remaining != 0 || calls != 1 {
		t.Fatalf("response = %v, calls = %d, error = %v", response, calls, err)
	}
}
