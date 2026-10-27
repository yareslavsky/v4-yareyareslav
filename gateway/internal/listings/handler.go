package listings

import (
	"context"
	"encoding/json"
	"io"
	"marketplace/gateway/config"
	"marketplace/gateway/internal/auth"
	"marketplace/gateway/pkg/jwt"
	"marketplace/gateway/pkg/requestid"
	"marketplace/gateway/pkg/response"
	"marketplace/gen/catalogpb"
	"net/http"
	"strconv"

	"github.com/go-chi/chi"
	"github.com/go-playground/validator"
	"github.com/google/uuid"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Service interface {
	FindAll(context.Context, int64, int64) ([]Listing, int64, error)
	FindOne(context.Context, string) (Listing, string, error)
	CreateOne(context.Context, *catalogpb.CreateCatalogRequest) (Listing, error)
	PatchOne(context.Context, *catalogpb.UpdateCatalogRequest) (Listing, error)
	DeleteOne(context.Context, string, string) error
}

type ListingsHandler struct{ service Service }

func NewListingsHandler(r chi.Router, _ *validator.Validate, ac config.AuthConfig, service Service) *ListingsHandler {
	h := &ListingsHandler{service: service}
	lr := chi.NewRouter()
	lr.Get("/", h.GetAllListings)
	lr.Get("/{id}", h.GetOneListing)
	lr.Post("/", ac.Middleware.CheckUser(h.PostListing(), auth.Seller))
	lr.Patch("/{id}", ac.Middleware.CheckUser(h.PatchListing(), auth.Seller))
	lr.Delete("/{id}", ac.Middleware.CheckUser(h.DeleteListing(), auth.Seller))
	r.Mount("/listings", lr)
	return h
}

type fieldError = response.FieldError
type errorBody = response.ErrorBody

func writeJSON(w http.ResponseWriter, body any, code int) { _ = response.Body(w, body, code) }
func validationError(w http.ResponseWriter, fields []fieldError) {
	response.ValidationError(w, fields)
}
func writeRPCError(w http.ResponseWriter, err error) {
	response.GRPCError(w, err)
}
func requestContext(r *http.Request) context.Context {
	return requestid.OutgoingContext(r.Context())
}
func listingID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if _, err := uuid.Parse(id); err != nil || len(id) != 36 {
		writeRPCError(w, status.Error(codes.NotFound, "listing not found"))
		return "", false
	}
	return id, true
}
func readBody(w http.ResponseWriter, r *http.Request) (listingRequest, bool) {
	var request *listingRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := decoder.Decode(&request); err != nil || request == nil {
		validationError(w, []fieldError{{Field: "body", Message: "must be a JSON object"}})
		return listingRequest{}, false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		validationError(w, []fieldError{{Field: "body", Message: "must contain one JSON object"}})
		return listingRequest{}, false
	}
	return *request, true
}

func (h *ListingsHandler) GetAllListings(w http.ResponseWriter, r *http.Request) {
	limit, offset := int64(20), int64(0)
	var fields []fieldError
	for _, parameter := range []struct {
		name  string
		value *int64
	}{{"limit", &limit}, {"offset", &offset}} {
		if values, ok := r.URL.Query()[parameter.name]; ok {
			value, err := strconv.ParseInt(values[0], 10, 64)
			if err != nil || (parameter.name == "limit" && (value < 1 || value > 100)) || (parameter.name == "offset" && value < 0) {
				fields = append(fields, fieldError{Field: parameter.name, Message: "is out of range or not an integer"})
			} else {
				*parameter.value = value
			}
		}
	}
	if len(fields) > 0 {
		validationError(w, fields)
		return
	}
	items, total, err := h.service.FindAll(requestContext(r), limit, offset)
	if err != nil {
		writeRPCError(w, err)
		return
	}
	w.Header().Set("X-Total-Count", strconv.FormatInt(total, 10))
	writeJSON(w, items, http.StatusOK)
}
func (h *ListingsHandler) GetOneListing(w http.ResponseWriter, r *http.Request) {
	id, ok := listingID(w, r)
	if !ok {
		return
	}
	item, cache, err := h.service.FindOne(requestContext(r), id)
	if err != nil {
		writeRPCError(w, err)
		return
	}
	w.Header().Set("X-Cache", cache)
	writeJSON(w, item, http.StatusOK)
}
func (h *ListingsHandler) PostListing() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		request, ok := readBody(w, r)
		if !ok {
			return
		}
		price, fields := request.validate(true)
		if len(fields) > 0 {
			validationError(w, fields)
			return
		}
		item, err := h.service.CreateOne(requestContext(r), &catalogpb.CreateCatalogRequest{SellerId: jwt.UserID(r.Context()), Title: *request.Title, Price: *price, Stock: request.Stock})
		if err != nil {
			writeRPCError(w, err)
			return
		}
		writeJSON(w, item, http.StatusCreated)
	}
}
func (h *ListingsHandler) PatchListing() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := listingID(w, r)
		if !ok {
			return
		}
		request, ok := readBody(w, r)
		if !ok {
			return
		}
		price, fields := request.validate(false)
		if len(fields) > 0 {
			validationError(w, fields)
			return
		}
		item, err := h.service.PatchOne(requestContext(r), &catalogpb.UpdateCatalogRequest{Id: id, SellerId: jwt.UserID(r.Context()), Title: request.Title, Price: price, Stock: request.Stock})
		if err != nil {
			writeRPCError(w, err)
			return
		}
		writeJSON(w, item, http.StatusOK)
	}
}
func (h *ListingsHandler) DeleteListing() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := listingID(w, r)
		if !ok {
			return
		}
		if err := h.service.DeleteOne(requestContext(r), id, jwt.UserID(r.Context())); err != nil {
			writeRPCError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
