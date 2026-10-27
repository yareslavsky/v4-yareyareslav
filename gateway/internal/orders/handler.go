package orders

import (
	"context"
	"encoding/json"
	"io"
	"marketplace/gateway/config"
	"marketplace/gateway/internal/auth"
	"marketplace/gateway/pkg/jwt"
	"marketplace/gateway/pkg/requestid"
	"marketplace/gateway/pkg/response"
	"marketplace/gen/orderspb"
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/go-chi/chi"
	"github.com/go-playground/validator"
	"github.com/google/uuid"
)

type Service interface {
	CreateOrders(context.Context, *orderspb.CreateOrderRequest) (Order, bool, error)
	FindOrders(context.Context, string, int32, int32) ([]Order, int64, error)
}

type OrdersHandler struct{ service Service }

func NewOrdersHandler(r chi.Router, _ *validator.Validate, ac config.AuthConfig, service Service) *OrdersHandler {
	h := &OrdersHandler{service: service}
	ordersRouter := chi.NewRouter()
	ordersRouter.Post("/", ac.Middleware.CheckUser(h.PostOrders(), auth.Buyer))
	ordersRouter.Get("/", ac.Middleware.CheckUser(h.GetOrders(), auth.Buyer))
	r.Mount("/orders", ordersRouter)
	return h
}

func requestContext(r *http.Request) context.Context {
	return requestid.OutgoingContext(r.Context())
}

func (h *OrdersHandler) GetOrders() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit, offset := int32(20), int32(0)
		var fields []response.FieldError
		for _, parameter := range []struct {
			name  string
			value *int32
		}{{"limit", &limit}, {"offset", &offset}} {
			if values, ok := r.URL.Query()[parameter.name]; ok {
				value, err := strconv.ParseInt(values[0], 10, 32)
				if err != nil || (parameter.name == "limit" && (value < 1 || value > 100)) || (parameter.name == "offset" && value < 0) {
					fields = append(fields, response.FieldError{Field: parameter.name, Message: "is out of range or not an integer"})
				} else {
					*parameter.value = int32(value)
				}
			}
		}
		if len(fields) > 0 {
			response.ValidationError(w, fields)
			return
		}
		orders, total, err := h.service.FindOrders(requestContext(r), jwt.UserID(r.Context()), limit, offset)
		if err != nil {
			response.GRPCError(w, err)
			return
		}
		w.Header().Set("X-Total-Count", strconv.FormatInt(total, 10))
		_ = response.Body(w, orders, http.StatusOK)
	}
}

func (h *OrdersHandler) PostOrders() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var request *struct {
			ListingID string `json:"listing_id"`
			Qty       int32  `json:"qty"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		if err := decoder.Decode(&request); err != nil || request == nil {
			response.ValidationError(w, []response.FieldError{{Field: "body", Message: "must be a JSON object"}})
			return
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			response.ValidationError(w, []response.FieldError{{Field: "body", Message: "must contain one JSON object"}})
			return
		}
		var fields []response.FieldError
		if _, err := uuid.Parse(request.ListingID); err != nil || len(request.ListingID) != 36 {
			fields = append(fields, response.FieldError{Field: "listing_id", Message: "must be a valid UUID"})
		}
		if request.Qty <= 0 {
			fields = append(fields, response.FieldError{Field: "qty", Message: "must be positive"})
		}
		key := r.Header.Get("Idempotency-Key")
		if utf8.RuneCountInString(key) > 200 {
			fields = append(fields, response.FieldError{Field: "Idempotency-Key", Message: "must be at most 200 characters"})
		}
		if len(fields) > 0 {
			response.ValidationError(w, fields)
			return
		}
		order, replayed, err := h.service.CreateOrders(requestContext(r), &orderspb.CreateOrderRequest{
			BuyerId: jwt.UserID(r.Context()), ListingId: request.ListingID, Qty: request.Qty, IdempotencyKey: key,
		})
		if err != nil {
			response.GRPCError(w, err)
			return
		}
		code := http.StatusCreated
		if replayed {
			code = http.StatusOK
			w.Header().Set("Idempotent-Replayed", "true")
		}
		_ = response.Body(w, order, code)
	}
}
