package repository

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"marketplace/orders-svc/internal/orders/model"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type ReservationAction = func(context.Context, string) error

func (r *PGRepository) CreateReserved(ctx context.Context, order model.Order, key string, reserve, release ReservationAction) (*model.Order, error) {
	id := uuid.NewString()
	persistCtx, cancelPersist := context.WithTimeout(ctx, 2*time.Second)
	err := r.persistReservation(persistCtx, id, order.BuyerID, key)
	cancelPersist()
	if err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		if key != "" {
			if cleanupErr := r.cleanupFailedReservationIntent(cleanupCtx, id, order.BuyerID, key); cleanupErr != nil {
				slog.WarnContext(cleanupCtx, "release idempotency key failed", "error", cleanupErr)
			}
		}
		return nil, fmt.Errorf("persist reservation saga: %w", err)
	}
	created, err := r.createReserved(ctx, id, order, key, reserve)
	if err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 4*time.Second)
		defer cancel()
		if cleanupErr := r.reconcileReservation(cleanupCtx, id, release); cleanupErr != nil {
			slog.WarnContext(cleanupCtx, "reservation compensation pending", "reservation_id", id, "error", cleanupErr)
		}
	}
	return created, err
}

func (r *PGRepository) persistReservation(ctx context.Context, id, buyerID, key string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollbackTransaction(ctx, tx)
	if _, err = tx.Exec(ctx, `INSERT INTO reservation_sagas (id, buyer_id, idempotency_key) VALUES ($1, $2, $3)`, id, buyerID, key); err != nil {
		return err
	}
	if key != "" {
		result, err := tx.Exec(ctx, `UPDATE idempotency_keys SET reservation_id = $3 WHERE buyer_id = $1 AND key = $2 AND order_id IS NULL`, buyerID, key, id)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return ErrIdempotencyKeyNotReserved
		}
	}
	return tx.Commit(ctx)
}

func (r *PGRepository) createReserved(ctx context.Context, id string, order model.Order, key string, reserve ReservationAction) (*model.Order, error) {
	ctx, cancelOperation := context.WithTimeout(ctx, 5*time.Second)
	defer cancelOperation()
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer rollbackTransaction(ctx, tx)
	var lockedID string
	if err = tx.QueryRow(ctx, `SELECT id::text FROM reservation_sagas WHERE id = $1 AND NOT completed FOR UPDATE`, id).Scan(&lockedID); err != nil {
		return nil, err
	}
	reserveCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	err = reserve(reserveCtx, id)
	cancel()
	if err != nil {
		return nil, fmt.Errorf("reserve listing: %w", err)
	}
	createCtx, cancelCreate := context.WithTimeout(ctx, 2*time.Second)
	defer cancelCreate()
	nested := NewPGRepository(tx, r.mw)
	created, err := nested.Create(createCtx, order, key)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(createCtx, `UPDATE reservation_sagas SET completed = true WHERE id = $1`, id); err != nil {
		return nil, err
	}
	if err = tx.Commit(createCtx); err != nil {
		return nil, fmt.Errorf("commit reserved order: %w", err)
	}
	return created, nil
}

func (r *PGRepository) reconcileReservation(ctx context.Context, id string, release ReservationAction) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollbackTransaction(ctx, tx)
	var buyerID, key string
	err = tx.QueryRow(ctx, `SELECT buyer_id::text, idempotency_key FROM reservation_sagas WHERE id = $1 AND NOT completed FOR UPDATE SKIP LOCKED`, id).Scan(&buyerID, &key)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	releaseCtx, cancelRelease := context.WithTimeout(ctx, 2*time.Second)
	err = release(releaseCtx, id)
	cancelRelease()
	if err != nil {
		if _, updateErr := tx.Exec(ctx, `UPDATE reservation_sagas SET retry_at = now() + interval '10 seconds' WHERE id = $1`, id); updateErr != nil {
			return errors.Join(err, updateErr)
		}
		return errors.Join(err, tx.Commit(ctx))
	}
	if key != "" {
		if _, err = tx.Exec(ctx, `DELETE FROM idempotency_keys WHERE buyer_id = $1 AND key = $2 AND reservation_id = $3 AND order_id IS NULL`, buyerID, key, id); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE reservation_sagas SET completed = true WHERE id = $1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *PGRepository) ReconcileReservations(ctx context.Context, release ReservationAction) error {
	rows, err := r.pool.Query(ctx, `SELECT id::text FROM reservation_sagas WHERE NOT completed AND retry_at <= now() ORDER BY retry_at LIMIT 100`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	var result error
	for _, id := range ids {
		attemptCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		result = errors.Join(result, r.reconcileReservation(attemptCtx, id, release))
		cancel()
		if ctx.Err() != nil {
			break
		}
	}
	return result
}

func (r *PGRepository) cleanupFailedReservationIntent(ctx context.Context, id, buyerID, key string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollbackTransaction(ctx, tx)
	var reservationID *string
	err = tx.QueryRow(ctx, `SELECT reservation_id::text FROM idempotency_keys WHERE buyer_id = $1 AND key = $2 FOR UPDATE`, buyerID, key).Scan(&reservationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var persisted bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM reservation_sagas WHERE id = $1)`, id).Scan(&persisted); err != nil {
		return err
	}
	if !persisted && reservationID == nil {
		if _, err = tx.Exec(ctx, `DELETE FROM idempotency_keys WHERE buyer_id = $1 AND key = $2 AND order_id IS NULL`, buyerID, key); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
