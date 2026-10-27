package httplog

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"marketplace/gateway/pkg/requestid"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/go-chi/chi"
)

func TestLogRecordsResponse(t *testing.T) {
	for _, test := range []struct {
		name    string
		handler http.HandlerFunc
		status  int
		level   string
	}{
		{"implicit empty response", func(http.ResponseWriter, *http.Request) {}, 200, "INFO"},
		{"implicit write", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) }, 200, "INFO"},
		{"created", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(201) }, 201, "INFO"},
		{"unauthorized", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }, 401, "INFO"},
		{"internal", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }, 500, "ERROR"},
		{"unavailable", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }, 503, "ERROR"},
		{"first status wins", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(201); w.WriteHeader(500) }, 201, "INFO"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&output, nil))
			handler := requestid.Middleware(Middleware(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if output.Len() != 0 {
					t.Error("request logged before response")
				}
				test.handler.ServeHTTP(w, r)
				if output.Len() != 0 {
					t.Error("request logged before handler completed")
				}
			})))
			request := httptest.NewRequest("POST", "/orders?limit=5", nil)
			request.RemoteAddr = "192.0.2.1:54321"
			request.Header.Set("X-Request-ID", "fixed-id")
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != test.status {
				t.Errorf("response status: %d", recorder.Code)
			}
			lines := strings.Split(strings.TrimSpace(output.String()), "\n")
			if len(lines) != 1 {
				t.Fatalf("log lines: %d", len(lines))
			}
			var record map[string]any
			if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
				t.Fatal(err)
			}
			for key, want := range map[string]any{"request_id": "fixed-id", "method": "POST", "path": "/orders", "status": float64(test.status), "remote_addr": "192.0.2.1:54321", "level": test.level} {
				if record[key] != want {
					t.Errorf("%s: %v, want %v", key, record[key], want)
				}
			}
			if duration, ok := record["dur_ms"].(float64); !ok || duration < 0 {
				t.Errorf("invalid duration: %v", record["dur_ms"])
			}
		})
	}
}

func TestLogMeasuresHandlerDuration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var output bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&output, nil))
		handler := Middleware(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(250 * time.Millisecond); w.WriteHeader(204) }))
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
		var record map[string]any
		if err := json.Unmarshal(output.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		if record["dur_ms"] != float64(250) {
			t.Errorf("duration: %v, want 250ms", record["dur_ms"])
		}
	})
}

func TestLogUnknownRouteWithGeneratedID(t *testing.T) {
	var output bytes.Buffer
	router := chi.NewRouter()
	router.Use(requestid.Middleware)
	router.Use(Middleware(slog.New(slog.NewJSONHandler(&output, nil))))
	router.Get("/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest("GET", "/missing", nil))
	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["status"] != float64(404) || record["level"] != "INFO" || record["request_id"] != recorder.Header().Get("X-Request-ID") || record["request_id"] == "" {
		t.Errorf("record: %v", record)
	}
}
