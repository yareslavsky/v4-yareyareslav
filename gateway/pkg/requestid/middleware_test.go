package requestid

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi"
	"github.com/google/uuid"
	"google.golang.org/grpc/metadata"
)

func TestRequestIDAcrossStatuses(t *testing.T) {
	for _, input := range []string{"", "acc-12-fixed"} {
		for _, code := range []int{200, 401, 403, 500} {
			request := httptest.NewRequest("GET", "/", nil)
			request.Header.Set("X-Request-ID", input)
			recorder := httptest.NewRecorder()
			var contextID string
			Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				contextID = ID(r.Context())
				if contextID != w.Header().Get("X-Request-ID") {
					t.Error("context and response have different IDs")
				}
				if input != "" && contextID != input {
					t.Error("incoming ID was replaced")
				}
				w.WriteHeader(code)
			})).ServeHTTP(recorder, request)
			if recorder.Code != code {
				t.Errorf("status: %d", recorder.Code)
			}
			if input == "" {
				id, err := uuid.Parse(contextID)
				if err != nil || id.Version() != 4 || len(contextID) != 36 {
					t.Fatalf("invalid generated UUID: %q", contextID)
				}
			}
		}
	}
}

func TestGeneratedIDsAreDistinct(t *testing.T) {
	router := chi.NewRouter()
	router.Use(Middleware)
	router.Get("/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	ids := map[string]bool{}
	for range 20 {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest("GET", "/", nil))
		id := recorder.Header().Get("X-Request-ID")
		if id == "" || ids[id] {
			t.Fatalf("missing or repeated ID: %q", id)
		}
		ids[id] = true
	}
	missing := httptest.NewRecorder()
	router.ServeHTTP(missing, httptest.NewRequest("GET", "/missing", nil))
	if missing.Code != 404 || missing.Header().Get("X-Request-ID") == "" {
		t.Fatal("unknown route has no request ID")
	}
}

func TestOutgoingMetadataUsesContextID(t *testing.T) {
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("other-header", "keep", "x-request-id", "old", "x-request-id", "duplicate"))
	request := httptest.NewRequest("GET", "/", nil).WithContext(ctx)
	request.Header.Set("X-Request-ID", "original-id")
	Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("X-Request-ID", "changed-id")
		outgoing := OutgoingContext(r.Context())
		md, _ := metadata.FromOutgoingContext(outgoing)
		if values := md.Get("x-request-id"); len(values) != 1 || values[0] != "original-id" {
			t.Errorf("outgoing request ID: %v", values)
		}
		if values := md.Get("other-header"); len(values) != 1 || values[0] != "keep" {
			t.Errorf("other metadata lost: %v", values)
		}
		original, _ := metadata.FromOutgoingContext(ctx)
		if values := original.Get("x-request-id"); len(values) != 2 || values[0] != "old" {
			t.Error("original metadata was changed")
		}
	})).ServeHTTP(httptest.NewRecorder(), request)
}
