package catalog

import (
	"context"
	"marketplace/gen/catalogpb"

	"google.golang.org/grpc/metadata"
)

type Client struct {
	client catalogpb.CatalogServiceClient
}

func NewClient(client catalogpb.CatalogServiceClient) *Client {
	return &Client{client: client}
}

func (c *Client) Reserve(ctx context.Context, listingID string, qty int) error {
	return c.ReserveReservation(ctx, listingID, qty, "")
}

func (c *Client) ReserveReservation(ctx context.Context, listingID string, qty int, reservationID string) error {
	ctx = forwardRequestID(ctx)
	_, err := c.client.Reserve(ctx, &catalogpb.ReserveCatalogRequest{
		ReservationId: reservationID,
		ListingId:     listingID,
		Qty:           int64(qty),
	})
	return err
}

func forwardRequestID(ctx context.Context) context.Context {
	if incoming, ok := metadata.FromIncomingContext(ctx); ok {
		if values := incoming.Get("x-request-id"); len(values) > 0 {
			outgoing, _ := metadata.FromOutgoingContext(ctx)
			outgoing = outgoing.Copy()
			outgoing.Set("x-request-id", values[0])
			ctx = metadata.NewOutgoingContext(ctx, outgoing)
		}
	}
	return ctx
}

func (c *Client) ReleaseReservation(ctx context.Context, id string) error {
	_, err := c.client.ReleaseReservation(forwardRequestID(ctx), &catalogpb.ReleaseReservationRequest{ReservationId: id})
	return err
}
