package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrReservationConflict = errors.New("reservation is released or has different parameters")

func (r *PGRepository) ReserveReservation(ctx context.Context, id, listingID string, qty int64) (int64, error) {
	if qty <= 0 {
		return 0, ErrInvalidQuantity
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer rollbackReservation(ctx, tx)
	_, err = tx.Exec(ctx, `INSERT INTO reservations (id, listing_id, qty) VALUES ($1, $2, $3) ON CONFLICT (id) DO NOTHING`, id, listingID, qty)
	if err != nil {
		return 0, err
	}
	var storedListing *string
	var storedQty, remaining *int64
	var released bool
	err = tx.QueryRow(ctx, `SELECT listing_id::text, qty, remaining, released FROM reservations WHERE id = $1 FOR UPDATE`, id).Scan(&storedListing, &storedQty, &remaining, &released)
	if err != nil {
		return 0, err
	}
	if released || storedListing == nil || !strings.EqualFold(*storedListing, listingID) || storedQty == nil || *storedQty != qty {
		return 0, ErrReservationConflict
	}
	if remaining != nil {
		return *remaining, nil
	}
	nested := NewPGRepository(tx)
	value, err := nested.Reserve(ctx, listingID, qty)
	if err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, `UPDATE reservations SET remaining = $2 WHERE id = $1`, id, value); err != nil {
		return 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit reservation: %w", err)
	}
	return value, nil
}

func (r *PGRepository) ReleaseReservation(ctx context.Context, id string) (string, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer rollbackReservation(ctx, tx)
	if _, err = tx.Exec(ctx, `INSERT INTO reservations (id, released) VALUES ($1, true) ON CONFLICT (id) DO NOTHING`, id); err != nil {
		return "", err
	}
	var listingID *string
	var qty *int64
	var released bool
	err = tx.QueryRow(ctx, `SELECT listing_id::text, qty, released FROM reservations WHERE id = $1 FOR UPDATE`, id).Scan(&listingID, &qty, &released)
	if err != nil {
		return "", err
	}
	if !released {
		if _, err = tx.Exec(ctx, `UPDATE listings SET stock = stock + $2 WHERE id = $1`, *listingID, *qty); err != nil {
			return "", err
		}
		if _, err = tx.Exec(ctx, `UPDATE reservations SET released = true WHERE id = $1`, id); err != nil {
			return "", err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	if listingID == nil {
		return "", nil
	}
	return *listingID, nil
}

func rollbackReservation(ctx context.Context, tx pgx.Tx) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_ = tx.Rollback(cleanupCtx)
}
