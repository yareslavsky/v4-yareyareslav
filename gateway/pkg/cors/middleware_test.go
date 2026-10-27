package cors

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"marketplace/gateway/pkg/httplog"
	"marketplace/gateway/pkg/jwt"
	"marketplace/gateway/pkg/recoverer"
	"marketplace/gateway/pkg/requestid"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi"
)

func checkHeaders(t *testing.T, headers http.Header) {
	t.Helper()
	if headers.Get("Access-Control-Allow-Origin") != "*" {
		t.Error("origin must be wildcard")
	}
	for name, values := range map[string][]string{
		"Access-Control-Allow-Methods":  {"GET", "POST", "PATCH", "DELETE", "OPTIONS"},
		"Access-Control-Allow-Headers":  {"Authorization", "Content-Type", "Idempotency-Key", "X-Request-ID"},
		"Access-Control-Expose-Headers": {"X-Total-Count", "X-Request-ID", "X-Cache", "Idempotent-Replayed"},
	} {
		allowed := map[string]bool{}
		for _, value := range strings.Split(headers.Get(name), ",") {
			allowed[strings.TrimSpace(value)] = true
		}
		for _, value := range values {
			if !allowed[value] {
				t.Errorf("%s missing %s", name, value)
			}
		}
	}
}

func TestCORSWithMiddlewareChain(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	router := chi.NewRouter()
	router.Use(requestid.Middleware)
	router.Use(httplog.Middleware(logger))
	router.Use(recoverer.Middleware(logger))
	router.Use(Middleware)
	manager := jwt.NewManager("test-secret", jwt.AccessTTL, jwt.RefreshTTL)
	called := false
	router.Post("/orders", jwt.NewJWTMiddleware(manager).CheckUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(201) }), "buyer"))
	router.Get("/panic", func(http.ResponseWriter, *http.Request) { panic("failure") })
	router.Get("/", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	for _, test := range []struct {
		name, method, path, origin string
		code                       int
	}{
		{"preflight before auth", "OPTIONS", "/orders", "http://localhost:5173", 204},
		{"preflight without request method", "OPTIONS", "/orders", "http://localhost:5173", 204},
		{"unauthorized", "POST", "/orders", "http://localhost:5173", 401},
		{"panic", "GET", "/panic", "http://localhost:5173", 500},
		{"public response", "GET", "/", "http://localhost:5173", 200},
		{"unknown route", "GET", "/unknown", "http://localhost:5173", 404},
		{"options without origin", "OPTIONS", "/orders", "", 405},
	} {
		t.Run(test.name, func(t *testing.T) {
			logs.Reset()
			request := httptest.NewRequest(test.method, test.path, nil)
			request.Header.Set("Origin", test.origin)
			request.Header.Set("X-Request-ID", "cors-request")
			if test.name == "preflight before auth" {
				request.Header.Set("Access-Control-Request-Method", "POST")
				request.Header.Set("Access-Control-Request-Headers", "authorization,content-type,idempotency-key")
			}
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			if recorder.Code != test.code {
				t.Fatalf("status %d, body %s", recorder.Code, recorder.Body.String())
			}
			checkHeaders(t, recorder.Header())
			if recorder.Header().Get("X-Request-ID") != "cors-request" {
				t.Error("request ID missing")
			}
			if test.code == 204 && recorder.Body.Len() != 0 {
				t.Error("preflight must have no body")
			}
			lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
			var entry map[string]any
			if err := json.Unmarshal([]byte(lines[len(lines)-1]), &entry); err != nil {
				t.Fatal(err)
			}
			if entry["status"] != float64(test.code) || entry["request_id"] != "cors-request" {
				t.Errorf("request log: %v", entry)
			}
		})
	}
	if called {
		t.Error("preflight or unauthorized request reached private handler")
	}
}
