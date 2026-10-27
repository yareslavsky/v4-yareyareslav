package response_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"marketplace/gateway/config"
	"marketplace/gateway/internal/auth"
	"marketplace/gateway/internal/listings"
	"marketplace/gateway/internal/orders"
	"marketplace/gateway/pkg/cors"
	"marketplace/gateway/pkg/httplog"
	"marketplace/gateway/pkg/jwt"
	"marketplace/gateway/pkg/recoverer"
	"marketplace/gateway/pkg/requestid"
	"marketplace/gateway/pkg/response"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi"
	"github.com/go-playground/validator"
)

func TestRouterErrorsAreJSON(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	router := chi.NewRouter()
	router.NotFound(response.NotFound)
	router.MethodNotAllowed(response.MethodNotAllowed)
	router.Use(requestid.Middleware)
	router.Use(httplog.Middleware(logger))
	router.Use(recoverer.Middleware(logger))
	router.Use(cors.Middleware)
	router.Get("/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	router.Get("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	ac := config.NewAuthConfig(jwt.NewJWTMiddleware(jwt.NewManager("test-secret", jwt.AccessTTL, jwt.RefreshTTL)))
	auth.NewAuthHandler(router, validator.New(), nil)
	listings.NewListingsHandler(router, nil, ac, nil)
	orders.NewOrdersHandler(router, nil, ac, nil)
	for _, test := range []struct {
		method, path string
		status       int
		name         string
	}{
		{"GET", "/missing", 404, "not_found"},
		{"POST", "/missing", 404, "not_found"},
		{"GET", "/favicon.ico", 404, "not_found"},
		{"GET", "/auth/missing", 404, "not_found"},
		{"GET", "/orders/missing", 404, "not_found"},
		{"POST", "/listings/11111111-1111-4111-8111-111111111111/buy", 404, "not_found"},
		{"GET", "/listings/id/extra", 404, "not_found"},
		{"POST", "/", 405, "method_not_allowed"},
		{"POST", "/healthz", 405, "method_not_allowed"},
		{"GET", "/auth/login", 405, "method_not_allowed"},
		{"DELETE", "/orders", 405, "method_not_allowed"},
		{"PUT", "/listings", 405, "method_not_allowed"},
		{"POST", "/listings/id", 405, "method_not_allowed"},
		{"OPTIONS", "/orders", 405, "method_not_allowed"},
	} {
		t.Run(test.method+test.path, func(t *testing.T) {
			logs.Reset()
			request := httptest.NewRequest(test.method, test.path, nil)
			request.Header.Set("X-Request-ID", "routing-request")
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			if recorder.Code != test.status {
				t.Fatalf("status %d, body %s", recorder.Code, recorder.Body.String())
			}
			if recorder.Header().Get("Content-Type") != "application/json" {
				t.Error("expected JSON")
			}
			if recorder.Header().Get("X-Request-ID") != "routing-request" || recorder.Header().Get("Access-Control-Allow-Origin") != "*" {
				t.Error("middleware headers missing")
			}
			var body map[string]any
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body["error"] != test.name || body["message"] == "" || len(body) != 2 {
				t.Errorf("error body: %v", body)
			}
			lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
			if len(lines) != 1 {
				t.Fatalf("request log count: %d", len(lines))
			}
			var logEntry map[string]any
			if err := json.Unmarshal([]byte(lines[0]), &logEntry); err != nil {
				t.Fatal(err)
			}
			if logEntry["status"] != float64(test.status) {
				t.Errorf("logged status: %v", logEntry["status"])
			}
		})
	}
}
