package healthz

import (
	"context"
	"encoding/json"
	"marketplace/gateway/pkg/requestid"
	"marketplace/gateway/pkg/response"
	"marketplace/gen/catalogpb"
	"marketplace/gen/orderspb"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type healthServer struct {
	healthpb.UnimplementedHealthServer
	check func(context.Context, *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error)
}

func (s *healthServer) Check(ctx context.Context, req *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	return s.check(ctx, req)
}

func testClient(t *testing.T, check func(context.Context, *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error)) healthpb.HealthClient {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	healthpb.RegisterHealthServer(server, &healthServer{check: check})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	conn, err := grpc.NewClient("passthrough:///health-test", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return healthpb.NewHealthClient(conn)
}
func healthRequest(catalog, orders healthpb.HealthClient) *httptest.ResponseRecorder {
	router := chi.NewRouter()
	router.Use(requestid.Middleware)
	NewHealthzHandler(router, catalog, orders)
	request := httptest.NewRequest("GET", "/healthz", nil)
	request.Header.Set("X-Request-ID", "health-request")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}
func assertUnavailable(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, body %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Content-Type") != "application/json" {
		t.Error("expected JSON error")
	}
	var body response.ErrorBody
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error != "unavailable" || body.Message == "" || len(body.Fields) != 0 {
		t.Errorf("error: %+v", body)
	}
}

func TestBothBackendsServing(t *testing.T) {
	started := []chan struct{}{make(chan struct{}), make(chan struct{})}
	var calls atomic.Int32
	client := func(index int, service string) healthpb.HealthClient {
		return testClient(t, func(ctx context.Context, req *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
			calls.Add(1)
			if req.Service != service {
				t.Errorf("service: %s, want %s", req.Service, service)
			}
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > 3*time.Second {
				t.Error("expected three-second deadline")
			}
			md, _ := metadata.FromIncomingContext(ctx)
			if values := md.Get("x-request-id"); len(values) != 1 || values[0] != "health-request" {
				t.Errorf("request ID: %v", values)
			}
			close(started[index])
			select {
			case <-started[1-index]:
			case <-ctx.Done():
				return nil, status.Error(codes.DeadlineExceeded, "checks did not run concurrently")
			}
			return &healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING}, nil
		})
	}
	recorder := healthRequest(client(0, catalogpb.CatalogService_ServiceDesc.ServiceName), client(1, orderspb.OrdersService_ServiceDesc.ServiceName))
	if recorder.Code != 200 || recorder.Body.String() != "ok" {
		t.Fatalf("status %d, body %s", recorder.Code, recorder.Body.String())
	}
	if calls.Load() != 2 {
		t.Errorf("health checks: %d", calls.Load())
	}
	if recorder.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Error("wrong success content type")
	}
}

func TestUnhealthyBackend(t *testing.T) {
	for _, backend := range []string{"catalog-svc", "orders-svc"} {
		for _, test := range []struct {
			name  string
			reply healthpb.HealthCheckResponse_ServingStatus
			err   error
		}{
			{name: "not serving", reply: healthpb.HealthCheckResponse_NOT_SERVING},
			{name: "unknown", reply: healthpb.HealthCheckResponse_UNKNOWN},
			{name: "service unknown", reply: healthpb.HealthCheckResponse_SERVICE_UNKNOWN},
			{name: "unreachable", err: status.Error(codes.Unavailable, "disconnected")},
			{name: "deadline", err: status.Error(codes.DeadlineExceeded, "timeout")},
			{name: "health not registered", err: status.Error(codes.Unimplemented, "no health service")},
			{name: "named service missing", err: status.Error(codes.NotFound, "unknown service")},
		} {
			t.Run(backend+"/"+test.name, func(t *testing.T) {
				client := func(name string) healthpb.HealthClient {
					return testClient(t, func(context.Context, *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
						if name == backend {
							return &healthpb.HealthCheckResponse{Status: test.reply}, test.err
						}
						return &healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING}, nil
					})
				}
				assertUnavailable(t, healthRequest(client("catalog-svc"), client("orders-svc")))
			})
		}
	}
}

func TestHealthDeadline(t *testing.T) {
	slow := testClient(t, func(ctx context.Context, _ *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
		<-ctx.Done()
		return nil, status.Error(codes.DeadlineExceeded, "timeout")
	})
	healthy := testClient(t, func(context.Context, *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
		return &healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING}, nil
	})
	started := time.Now()
	recorder := healthRequest(slow, healthy)
	assertUnavailable(t, recorder)
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("health took %s", elapsed)
	}
}
