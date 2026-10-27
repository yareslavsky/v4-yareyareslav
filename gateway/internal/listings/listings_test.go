package listings

import (
	"context"
	"encoding/json"
	"marketplace/gateway/config"
	"marketplace/gateway/pkg/jwt"
	"marketplace/gateway/pkg/requestid"
	"marketplace/gen/catalogpb"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const testListingID = "11111111-1111-4111-8111-111111111111"
const testSellerID = "22222222-2222-4222-8222-222222222222"

type catalogStub struct {
	catalogpb.UnimplementedCatalogServiceServer
	create func(context.Context, *catalogpb.CreateCatalogRequest) (*catalogpb.CreateCatalogResponse, error)
	get    func(context.Context, *catalogpb.GetCatalogRequest) (*catalogpb.GetCatalogResponse, error)
	list   func(context.Context, *catalogpb.ListCatalogRequest) (*catalogpb.ListCatalogResponse, error)
	update func(context.Context, *catalogpb.UpdateCatalogRequest) (*catalogpb.UpdateCatalogResponse, error)
	delete func(context.Context, *catalogpb.DeleteCatalogRequest) (*emptypb.Empty, error)
}

func (s *catalogStub) Create(ctx context.Context, req *catalogpb.CreateCatalogRequest) (*catalogpb.CreateCatalogResponse, error) {
	return s.create(ctx, req)
}
func (s *catalogStub) Get(ctx context.Context, req *catalogpb.GetCatalogRequest) (*catalogpb.GetCatalogResponse, error) {
	return s.get(ctx, req)
}
func (s *catalogStub) List(ctx context.Context, req *catalogpb.ListCatalogRequest) (*catalogpb.ListCatalogResponse, error) {
	return s.list(ctx, req)
}
func (s *catalogStub) Update(ctx context.Context, req *catalogpb.UpdateCatalogRequest) (*catalogpb.UpdateCatalogResponse, error) {
	return s.update(ctx, req)
}
func (s *catalogStub) Delete(ctx context.Context, req *catalogpb.DeleteCatalogRequest) (*emptypb.Empty, error) {
	return s.delete(ctx, req)
}

func testRouter(t *testing.T, stub *catalogStub) (http.Handler, *jwt.Manager) {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	catalogpb.RegisterCatalogServiceServer(server, stub)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	conn, err := grpc.NewClient("passthrough:///catalog-test", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	manager := jwt.NewManager("test-secret", 5*time.Minute, 7*24*time.Hour)
	router := chi.NewRouter()
	router.Use(requestid.Middleware)
	NewListingsHandler(router, nil, config.NewAuthConfig(jwt.NewJWTMiddleware(manager)), NewListingsService(catalogpb.NewCatalogServiceClient(conn)))
	return router, manager
}
func doRequest(router http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("X-Request-ID", "test-request")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}
func accessToken(t *testing.T, manager *jwt.Manager, role string) string {
	t.Helper()
	token, err := manager.GenerateAccessToken(testSellerID, role)
	if err != nil {
		t.Fatal(err)
	}
	return token
}
func sampleListing() *catalogpb.Listing {
	return &catalogpb.Listing{Id: testListingID, SellerId: testSellerID, Title: "Milk", Price: 2919, Stock: 0, Sold: 3, CreatedAt: timestamppb.New(time.Date(2025, 5, 15, 10, 0, 0, 0, time.UTC))}
}
func checkRPCContext(t *testing.T, ctx context.Context) {
	t.Helper()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 3*time.Second {
		t.Error("RPC must have a three-second deadline")
	}
	md, _ := metadata.FromIncomingContext(ctx)
	if got := md.Get("x-request-id"); len(got) != 1 || got[0] != "test-request" {
		t.Errorf("request ID: %v", got)
	}
}

func TestListingsHTTPToGRPC(t *testing.T) {
	stub := &catalogStub{
		create: func(ctx context.Context, req *catalogpb.CreateCatalogRequest) (*catalogpb.CreateCatalogResponse, error) {
			checkRPCContext(t, ctx)
			if req.SellerId != testSellerID || req.Title != "Milk" || req.Price != 2919 || req.Stock == nil || *req.Stock != 0 {
				t.Errorf("create request: %v", req)
			}
			return &catalogpb.CreateCatalogResponse{Listing: sampleListing()}, nil
		},
		get: func(ctx context.Context, req *catalogpb.GetCatalogRequest) (*catalogpb.GetCatalogResponse, error) {
			checkRPCContext(t, ctx)
			if req.Id != testListingID {
				t.Errorf("get ID: %s", req.Id)
			}
			if err := grpc.SetHeader(ctx, metadata.Pairs("x-cache", "HIT")); err != nil {
				t.Error(err)
			}
			return &catalogpb.GetCatalogResponse{Listing: sampleListing()}, nil
		},
		list: func(ctx context.Context, req *catalogpb.ListCatalogRequest) (*catalogpb.ListCatalogResponse, error) {
			checkRPCContext(t, ctx)
			if req.GetLimit() != 5 || req.GetOffset() != 2 {
				t.Errorf("pagination: %v", req)
			}
			return &catalogpb.ListCatalogResponse{Items: []*catalogpb.Listing{sampleListing()}, Total: 42}, nil
		},
		update: func(ctx context.Context, req *catalogpb.UpdateCatalogRequest) (*catalogpb.UpdateCatalogResponse, error) {
			checkRPCContext(t, ctx)
			if req.Id != testListingID || req.SellerId != testSellerID || req.Price == nil || *req.Price != 1 || req.Stock != nil || req.Title != nil {
				t.Errorf("partial update: %v", req)
			}
			listing := sampleListing()
			listing.Price = 1
			return &catalogpb.UpdateCatalogResponse{Listing: listing}, nil
		},
		delete: func(ctx context.Context, req *catalogpb.DeleteCatalogRequest) (*emptypb.Empty, error) {
			checkRPCContext(t, ctx)
			if req.Id != testListingID || req.SellerId != testSellerID {
				t.Errorf("delete request: %v", req)
			}
			return &emptypb.Empty{}, nil
		},
	}
	router, manager := testRouter(t, stub)
	token := accessToken(t, manager, "seller")
	for _, test := range []struct {
		method, path, body string
		code               int
	}{
		{"POST", "/listings", `{"title":"Milk","price":29.19,"stock":0,"seller_id":"forged"}`, 201},
		{"GET", "/listings/" + testListingID, "", 200},
		{"GET", "/listings?limit=5&offset=2", "", 200},
		{"PATCH", "/listings/" + testListingID, `{"price":0.01}`, 200},
		{"DELETE", "/listings/" + testListingID, "", 204},
	} {
		t.Run(test.method+test.path, func(t *testing.T) {
			result := doRequest(router, test.method, test.path, test.body, token)
			if result.Code != test.code {
				t.Fatalf("status %d, body %s", result.Code, result.Body.String())
			}
			if test.method == "DELETE" {
				if result.Body.Len() != 0 {
					t.Error("204 must have no body")
				}
				return
			}
			if !strings.HasPrefix(result.Header().Get("Content-Type"), "application/json") {
				t.Error("expected JSON")
			}
			if test.method == "GET" && strings.Contains(test.path, "?") {
				if result.Header().Get("X-Total-Count") != "42" {
					t.Error("total count missing")
				}
			} else {
				var item Listing
				if err := json.Unmarshal(result.Body.Bytes(), &item); err != nil {
					t.Fatal(err)
				}
				wantPrice := "29.19"
				if test.method == "PATCH" {
					wantPrice = "0.01"
				}
				if item.ID != testListingID || item.SellerID != testSellerID || item.Price.String() != wantPrice || item.CreatedAt != "2025-05-15T10:00:00Z" {
					t.Errorf("listing: %+v", item)
				}
				if test.method == "GET" && result.Header().Get("X-Cache") != "HIT" {
					t.Error("cache header missing")
				}
			}
		})
	}
}

func TestListingsValidationAndAuth(t *testing.T) {
	router, manager := testRouter(t, &catalogStub{})
	seller := accessToken(t, manager, "seller")
	buyer := accessToken(t, manager, "buyer")
	refresh, err := manager.GenerateRefreshToken(testSellerID, "seller")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, method, path, body, token string
		code, fields                    int
	}{
		{"missing token before body", "POST", "/listings", "invalid", "", 401, 0},
		{"wrong role before body", "POST", "/listings", "invalid", buyer, 403, 0},
		{"refresh token", "POST", "/listings", "{}", refresh, 401, 0},
		{"bad token", "POST", "/listings", "{}", "invalid", 401, 0},
		{"all invalid fields", "POST", "/listings", `{"title":"","price":-1,"stock":-1}`, seller, 400, 3},
		{"all missing fields", "POST", "/listings", `{}`, seller, 400, 3},
		{"missing stock", "POST", "/listings", `{"title":"Milk","price":1}`, seller, 400, 1},
		{"fractional minor units", "PATCH", "/listings/" + testListingID, `{"price":0.001}`, seller, 400, 1},
		{"overflow", "PATCH", "/listings/" + testListingID, `{"price":92233720368547758.08}`, seller, 400, 1},
		{"huge exponent", "PATCH", "/listings/" + testListingID, `{"price":1e999999999}`, seller, 400, 1},
		{"quoted price", "PATCH", "/listings/" + testListingID, `{"price":"1"}`, seller, 400, 1},
		{"invalid JSON", "POST", "/listings", `{`, seller, 400, 1},
		{"null", "POST", "/listings", `null`, seller, 400, 1},
		{"trailing body", "POST", "/listings", `{} {}`, seller, 400, 1},
		{"bad uuid", "GET", "/listings/123", "", "", 404, 0},
		{"invalid pagination", "GET", "/listings?limit=0&offset=-1", "", "", 400, 2},
		{"empty limit", "GET", "/listings?limit=", "", "", 400, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := doRequest(router, test.method, test.path, test.body, test.token)
			if result.Code != test.code {
				t.Fatalf("status %d, body %s", result.Code, result.Body.String())
			}
			var body errorBody
			if err := json.Unmarshal(result.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if len(body.Fields) != test.fields || body.Error == "" {
				t.Errorf("error response: %+v", body)
			}
		})
	}
}

func TestListingsGRPCErrors(t *testing.T) {
	for _, test := range []struct {
		code codes.Code
		http int
		name string
	}{
		{codes.NotFound, 404, "not_found"}, {codes.InvalidArgument, 400, "validation_failed"},
		{codes.PermissionDenied, 403, "forbidden"}, {codes.FailedPrecondition, 409, "conflict"},
		{codes.AlreadyExists, 409, "conflict"}, {codes.Aborted, 409, "conflict"},
		{codes.Unavailable, 503, "unavailable"}, {codes.DeadlineExceeded, 503, "unavailable"},
		{codes.Internal, 500, "internal"},
	} {
		t.Run(test.code.String(), func(t *testing.T) {
			rpcErr := status.New(test.code, "backend error")
			if test.code == codes.InvalidArgument {
				var err error
				rpcErr, err = rpcErr.WithDetails(&errdetails.BadRequest{FieldViolations: []*errdetails.BadRequest_FieldViolation{{Field: "title", Description: "invalid title"}}})
				if err != nil {
					t.Fatal(err)
				}
			}
			router, _ := testRouter(t, &catalogStub{get: func(context.Context, *catalogpb.GetCatalogRequest) (*catalogpb.GetCatalogResponse, error) {
				return nil, rpcErr.Err()
			}})
			result := doRequest(router, "GET", "/listings/"+testListingID, "", "")
			if result.Code != test.http {
				t.Fatalf("status: %d", result.Code)
			}
			var body errorBody
			if err := json.Unmarshal(result.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error != test.name {
				t.Errorf("error: %+v", body)
			}
			if test.code == codes.InvalidArgument && (len(body.Fields) != 1 || body.Fields[0].Field != "title") {
				t.Errorf("validation details: %+v", body)
			}
		})
	}
}

func TestPriceExactConversion(t *testing.T) {
	for _, test := range []struct {
		input string
		minor int64
	}{{"0.29", 29}, {"29.19", 2919}, {"1e2", 10000}, {"92233720368547758.07", 9223372036854775807}} {
		t.Run(test.input, func(t *testing.T) {
			request, ok := readBody(httptest.NewRecorder(), httptest.NewRequest("PATCH", "/", strings.NewReader(`{"price":`+test.input+`}`)))
			if !ok {
				t.Fatal("could not decode price")
			}
			minor, fields := request.validate(false)
			if len(fields) != 0 || minor == nil || *minor != test.minor {
				t.Fatalf("minor %v fields %v", minor, fields)
			}
			result := toListing(&catalogpb.Listing{Price: *minor})
			data, err := json.Marshal(struct {
				Price json.Number `json:"price"`
			}{result.Price})
			if err != nil {
				t.Fatal(err)
			}
			var roundtrip listingRequest
			if err := json.Unmarshal(data, &roundtrip); err != nil {
				t.Fatal(err)
			}
			got, fields := roundtrip.validate(false)
			if len(fields) != 0 || *got != test.minor {
				t.Fatal("price did not roundtrip")
			}
		})
	}
}

func TestGeneratedRequestIDReachesCatalog(t *testing.T) {
	received := make(chan string, 1)
	router, _ := testRouter(t, &catalogStub{get: func(ctx context.Context, req *catalogpb.GetCatalogRequest) (*catalogpb.GetCatalogResponse, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		values := md.Get("x-request-id")
		if len(values) != 1 {
			t.Errorf("request ID metadata: %v", values)
			received <- ""
		} else {
			received <- values[0]
		}
		return &catalogpb.GetCatalogResponse{Listing: sampleListing()}, nil
	}})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest("GET", "/listings/"+testListingID, nil))
	if recorder.Code != 200 {
		t.Fatalf("status %d, body %s", recorder.Code, recorder.Body.String())
	}
	id := recorder.Header().Get("X-Request-ID")
	if id == "" || id != <-received {
		t.Fatalf("response and gRPC IDs do not match: %q", id)
	}
}
