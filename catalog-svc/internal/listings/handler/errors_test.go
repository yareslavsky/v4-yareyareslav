package handler

import (
	"context"
	"errors"
	"fmt"
	"marketplace/catalog-svc/internal/listings/repository"
	"marketplace/catalog-svc/internal/listings/service"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestToGRPCError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code codes.Code
	}{
		{"nil", nil, codes.OK},
		{"canceled", context.Canceled, codes.Canceled},
		{"deadline", context.DeadlineExceeded, codes.DeadlineExceeded},
		{"validation", service.ErrValidation, codes.InvalidArgument},
		{"quantity", repository.ErrInvalidQuantity, codes.InvalidArgument},
		{"not found", repository.ErrNotFound, codes.NotFound},
		{"forbidden", repository.ErrForbidden, codes.PermissionDenied},
		{"stock", repository.ErrInsufficientStock, codes.FailedPrecondition},
		{"unknown", errors.New("database credentials"), codes.Internal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.err
			if err != nil {
				err = fmt.Errorf("service: %w", err)
			}
			got := toGRPCError(err)
			if code := status.Code(got); code != tt.code {
				t.Fatalf("code = %v, want %v", code, tt.code)
			}
			if tt.code == codes.Internal && status.Convert(got).Message() != "internal server error" {
				t.Fatalf("internal error exposed: %v", got)
			}
		})
	}
}

func TestToGRPCErrorValidationDetails(t *testing.T) {
	err := fmt.Errorf("service create: %w", &service.ValidationError{
		Fields: []service.FieldError{{Field: "stock", Message: "is required"}},
	})
	st := status.Convert(toGRPCError(err))
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", st.Code())
	}
	details := st.Details()
	if len(details) != 1 {
		t.Fatalf("details = %v, want one BadRequest", details)
	}
	badRequest, ok := details[0].(*errdetails.BadRequest)
	if !ok || len(badRequest.FieldViolations) != 1 {
		t.Fatalf("unexpected validation details: %v", details)
	}
	field := badRequest.FieldViolations[0]
	if field.Field != "stock" || field.Description != "is required" {
		t.Fatalf("unexpected field violation: %v", field)
	}
}
