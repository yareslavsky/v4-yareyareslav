package catalog

import (
	"context"
	"errors"
	"marketplace/gen/catalogpb"
	"reflect"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

type catalogClientStub struct {
	catalogpb.CatalogServiceClient
	reserve func(context.Context, *catalogpb.ReserveCatalogRequest, ...grpc.CallOption) (*catalogpb.ReserveCatalogResponse, error)
	release func(context.Context, *catalogpb.ReleaseReservationRequest, ...grpc.CallOption) (*emptypb.Empty, error)
}

func (c *catalogClientStub) Reserve(ctx context.Context, req *catalogpb.ReserveCatalogRequest, opts ...grpc.CallOption) (*catalogpb.ReserveCatalogResponse, error) {
	return c.reserve(ctx, req, opts...)
}

func (c *catalogClientStub) ReleaseReservation(ctx context.Context, req *catalogpb.ReleaseReservationRequest, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	return c.release(ctx, req, opts...)
}

func TestReservationRPCs(t *testing.T) {
	failure := status.Error(codes.Unavailable, "catalog unavailable")
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("x-request-id", "request"))
	stub := &catalogClientStub{
		reserve: func(_ context.Context, req *catalogpb.ReserveCatalogRequest, _ ...grpc.CallOption) (*catalogpb.ReserveCatalogResponse, error) {
			if req.ReservationId != "reservation" || req.ListingId != "listing" || req.Qty != 3 {
				t.Fatalf("reserve: %+v", req)
			}
			return &catalogpb.ReserveCatalogResponse{}, nil
		},
		release: func(callCtx context.Context, req *catalogpb.ReleaseReservationRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
			md, _ := metadata.FromOutgoingContext(callCtx)
			if req.ReservationId != "reservation" || !reflect.DeepEqual(md.Get("x-request-id"), []string{"request"}) {
				t.Fatalf("release: %+v, metadata=%v", req, md)
			}
			return nil, failure
		},
	}
	client := NewClient(stub)
	if err := client.ReserveReservation(ctx, "listing", 3, "reservation"); err != nil {
		t.Fatal(err)
	}
	if err := client.ReleaseReservation(ctx, "reservation"); !errors.Is(err, failure) {
		t.Fatalf("release error: %v", err)
	}
}

func TestReserve_RequestAndMetadata(t *testing.T) {
	tests := []struct {
		name     string
		incoming metadata.MD
		outgoing metadata.MD
		want     metadata.MD
	}{
		{name: "no metadata"},
		{name: "request ID forwarded", incoming: metadata.Pairs("x-request-id", "request-1"), want: metadata.Pairs("x-request-id", "request-1")},
		{name: "first request ID used", incoming: metadata.Pairs("x-request-id", "first", "x-request-id", "second"), want: metadata.Pairs("x-request-id", "first")},
		{name: "unrelated incoming metadata ignored", incoming: metadata.Pairs("authorization", "incoming-token")},
		{name: "existing outgoing metadata preserved", outgoing: metadata.Pairs("authorization", "outgoing-token"), want: metadata.Pairs("authorization", "outgoing-token")},
		{
			name:     "incoming ID replaces outgoing ID without dropping other metadata",
			incoming: metadata.Pairs("x-request-id", "incoming", "other", "ignored"),
			outgoing: metadata.Pairs("x-request-id", "outgoing", "authorization", "token"),
			want:     metadata.Pairs("x-request-id", "incoming", "authorization", "token"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			deadline, _ := ctx.Deadline()
			if tt.incoming != nil {
				ctx = metadata.NewIncomingContext(ctx, tt.incoming)
			}
			if tt.outgoing != nil {
				ctx = metadata.NewOutgoingContext(ctx, tt.outgoing)
			}
			incomingBefore := tt.incoming.Copy()
			outgoingBefore := tt.outgoing.Copy()
			listingID := "665e8411-e29b-41d4-a716-446655440000"
			calls := 0
			var forwardedCtx context.Context
			stub := &catalogClientStub{reserve: func(receivedCtx context.Context, req *catalogpb.ReserveCatalogRequest, opts ...grpc.CallOption) (*catalogpb.ReserveCatalogResponse, error) {
				calls++
				forwardedCtx = receivedCtx
				if req.GetListingId() != listingID || req.GetQty() != 5 {
					t.Errorf("request = %v, want listing %q and quantity 5", req, listingID)
				}
				if len(opts) != 0 {
					t.Errorf("unexpected call options: %v", opts)
				}
				outgoing, _ := metadata.FromOutgoingContext(receivedCtx)
				if !reflect.DeepEqual(outgoing, tt.want) {
					t.Errorf("outgoing metadata = %v, want %v", outgoing, tt.want)
				}
				if got, ok := receivedCtx.Deadline(); !ok || !got.Equal(deadline) {
					t.Errorf("deadline = %v, want %v", got, deadline)
				}
				return &catalogpb.ReserveCatalogResponse{Remaining: 3}, nil
			}}

			if err := NewClient(stub).Reserve(ctx, listingID, 5); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("RPC calls = %d, want 1", calls)
			}
			if tt.incoming != nil && !reflect.DeepEqual(tt.incoming, incomingBefore) {
				t.Error("incoming metadata was mutated")
			}
			if tt.outgoing != nil && !reflect.DeepEqual(tt.outgoing, outgoingBefore) {
				t.Error("outgoing metadata was mutated")
			}
			cancel()
			if !errors.Is(forwardedCtx.Err(), context.Canceled) {
				t.Errorf("forwarded context error = %v, want context.Canceled", forwardedCtx.Err())
			}
		})
	}
}

func TestReserve_ReturnsRPCError(t *testing.T) {
	failures := []error{
		status.Error(codes.NotFound, "listing not found"),
		status.Error(codes.FailedPrecondition, "insufficient stock"),
		status.Error(codes.Unavailable, "catalog unavailable"),
		context.Canceled,
		context.DeadlineExceeded,
		errors.New("transport failure"),
	}
	for _, failure := range failures {
		t.Run(failure.Error(), func(t *testing.T) {
			calls := 0
			stub := &catalogClientStub{reserve: func(context.Context, *catalogpb.ReserveCatalogRequest, ...grpc.CallOption) (*catalogpb.ReserveCatalogResponse, error) {
				calls++
				return nil, failure
			}}
			err := NewClient(stub).Reserve(context.Background(), "665e8411-e29b-41d4-a716-446655440000", 5)
			if err != failure {
				t.Errorf("error = %v, want original error %v", err, failure)
			}
			if calls != 1 {
				t.Errorf("RPC calls = %d, want 1", calls)
			}
		})
	}
}
