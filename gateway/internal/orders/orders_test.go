package orders

import (
	"context"
	"encoding/json"
	"marketplace/gateway/config"
	"marketplace/gateway/pkg/jwt"
	"marketplace/gateway/pkg/requestid"
	"marketplace/gateway/pkg/response"
	"marketplace/gen/orderspb"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const buyerID = "11111111-1111-4111-8111-111111111111"
const listingID = "22222222-2222-4222-8222-222222222222"
const orderID = "33333333-3333-4333-8333-333333333333"

type ordersStub struct {
	orderspb.UnimplementedOrdersServiceServer
	create func(context.Context, *orderspb.CreateOrderRequest) (*orderspb.CreateOrderResponse, error)
	list   func(context.Context, *orderspb.ListOrdersRequest) (*orderspb.ListOrdersResponse, error)
}

func (s *ordersStub) Create(ctx context.Context, req *orderspb.CreateOrderRequest) (*orderspb.CreateOrderResponse, error) {
	return s.create(ctx, req)
}
func (s *ordersStub) List(ctx context.Context, req *orderspb.ListOrdersRequest) (*orderspb.ListOrdersResponse, error) {
	return s.list(ctx, req)
}

func testRouter(t *testing.T, stub *ordersStub) (http.Handler, *jwt.Manager) {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	orderspb.RegisterOrdersServiceServer(server, stub)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	conn, err := grpc.NewClient("passthrough:///orders-test", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	manager := jwt.NewManager("test-secret", 5*time.Minute, 7*24*time.Hour)
	router := chi.NewRouter()
	router.Use(requestid.Middleware)
	NewOrdersHandler(router, nil, config.NewAuthConfig(jwt.NewJWTMiddleware(manager)), NewOrdersService(orderspb.NewOrdersServiceClient(conn)))
	return router, manager
}
func doRequest(router http.Handler, method, path, body, token, key string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("X-Request-ID", "order-request")
	request.Header.Set("Idempotency-Key", key)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}
func token(t *testing.T, manager *jwt.Manager, role string) string {
	t.Helper()
	value, err := manager.GenerateAccessToken(buyerID, role)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func sampleOrder() *orderspb.Order {
	return &orderspb.Order{Id: orderID, BuyerId: buyerID, ListingId: listingID, Qty: 3, CreatedAt: timestamppb.New(time.Date(2025, 5, 15, 10, 0, 0, 0, time.UTC))}
}
func checkContext(t *testing.T, ctx context.Context) {
	t.Helper()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 3*time.Second {
		t.Error("expected three-second deadline")
	}
	md, _ := metadata.FromIncomingContext(ctx)
	if values := md.Get("x-request-id"); len(values) != 1 || values[0] != "order-request" {
		t.Errorf("request ID: %v", values)
	}
}

func TestCreateOrderAndReplay(t *testing.T) {
	for _, replay := range []bool{false, true} {
		name := "created"
		if replay {
			name = "replayed"
		}
		t.Run(name, func(t *testing.T) {
			router, manager := testRouter(t, &ordersStub{create: func(ctx context.Context, req *orderspb.CreateOrderRequest) (*orderspb.CreateOrderResponse, error) {
				checkContext(t, ctx)
				if req.BuyerId != buyerID || req.ListingId != listingID || req.Qty != 3 || req.IdempotencyKey != "unique-key" {
					t.Errorf("request: %v", req)
				}
				if replay {
					if err := grpc.SetHeader(ctx, metadata.Pairs("x-idempotent-replay", "true")); err != nil {
						t.Error(err)
					}
				}
				return &orderspb.CreateOrderResponse{Order: sampleOrder()}, nil
			}})
			result := doRequest(router, "POST", "/orders", `{"listing_id":"`+listingID+`","qty":3,"buyer_id":"forged"}`, token(t, manager, "buyer"), "unique-key")
			wantCode := 201
			if replay {
				wantCode = 200
			}
			if result.Code != wantCode {
				t.Fatalf("status %d, body %s", result.Code, result.Body.String())
			}
			if (result.Header().Get("Idempotent-Replayed") == "true") != replay {
				t.Error("wrong replay header")
			}
			var order Order
			if err := json.Unmarshal(result.Body.Bytes(), &order); err != nil {
				t.Fatal(err)
			}
			if order.ID != orderID || order.BuyerID != buyerID || order.ListingID != listingID || order.Qty != 3 || order.CreatedAt != "2025-05-15T10:00:00Z" {
				t.Errorf("order: %+v", order)
			}
		})
	}
}

func TestListOrders(t *testing.T) {
	for _, test := range []struct {
		name, query   string
		limit, offset int32
		empty         bool
	}{
		{"defaults", "", 20, 0, true}, {"custom pagination", "?limit=5&offset=2", 5, 2, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			router, manager := testRouter(t, &ordersStub{list: func(ctx context.Context, req *orderspb.ListOrdersRequest) (*orderspb.ListOrdersResponse, error) {
				checkContext(t, ctx)
				if req.BuyerId != buyerID || req.GetLimit() != test.limit || req.GetOffset() != test.offset {
					t.Errorf("request: %v", req)
				}
				if test.empty {
					return &orderspb.ListOrdersResponse{}, nil
				}
				return &orderspb.ListOrdersResponse{Orders: []*orderspb.Order{sampleOrder()}, Total: 42}, nil
			}})
			result := doRequest(router, "GET", "/orders"+test.query, "", token(t, manager, "buyer"), "")
			if result.Code != 200 {
				t.Fatalf("status: %d, body %s", result.Code, result.Body.String())
			}
			count := "42"
			if test.empty {
				count = "0"
			}
			if result.Header().Get("X-Total-Count") != count {
				t.Error("wrong total count")
			}
			if test.empty && result.Body.String() != "[]" {
				t.Errorf("empty list: %s", result.Body.String())
			}
			var orders []Order
			if err := json.Unmarshal(result.Body.Bytes(), &orders); err != nil {
				t.Fatal(err)
			}
			if !test.empty && (len(orders) != 1 || orders[0].BuyerID != buyerID) {
				t.Errorf("orders: %+v", orders)
			}
		})
	}
}

func TestOrderValidationAndAuth(t *testing.T) {
	router, manager := testRouter(t, &ordersStub{})
	buyer := token(t, manager, "buyer")
	seller := token(t, manager, "seller")
	refresh, err := manager.GenerateRefreshToken(buyerID, "buyer")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, method, path, body, token, key string
		code, fields                         int
	}{
		{"missing token", "POST", "/orders", "invalid", "", "", 401, 0},
		{"seller before body", "POST", "/orders", "invalid", seller, "", 403, 0},
		{"refresh token", "GET", "/orders", "", refresh, "", 401, 0},
		{"private list", "GET", "/orders", "", "", "", 401, 0},
		{"invalid fields", "POST", "/orders", `{"listing_id":"bad","qty":0}`, buyer, "", 400, 2},
		{"missing fields", "POST", "/orders", `{}`, buyer, "", 400, 2},
		{"negative quantity", "POST", "/orders", `{"listing_id":"` + listingID + `","qty":-1}`, buyer, "", 400, 1},
		{"overflow quantity", "POST", "/orders", `{"listing_id":"` + listingID + `","qty":2147483648}`, buyer, "", 400, 1},
		{"long key", "POST", "/orders", `{"listing_id":"` + listingID + `","qty":1}`, buyer, strings.Repeat("x", 201), 400, 1},
		{"null body", "POST", "/orders", `null`, buyer, "", 400, 1},
		{"trailing JSON", "POST", "/orders", `{} {}`, buyer, "", 400, 1},
		{"bad pagination", "GET", "/orders?limit=101&offset=-1", "", buyer, "", 400, 2},
		{"overflow offset", "GET", "/orders?offset=2147483648", "", buyer, "", 400, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := doRequest(router, test.method, test.path, test.body, test.token, test.key)
			if result.Code != test.code {
				t.Fatalf("status %d, body %s", result.Code, result.Body.String())
			}
			var body response.ErrorBody
			if err := json.Unmarshal(result.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error == "" || len(body.Fields) != test.fields {
				t.Errorf("error: %+v", body)
			}
		})
	}
}

func TestOrderGRPCErrors(t *testing.T) {
	for _, test := range []struct {
		code codes.Code
		http int
		name string
	}{
		{codes.NotFound, 404, "not_found"}, {codes.FailedPrecondition, 409, "conflict"},
		{codes.Unavailable, 503, "unavailable"}, {codes.DeadlineExceeded, 503, "unavailable"},
		{codes.InvalidArgument, 400, "validation_failed"}, {codes.Internal, 500, "internal"},
	} {
		t.Run(test.code.String(), func(t *testing.T) {
			router, manager := testRouter(t, &ordersStub{create: func(context.Context, *orderspb.CreateOrderRequest) (*orderspb.CreateOrderResponse, error) {
				return nil, status.Error(test.code, "RPC failed")
			}})
			result := doRequest(router, "POST", "/orders", `{"listing_id":"`+listingID+`","qty":1}`, token(t, manager, "buyer"), "")
			if result.Code != test.http {
				t.Fatalf("status %d, body %s", result.Code, result.Body.String())
			}
			var body response.ErrorBody
			if err := json.Unmarshal(result.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error != test.name {
				t.Errorf("error: %+v", body)
			}
		})
	}
}
