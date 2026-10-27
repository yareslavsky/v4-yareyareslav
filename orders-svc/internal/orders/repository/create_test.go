package repository

import (
	"context"
	"encoding/json"
	"errors"
	"marketplace/orders-svc/internal/orders/model"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v5"
)

type orderEventMatcher struct {
	expected model.CreateOrderEvent
}

func (m orderEventMatcher) Match(value any) bool {
	payload, ok := value.([]byte)
	if !ok {
		return false
	}
	var event model.CreateOrderEvent
	return json.Unmarshal(payload, &event) == nil && event == m.expected
}

func createTestOrder() model.Order {
	return model.Order{BuyerID: ValidBuyerID, ListingID: ValidListingID, Qty: 5}
}

func expectedCreatedOrder() model.Order {
	order := createTestOrder()
	order.ID = createdOrderID
	order.CreatedAt = orderCreatedAt.UTC().Format(time.RFC3339Nano)
	return order
}

func expectCreate(pool pgxmock.PgxPoolIface, key, failedStep string, failure error) {
	begin := pool.ExpectBegin()
	if failedStep == "begin" {
		begin.WillReturnError(failure)
		return
	}
	defer pool.ExpectRollback()

	if key != "" {
		lock := pool.ExpectQuery("SELECT order_id::text FROM idempotency_keys").
			WithArgs(ValidBuyerID, key)
		if failedStep == "lock" {
			lock.WillReturnError(failure)
			return
		}
		lock.WillReturnRows(pgxmock.NewRows([]string{"order_id"}).AddRow(nil))
	}

	insert := pool.ExpectQuery("INSERT INTO orders").
		WithArgs(ValidBuyerID, ValidListingID, 5)
	if failedStep == "insert" {
		insert.WillReturnError(failure)
		return
	}
	insert.WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).
		AddRow(createdOrderID, orderCreatedAt))

	eventID := pool.ExpectQuery("SELECT gen_random_uuid\\(\\)::text")
	if failedStep == "event ID" {
		eventID.WillReturnError(failure)
		return
	}
	eventID.WillReturnRows(pgxmock.NewRows([]string{"event_id"}).AddRow(createdEventID))

	outbox := pool.ExpectExec("INSERT INTO outbox \\(event_id, topic, key, payload\\) VALUES \\(\\$1, 'order.created', \\$2, \\$3\\)").
		WithArgs(createdEventID, createdOrderID, orderEventMatcher{expected: model.CreateOrderEvent{
			EventID: createdEventID, OrderID: createdOrderID,
			BuyerID: ValidBuyerID, ListingID: ValidListingID, Qty: 5,
			CreatedAt: expectedCreatedOrder().CreatedAt,
		}})
	if failedStep == "outbox" {
		outbox.WillReturnError(failure)
		return
	}
	outbox.WillReturnResult(pgxmock.NewResult("INSERT", 1))

	if key != "" {
		link := pool.ExpectExec("UPDATE idempotency_keys SET order_id = \\$3 WHERE buyer_id = \\$1 AND key = \\$2").
			WithArgs(ValidBuyerID, key, createdOrderID)
		if failedStep == "link" {
			link.WillReturnError(failure)
			return
		}
		link.WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	}

	commit := pool.ExpectCommit()
	if failedStep == "commit" {
		commit.WillReturnError(failure)
	}
}

func TestCreate_Success(t *testing.T) {
	for _, key := range []string{"", "order-1"} {
		name := "without key"
		if key != "" {
			name = "with reserved key"
		}
		t.Run(name, func(t *testing.T) {
			pool := newTestPool(t, pgxmock.QueryMatcherRegexp)
			expectCreate(pool, key, "", nil)
			repo := NewPGRepository(pool, nil)

			created, err := repo.Create(context.Background(), createTestOrder(), key)
			if err != nil {
				t.Fatal(err)
			}
			if created == nil || *created != expectedCreatedOrder() {
				t.Fatalf("created = %+v, want %+v", created, expectedCreatedOrder())
			}
		})
	}
}

func TestCreate_Failures(t *testing.T) {
	for _, step := range []string{"begin", "lock", "insert", "event ID", "outbox", "link", "commit"} {
		t.Run(step, func(t *testing.T) {
			pool := newTestPool(t, pgxmock.QueryMatcherRegexp)
			failure := errors.New("database failure")
			expectCreate(pool, "order-1", step, failure)
			repo := NewPGRepository(pool, nil)

			created, err := repo.Create(context.Background(), createTestOrder(), "order-1")
			if !errors.Is(err, failure) {
				t.Fatalf("error = %v, want wrapped database failure", err)
			}
			if created != nil {
				t.Fatalf("created = %+v, want nil", created)
			}
		})
	}
}

func TestCreate_UnreservedKey(t *testing.T) {
	pool := newTestPool(t, pgxmock.QueryMatcherRegexp)
	expectCreate(pool, "order-1", "lock", pgx.ErrNoRows)
	repo := NewPGRepository(pool, nil)

	created, err := repo.Create(context.Background(), createTestOrder(), "order-1")
	if !errors.Is(err, ErrIdempotencyKeyNotReserved) {
		t.Fatalf("error = %v, want ErrIdempotencyKeyNotReserved", err)
	}
	if created != nil {
		t.Fatalf("created = %+v, want nil", created)
	}
}

func TestCreate_ExistingOrder(t *testing.T) {
	for _, failLookup := range []bool{false, true} {
		name := "returns original order"
		if failLookup {
			name = "lookup failure"
		}
		t.Run(name, func(t *testing.T) {
			pool := newTestPool(t, pgxmock.QueryMatcherRegexp)
			pool.ExpectBegin()
			existingID := createdOrderID
			pool.ExpectQuery("SELECT order_id::text FROM idempotency_keys").
				WithArgs(ValidBuyerID, "order-1").
				WillReturnRows(pgxmock.NewRows([]string{"order_id"}).AddRow(&existingID))
			lookup := pool.ExpectQuery("SELECT id::text, buyer_id::text, listing_id::text, qty, created_at FROM orders").
				WithArgs(createdOrderID, ValidBuyerID)
			failure := errors.New("lookup failed")
			if failLookup {
				lookup.WillReturnError(failure)
			} else {
				lookup.WillReturnRows(pgxmock.NewRows([]string{"id", "buyer_id", "listing_id", "qty", "created_at"}).
					AddRow(createdOrderID, ValidBuyerID, ValidListingID, 5, orderCreatedAt))
			}
			pool.ExpectRollback()
			repo := NewPGRepository(pool, nil)
			request := createTestOrder()
			request.Qty = 10

			created, err := repo.Create(context.Background(), request, "order-1")
			if failLookup {
				if !errors.Is(err, failure) || created != nil {
					t.Fatalf("created = %+v, error = %v, want nil and lookup failure", created, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if created == nil || *created != expectedCreatedOrder() {
				t.Fatalf("created = %+v, want original %+v", created, expectedCreatedOrder())
			}
		})
	}
}
