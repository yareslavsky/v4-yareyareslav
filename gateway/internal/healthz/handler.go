package healthz

import (
	"context"
	"log"
	"marketplace/gateway/pkg/requestid"
	"marketplace/gateway/pkg/response"
	"marketplace/gen/catalogpb"
	"marketplace/gen/orderspb"
	"net/http"
	"time"

	"github.com/go-chi/chi"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

type HealthzHandler struct {
	catalog healthpb.HealthClient
	orders  healthpb.HealthClient
}

func NewHealthzHandler(r chi.Router, catalog, orders healthpb.HealthClient) *HealthzHandler {
	h := &HealthzHandler{catalog: catalog, orders: orders}
	r.Get("/healthz", h.CheckHealthz)
	return h
}

func (h *HealthzHandler) CheckHealthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(requestid.OutgoingContext(r.Context()), 3*time.Second)
	defer cancel()
	type result struct {
		name    string
		serving bool
	}
	results := make(chan result, 2)
	for _, backend := range []struct {
		name, service string
		client        healthpb.HealthClient
	}{
		{"catalog-svc", catalogpb.CatalogService_ServiceDesc.ServiceName, h.catalog},
		{"orders-svc", orderspb.OrdersService_ServiceDesc.ServiceName, h.orders},
	} {
		go func() {
			reply, err := backend.client.Check(ctx, &healthpb.HealthCheckRequest{Service: backend.service})
			results <- result{backend.name, err == nil && reply.GetStatus() == healthpb.HealthCheckResponse_SERVING}
		}()
	}
	for range 2 {
		select {
		case result := <-results:
			if !result.serving {
				_ = response.Body(w, response.ErrorBody{Error: "unavailable", Message: result.name + " is not serving"}, http.StatusServiceUnavailable)
				return
			}
		case <-ctx.Done():
			_ = response.Body(w, response.ErrorBody{Error: "unavailable", Message: "gRPC health check did not complete"}, http.StatusServiceUnavailable)
			return
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if _, err := w.Write([]byte("ok")); err != nil {
		log.Printf("write health response: %v", err)
	}
}
