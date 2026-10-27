package response

import (
	"net/http"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

type ErrorBody struct {
	Error   string       `json:"error"`
	Message string       `json:"message"`
	Fields  []FieldError `json:"fields,omitempty"`
}

func NotFound(w http.ResponseWriter, _ *http.Request) {
	_ = Body(w, ErrorBody{Error: "not_found", Message: "route not found"}, http.StatusNotFound)
}

func MethodNotAllowed(w http.ResponseWriter, _ *http.Request) {
	_ = Body(w, ErrorBody{Error: "method_not_allowed", Message: "method is not allowed for this route"}, http.StatusMethodNotAllowed)
}

func ValidationError(w http.ResponseWriter, fields []FieldError) {
	_ = Body(w, ErrorBody{"validation_failed", "request is invalid", fields}, http.StatusBadRequest)
}

func GRPCError(w http.ResponseWriter, err error) {
	code, name, message := http.StatusInternalServerError, "internal", "internal server error"
	var fields []FieldError
	st := status.Convert(err)
	switch st.Code() {
	case codes.NotFound:
		code, name, message = http.StatusNotFound, "not_found", "resource not found"
	case codes.InvalidArgument:
		code, name, message = http.StatusBadRequest, "validation_failed", "request is invalid"
		for _, detail := range st.Details() {
			if detail, ok := detail.(*errdetails.BadRequest); ok {
				for _, field := range detail.FieldViolations {
					fields = append(fields, FieldError{field.Field, field.Description})
				}
			}
		}
		if len(fields) == 0 {
			fields = []FieldError{{"request", st.Message()}}
		}
	case codes.PermissionDenied:
		code, name, message = http.StatusForbidden, "forbidden", "operation is not permitted"
	case codes.FailedPrecondition, codes.AlreadyExists, codes.Aborted:
		code, name, message = http.StatusConflict, "conflict", "request conflicts with current state"
	case codes.Unavailable, codes.DeadlineExceeded:
		code, name, message = http.StatusServiceUnavailable, "unavailable", "service is unavailable"
	}
	_ = Body(w, ErrorBody{name, message, fields}, code)
}
