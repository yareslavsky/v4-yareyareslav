package repository

import (
	"context"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v5"
	"github.com/segmentio/kafka-go"
)

const ValidBuyerID = "550e8400-e29b-41d4-a716-446655440000"
const ValidListingID = "665e8411-e29b-41d4-a716-446655440000"
const createdOrderID = "550e8400-e29b-41d4-a716-446655440001"
const createdEventID = "550e8400-e29b-41d4-a716-446655440002"

var orderCreatedAt = time.Date(2026, 10, 9, 15, 30, 0, 123456000, time.FixedZone("MSK", 3*60*60))

func newTestPool(t *testing.T, matcher pgxmock.QueryMatcher) pgxmock.PgxPoolIface {
	t.Helper()
	pool, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(matcher))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		defer pool.Close()
		if err := pool.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})
	return pool
}

func newOrderRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{"id", "buyer_id", "listing_id", "qty", "created_at"})
}

type messageWriterFunc func(context.Context, ...kafka.Message) error

func (f messageWriterFunc) WriteMessages(ctx context.Context, messages ...kafka.Message) error {
	return f(ctx, messages...)
}
