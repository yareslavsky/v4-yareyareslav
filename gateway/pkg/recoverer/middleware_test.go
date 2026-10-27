package recoverer

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"marketplace/gateway/pkg/httplog"
	"marketplace/gateway/pkg/requestid"
	"marketplace/gateway/pkg/response"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPanicReturnsJSONAndLogsStatus(t *testing.T) {
	for _, value := range []any{"sensitive panic detail", nil, 42, []string{"panic"}} {
		t.Run("panic", func(t *testing.T) {
			var output bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&output, nil))
			handler := requestid.Middleware(httplog.Middleware(logger)(Middleware(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(value) }))))
			request := httptest.NewRequest("GET", "/panic", nil)
			request.Header.Set("X-Request-ID", "panic-request")
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != 500 || recorder.Header().Get("Content-Type") != "application/json" || recorder.Header().Get("X-Request-ID") != "panic-request" {
				t.Fatalf("status %d, headers %v", recorder.Code, recorder.Header())
			}
			var body response.ErrorBody
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error != "internal" || body.Message != "internal server error" || len(body.Fields) != 0 {
				t.Errorf("error body: %+v", body)
			}
			lines := strings.Split(strings.TrimSpace(output.String()), "\n")
			if len(lines) != 2 {
				t.Fatalf("expected panic log and one request log, got %d: %s", len(lines), output.String())
			}
			for index, line := range lines {
				var entry map[string]any
				if err := json.Unmarshal([]byte(line), &entry); err != nil {
					t.Fatal(err)
				}
				if entry["level"] != "ERROR" || entry["request_id"] != "panic-request" || entry["path"] != "/panic" {
					t.Errorf("entry: %v", entry)
				}
				if index == 0 && entry["stack"] == "" {
					t.Error("stack missing")
				}
				if index == 1 && (entry["status"] != float64(500) || entry["msg"] != "HTTP request") {
					t.Errorf("request log: %v", entry)
				}
			}
		})
	}
}

func TestNormalResponseIsUnchanged(t *testing.T) {
	var output bytes.Buffer
	handler := Middleware(slog.New(slog.NewJSONHandler(&output, nil)))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(201)
		_, _ = w.Write([]byte("created"))
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest("POST", "/", nil))
	if recorder.Code != 201 || recorder.Body.String() != "created" || output.Len() != 0 {
		t.Fatalf("status %d, body %s, logs %s", recorder.Code, recorder.Body.String(), output.String())
	}
}

func TestPanicAfterResponseStartedAborts(t *testing.T) {
	var output bytes.Buffer
	handler := Middleware(slog.New(slog.NewJSONHandler(&output, nil)))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("partial"))
		panic("failed after write")
	}))
	recorder := httptest.NewRecorder()
	defer func() {
		if value := recover(); value != http.ErrAbortHandler {
			t.Errorf("panic: %v, want ErrAbortHandler", value)
		}
		if recorder.Body.String() != "partial" {
			t.Error("JSON error appended to partial response")
		}
		if output.Len() == 0 {
			t.Error("panic was not logged")
		}
	}()
	handler.ServeHTTP(recorder, httptest.NewRequest("GET", "/", nil))
	t.Fatal("partial response panic was swallowed")
}

func TestAbortHandlerIsPreserved(t *testing.T) {
	var output bytes.Buffer
	handler := Middleware(slog.New(slog.NewJSONHandler(&output, nil)))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) }))
	defer func() {
		if value := recover(); value != http.ErrAbortHandler {
			t.Errorf("panic: %v, want ErrAbortHandler", value)
		}
		if output.Len() != 0 {
			t.Error("intentional connection abort was logged as a panic")
		}
	}()
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	t.Fatal("intentional abort was swallowed")
}

func TestAbortedResponseStillHasRequestLog(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	handler := requestid.Middleware(httplog.Middleware(logger)(Middleware(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("partial"))
		panic("failed after response started")
	}))))
	request := httptest.NewRequest("POST", "/partial", nil)
	request.Header.Set("X-Request-ID", "partial-request")
	recorder := httptest.NewRecorder()
	defer func() {
		if value := recover(); value != http.ErrAbortHandler {
			t.Errorf("panic: %v", value)
		}
		if recorder.Code != 202 || recorder.Body.String() != "partial" {
			t.Error("committed response was changed")
		}
		lines := strings.Split(strings.TrimSpace(output.String()), "\n")
		if len(lines) != 2 {
			t.Fatalf("expected panic and request logs, got %d: %s", len(lines), output.String())
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(lines[1]), &entry); err != nil {
			t.Fatal(err)
		}
		if entry["msg"] != "HTTP request" || entry["request_id"] != "partial-request" || entry["status"] != float64(202) || entry["level"] != "INFO" {
			t.Errorf("request log: %v", entry)
		}
	}()
	handler.ServeHTTP(recorder, request)
	t.Fatal("connection abort was swallowed")
}
