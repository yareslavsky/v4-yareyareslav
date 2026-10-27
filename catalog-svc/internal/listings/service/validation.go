package service

import (
	"errors"
	"marketplace/catalog-svc/internal/listings/dto"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

var ErrValidation = errors.New("listing validation failed")

type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

type ValidationError struct {
	Fields []FieldError
}

func (e *ValidationError) Error() string {
	return ErrValidation.Error()
}

func (e *ValidationError) Unwrap() error {
	return ErrValidation
}

func isValidUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	_, err := uuid.Parse(value)
	return err == nil
}

func validateList(limit, offset int) error {
	var fields []FieldError
	if limit < 1 || limit > 100 {
		fields = append(fields, FieldError{Field: "limit", Message: "must be between 1 and 100"})
	}
	if offset < 0 {
		fields = append(fields, FieldError{Field: "offset", Message: "must be non-negative"})
	}
	if len(fields) > 0 {
		return &ValidationError{Fields: fields}
	}
	return nil
}

func validateCreate(request dto.CreateListingRequest) error {
	var fields []FieldError
	if !isValidUUID(request.SellerID) {
		fields = append(fields, FieldError{Field: "seller_id", Message: "must be a valid UUID"})
	}
	if !utf8.ValidString(request.Title) || strings.TrimSpace(request.Title) == "" || utf8.RuneCountInString(request.Title) > 200 {
		fields = append(fields, FieldError{Field: "title", Message: "must be non-empty and at most 200 characters"})
	}
	if request.Price <= 0 {
		fields = append(fields, FieldError{Field: "price", Message: "must be positive"})
	}
	if request.Stock == nil {
		fields = append(fields, FieldError{Field: "stock", Message: "is required"})
	} else if *request.Stock < 0 {
		fields = append(fields, FieldError{Field: "stock", Message: "must be non-negative"})
	}
	if len(fields) > 0 {
		return &ValidationError{Fields: fields}
	}
	return nil
}

func validateReserve(request dto.ReserveListingRequest) error {
	var fields []FieldError
	if request.ReservationID != "" && !isValidUUID(request.ReservationID) {
		fields = append(fields, FieldError{Field: "reservation_id", Message: "must be a valid UUID"})
	}
	if !isValidUUID(request.ListingID) {
		fields = append(fields, FieldError{Field: "listing_id", Message: "must be a valid UUID"})
	}
	if request.Qty <= 0 {
		fields = append(fields, FieldError{Field: "qty", Message: "must be positive"})
	}
	if len(fields) > 0 {
		return &ValidationError{Fields: fields}
	}
	return nil
}

func validateOrderCreated(event dto.OrderCreatedEvent) error {
	var fields []FieldError
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "event_id", value: event.EventID},
		{name: "order_id", value: event.OrderID},
		{name: "buyer_id", value: event.BuyerID},
		{name: "listing_id", value: event.ListingID},
	} {
		if !isValidUUID(field.value) {
			fields = append(fields, FieldError{Field: field.name, Message: "must be a valid UUID"})
		}
	}
	if event.Qty <= 0 {
		fields = append(fields, FieldError{Field: "qty", Message: "must be positive"})
	}
	if _, err := time.Parse(time.RFC3339Nano, event.CreatedAt); err != nil {
		fields = append(fields, FieldError{Field: "created_at", Message: "must be a valid RFC3339 timestamp"})
	}
	if len(fields) > 0 {
		return &ValidationError{Fields: fields}
	}
	return nil
}

func validateUpdate(request dto.UpdateListingRequest) error {
	var fields []FieldError
	if !isValidUUID(request.SellerID) {
		fields = append(fields, FieldError{Field: "seller_id", Message: "must be a valid UUID"})
	}
	if request.Title != nil && (!utf8.ValidString(*request.Title) || strings.TrimSpace(*request.Title) == "" || utf8.RuneCountInString(*request.Title) > 200) {
		fields = append(fields, FieldError{Field: "title", Message: "must be non-empty and at most 200 characters"})
	}
	if request.Price != nil && *request.Price <= 0 {
		fields = append(fields, FieldError{Field: "price", Message: "must be positive"})
	}
	if request.Stock != nil && *request.Stock < 0 {
		fields = append(fields, FieldError{Field: "stock", Message: "must be non-negative"})
	}
	if len(fields) > 0 {
		return &ValidationError{Fields: fields}
	}
	return nil
}
