package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"marketplace/gen/catalogpb"
	"testing"
	"testing/synctest"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestLoggingInterceptor(t *testing.T) {
	tests := []struct {
		name          string
		incoming      metadata.MD
		outgoing      metadata.MD
		err           error
		wantRequestID string
		wantCode      codes.Code
		wantLevel     string
	}{
		{name: "success with request ID", incoming: metadata.Pairs("x-request-id", "request-1"), wantRequestID: "request-1", wantCode: codes.OK, wantLevel: "INFO"},
		{name: "no metadata", wantCode: codes.OK, wantLevel: "INFO"},
		{name: "missing request ID", incoming: metadata.Pairs("other", "value"), wantCode: codes.OK, wantLevel: "INFO"},
		{name: "first request ID used", incoming: metadata.Pairs("x-request-id", "first", "x-request-id", "second"), wantRequestID: "first", wantCode: codes.OK, wantLevel: "INFO"},
		{name: "outgoing metadata ignored", outgoing: metadata.Pairs("x-request-id", "outgoing"), wantCode: codes.OK, wantLevel: "INFO"},
		{name: "internal error", err: status.Error(codes.Internal, "database failed"), wantCode: codes.Internal, wantLevel: "ERROR"},
		{name: "unknown status", err: status.Error(codes.Unknown, "unknown failure"), wantCode: codes.Unknown, wantLevel: "ERROR"},
		{name: "plain error", err: errors.New("unexpected failure"), wantCode: codes.Unknown, wantLevel: "ERROR"},
		{name: "validation error", err: status.Error(codes.InvalidArgument, "invalid quantity"), wantCode: codes.InvalidArgument, wantLevel: "INFO"},
		{name: "unavailable", err: status.Error(codes.Unavailable, "catalog unavailable"), wantCode: codes.Unavailable, wantLevel: "INFO"},
		{name: "canceled", err: status.Error(codes.Canceled, "request canceled"), wantCode: codes.Canceled, wantLevel: "INFO"},
		{name: "deadline", err: status.Error(codes.DeadlineExceeded, "request timed out"), wantCode: codes.DeadlineExceeded, wantLevel: "INFO"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var output bytes.Buffer
				previousLogger := slog.Default()
				slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
				defer slog.SetDefault(previousLogger)

				ctx := context.Background()
				if tt.incoming != nil {
					ctx = metadata.NewIncomingContext(ctx, tt.incoming)
				}
				if tt.outgoing != nil {
					ctx = metadata.NewOutgoingContext(ctx, tt.outgoing)
				}
				request := &struct{ Value string }{Value: "request"}
				response := &struct{ Value string }{Value: "response"}
				method := catalogpb.CatalogService_Create_FullMethodName
				calls := 0
				handler := func(receivedCtx context.Context, receivedRequest any) (any, error) {
					calls++
					if receivedCtx != ctx || receivedRequest != request {
						t.Error("handler received a different context or request")
					}
					if output.Len() != 0 {
						t.Error("log was written before handler completed")
					}
					time.Sleep(17 * time.Millisecond)
					return response, tt.err
				}

				got, err := LoggingInterceptor(ctx, request, &grpc.UnaryServerInfo{FullMethod: method}, handler)
				if got != response || err != tt.err {
					t.Errorf("response = %v, error = %v, want original response and error", got, err)
				}
				if calls != 1 {
					t.Errorf("handler calls = %d, want 1", calls)
				}

				decoder := json.NewDecoder(&output)
				var record map[string]any
				if err := decoder.Decode(&record); err != nil {
					t.Fatalf("decode JSON log: %v", err)
				}
				for key, want := range map[string]any{
					"msg": "gRPC request completed", "level": tt.wantLevel,
					"request_id": tt.wantRequestID, "method": method,
					"code": tt.wantCode.String(), "dur_ms": float64(17),
				} {
					if record[key] != want {
						t.Errorf("log field %q = %v, want %v", key, record[key], want)
					}
				}
				loggedError, hasError := record["error"]
				if tt.err == nil {
					if hasError {
						t.Errorf("successful call logged error: %v", loggedError)
					}
				} else if !hasError || loggedError != tt.err.Error() {
					t.Errorf("logged error = %v, want %q", loggedError, tt.err.Error())
				}
				var extra any
				if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
					t.Errorf("expected exactly one log record, got extra %v and error %v", extra, err)
				}
			})
		})
	}
}
