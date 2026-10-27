package auth

import (
	"errors"
	"log"
	"marketplace/gateway/pkg/response"
	"net/http"

	"github.com/go-playground/validator"
)

type ErrorResponse struct {
	Error   string       `json:"error"`
	Message string       `json:"message"`
	Fields  []FieldError `json:"fields,omitempty"`
}

type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

const (
	ErrKeyValidationFailed = "validation_failed"
	ErrKeyConflict         = "conflict"
	ErrKeyInternal         = "internal"
	ErrKeyUnauthenticated  = "unauthenticated"
)

func writeError(
	w http.ResponseWriter,
	status int,
	code string,
	message string,
) {
	if err := response.Body(w, ErrorResponse{
		Error:   code,
		Message: message,
	}, status); err != nil {
		log.Printf("write error response: %v", err)
	}
}

func writeConflictError(w http.ResponseWriter, message string) {
	writeError(w, http.StatusConflict, ErrKeyConflict, message)
}

func writeValidationError(w http.ResponseWriter, err error) {
	fields := []response.FieldError{{Field: "body", Message: "must be a valid JSON object"}}
	var validationErr validator.ValidationErrors
	if errors.As(err, &validationErr) {
		fields = make([]response.FieldError, 0, len(validationErr))
		for _, field := range validationErr {
			fields = append(fields, response.FieldError{Field: field.Field(), Message: "must satisfy " + field.Tag()})
		}
	}
	response.ValidationError(w, fields)
}

func writeUnauthenticatedError(w http.ResponseWriter, message string) {
	writeError(w, http.StatusUnauthorized, ErrKeyUnauthenticated, message)
}

func writeInternalError(w http.ResponseWriter, message string) {
	writeError(w, http.StatusInternalServerError, ErrKeyInternal, message)
}
