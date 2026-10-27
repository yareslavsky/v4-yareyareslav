package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"marketplace/orders-svc/internal/orders/model"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/segmentio/kafka-go"
)

var ErrIdempotencyKeyNotReserved = errors.New("idempotency key is not reserved")

type MessageWriter interface {
	WriteMessages(context.Context, ...kafka.Message) error
}

type Pool interface {
	Begin(context.Context) (pgx.Tx, error)
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type PGRepository struct {
	pool Pool
	mw   MessageWriter
}

func NewPGRepository(pool Pool, mw MessageWriter) *PGRepository {
	return &PGRepository{
		pool: pool,
		mw:   mw,
	}
}

func (r *PGRepository) GetIdempotencyKeyState(
	ctx context.Context,
	buyerID, key string,
) (*model.IdempotencyKeyState, error) {
	var state model.IdempotencyKeyState
	err := r.pool.QueryRow(
		ctx,
		`
		SELECT order_id::text
		FROM idempotency_keys
		WHERE buyer_id = $1 AND key = $2
		`,
		buyerID,
		key,
	).Scan(&state.OrderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get idempotency key state: %w", err)
	}
	return &state, nil
}

func (r *PGRepository) Create(
	ctx context.Context,
	order model.Order,
	idempotencyKey string,
) (*model.Order, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create order transaction: %w", err)
	}
	defer rollbackTransaction(ctx, tx)

	if idempotencyKey != "" {
		var orderID *string
		err := tx.QueryRow(ctx,
			`
			SELECT order_id::text
			FROM idempotency_keys
			WHERE buyer_id = $1 AND key = $2
			FOR UPDATE
			`,
			order.BuyerID,
			idempotencyKey,
		).Scan(&orderID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrIdempotencyKeyNotReserved
		}
		if err != nil {
			return nil, fmt.Errorf("lock idempotency key: %w", err)
		}
		if orderID != nil {
			var existing model.Order
			var createdAt time.Time
			err := tx.QueryRow(ctx,
				`
				SELECT id::text, buyer_id::text, listing_id::text, qty, created_at
				FROM orders
				WHERE id = $1 AND buyer_id = $2
				`,
				*orderID,
				order.BuyerID,
			).Scan(
				&existing.ID, &existing.BuyerID, &existing.ListingID, &existing.Qty, &createdAt,
			)
			if err != nil {
				return nil, fmt.Errorf("get idempotent order: %w", err)
			}
			existing.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
			return &existing, nil
		}
	}

	created := model.Order{
		BuyerID:   order.BuyerID,
		ListingID: order.ListingID,
		Qty:       order.Qty,
	}

	var createdAt time.Time
	err = tx.QueryRow(
		ctx,
		`
		INSERT INTO orders (id, buyer_id, listing_id, qty)
		VALUES (gen_random_uuid(), $1, $2, $3)
		RETURNING id::text, created_at
		`,
		created.BuyerID,
		created.ListingID,
		created.Qty,
	).Scan(&created.ID, &createdAt)
	if err != nil {
		return nil, fmt.Errorf("insert order: %w", err)
	}
	created.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)

	var eventID string
	if err := tx.QueryRow(
		ctx,
		"SELECT gen_random_uuid()::text",
	).Scan(&eventID); err != nil {
		return nil, fmt.Errorf("generate order event ID: %w", err)
	}

	payload, err := json.Marshal(model.CreateOrderEvent{
		EventID:   eventID,
		OrderID:   created.ID,
		BuyerID:   created.BuyerID,
		ListingID: created.ListingID,
		Qty:       created.Qty,
		CreatedAt: created.CreatedAt,
	})
	if err != nil {
		return nil, fmt.Errorf("encode order event: %w", err)
	}

	_, err = tx.Exec(
		ctx,
		`
		INSERT INTO outbox (event_id, topic, key, payload)
		VALUES ($1, 'order.created', $2, $3)
		`,
		eventID,
		created.ID,
		payload,
	)
	if err != nil {
		return nil, fmt.Errorf("insert order outbox event: %w", err)
	}

	if idempotencyKey != "" {
		_, err := tx.Exec(ctx, `
			UPDATE idempotency_keys
			SET order_id = $3
			WHERE buyer_id = $1 AND key = $2
		`, created.BuyerID, idempotencyKey, created.ID)
		if err != nil {
			return nil, fmt.Errorf("link idempotency key to order: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create order transaction: %w", err)
	}
	return &created, nil
}

func (r *PGRepository) List(
	ctx context.Context,
	buyerID string,
	limit,
	offset int,
) ([]model.Order, int64, error) {
	rows, err := r.pool.Query(
		ctx,
		`
		SELECT id, buyer_id, listing_id, qty, created_at
		FROM orders
		WHERE buyer_id = $1
		ORDER BY created_at DESC, id DESC
		LIMIT $2
		OFFSET $3
		`,
		buyerID,
		limit,
		offset,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("get orders: %w", err)
	}
	defer rows.Close()

	var orders []model.Order

	for rows.Next() {
		var createdAt time.Time
		var order model.Order
		if err := rows.Scan(
			&order.ID,
			&order.BuyerID,
			&order.ListingID,
			&order.Qty,
			&createdAt,
		); err != nil {
			return nil, 0, fmt.Errorf("orders rows scan: %w", err)
		}
		order.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
		orders = append(orders, order)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("orders rows error: %w", err)
	}

	var totalCount int64
	if err := r.pool.QueryRow(
		ctx,
		`
		SELECT COUNT(id) 
		FROM orders
		WHERE buyer_id = $1
		`,
		buyerID,
	).Scan(&totalCount); err != nil {
		return nil, 0, fmt.Errorf("total orders count row scan: %w", err)
	}

	return orders, totalCount, nil
}

func (r *PGRepository) GetByID(
	ctx context.Context,
	orderID,
	buyerID string,
) (*model.Order, error) {
	var order model.Order
	var createdAt time.Time
	if err := r.pool.QueryRow(
		ctx,
		`
		SELECT id, buyer_id, listing_id, qty, created_at
		FROM orders
		WHERE id = $1 AND buyer_id = $2
		`,
		orderID,
		buyerID,
	).Scan(
		&order.ID,
		&order.BuyerID,
		&order.ListingID,
		&order.Qty,
		&createdAt,
	); err != nil {
		return nil, fmt.Errorf("get order by id: %w", err)
	}
	order.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)

	return &order, nil
}

func (r *PGRepository) ReserveIdempotencyKey(
	ctx context.Context,
	buyerID, key string,
) (bool, error) {
	res, err := r.pool.Exec(
		ctx,
		`
		INSERT INTO idempotency_keys (buyer_id, key)
		VALUES ($1, $2)
		ON CONFLICT (buyer_id, key) DO NOTHING
		`,
		buyerID, key,
	)
	if err != nil {
		return false, fmt.Errorf("acquiring idempotency key: %w", err)
	}

	n := res.RowsAffected()
	return n == 1, nil
}

func (r *PGRepository) ReleaseIdempotencyKey(
	ctx context.Context,
	buyerID, key string,
) (bool, error) {
	res, err := r.pool.Exec(
		ctx,
		`
		DELETE FROM idempotency_keys
		WHERE buyer_id = $1
			AND key = $2
			AND order_id is NULL
		`,
		buyerID, key,
	)
	if err != nil {
		return false, fmt.Errorf("releasing idempotency key: %w", err)
	}

	n := res.RowsAffected()
	return n == 1, nil
}

func (r *PGRepository) SendUnpublishedEvents(
	ctx context.Context,
	limit int,
) ([]model.OutboxEvent, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("start get unpublished transaction: %w", err)
	}
	defer rollbackTransaction(ctx, tx)

	rows, err := tx.Query(
		ctx,
		`
		SELECT 
			id, 
			topic, 
			key, 
			payload
		FROM outbox
		WHERE published_at IS NULL
		ORDER BY id
		LIMIT $1
		FOR UPDATE SKIP LOCKED
		`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("get orders: %w", err)
	}
	defer rows.Close()

	var events []model.OutboxEvent
	var IDs []int64
	for rows.Next() {
		var event model.OutboxEvent
		if err := rows.Scan(
			&event.ID,
			&event.Topic,
			&event.Key,
			&event.Payload,
		); err != nil {
			return nil, fmt.Errorf("orders rows scan: %w", err)
		}
		IDs = append(IDs, event.ID)
		events = append(events, event)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read outbox events: %w", err)
	}

	if len(events) == 0 {
		return events, nil
	}

	messages := make([]kafka.Message, 0, len(events))
	for _, event := range events {
		messages = append(messages, kafka.Message{
			Topic: event.Topic,
			Key:   []byte(event.Key),
			Value: event.Payload,
		})
	}
	sendCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := r.mw.WriteMessages(sendCtx, messages...); err != nil {
		return nil, fmt.Errorf("publish outbox events: %w", err)
	}

	res, err := tx.Exec(
		ctx,
		`
		UPDATE outbox
		SET published_at = NOW()
		WHERE id = ANY($1)
		`,
		IDs,
	)
	if err != nil {
		return nil, fmt.Errorf("update unpublished events: %w", err)
	}

	if n := res.RowsAffected(); n != int64(len(events)) {
		return nil, fmt.Errorf("updated %d outbox events, expected %d", res.RowsAffected(), len(events))
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit updated events: %w", err)
	}

	return events, nil

}
