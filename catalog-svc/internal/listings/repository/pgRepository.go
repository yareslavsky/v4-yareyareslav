package repository

import (
	"context"
	"errors"
	"fmt"
	"marketplace/catalog-svc/internal/listings/model"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrNotFound          = errors.New("listing not found")
	ErrForbidden         = errors.New("listing belongs to another seller")
	ErrInvalidQuantity   = errors.New("quantity must be positive")
	ErrInsufficientStock = errors.New("insufficient listing stock")
)

type Pool interface {
	Begin(context.Context) (pgx.Tx, error)
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type PGRepository struct {
	pool Pool
}

func NewPGRepository(pool Pool) *PGRepository {
	return &PGRepository{
		pool: pool,
	}
}

func (r *PGRepository) Create(
	ctx context.Context,
	listing model.Listing,
) (*model.Listing, error) {
	created := model.Listing{
		Title:    listing.Title,
		Price:    listing.Price,
		Stock:    listing.Stock,
		Sold:     0,
		SellerID: listing.SellerID,
	}

	var createdAt time.Time
	err := r.pool.QueryRow(
		ctx,
		`
		INSERT INTO listings (id, title, price, stock, sold, seller_id)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5)
		RETURNING id::text, created_at
		`,
		listing.Title,
		listing.Price,
		listing.Stock,
		0,
		listing.SellerID,
	).Scan(&created.ID, &createdAt)
	if err != nil {
		return nil, fmt.Errorf("create listing: %w", err)
	}
	created.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)

	return &created, nil
}

func (r *PGRepository) Get(
	ctx context.Context,
	id string,
) (*model.Listing, error) {
	var listing model.Listing
	var createdAt time.Time
	err := r.pool.QueryRow(
		ctx,
		`
		SELECT id::text, title, price, stock, sold, seller_id::text, created_at
		FROM listings
		WHERE id = $1
		`,
		id,
	).Scan(
		&listing.ID,
		&listing.Title,
		&listing.Price,
		&listing.Stock,
		&listing.Sold,
		&listing.SellerID,
		&createdAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get listing: %w", err)
	}
	listing.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)

	return &listing, nil
}

func (r *PGRepository) List(
	ctx context.Context,
	limit,
	offset int,
) ([]model.Listing, int64, error) {
	rows, err := r.pool.Query(
		ctx,
		`
		SELECT id::text, title, price, stock, sold, seller_id::text, created_at
		FROM listings
		ORDER BY created_at ASC, id ASC
		LIMIT $1
		OFFSET $2
		`,
		limit,
		offset,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("list listings: %w", err)
	}
	defer rows.Close()

	listings := make([]model.Listing, 0)
	for rows.Next() {
		var createdAt time.Time
		var listing model.Listing
		if err := rows.Scan(
			&listing.ID,
			&listing.Title,
			&listing.Price,
			&listing.Stock,
			&listing.Sold,
			&listing.SellerID,
			&createdAt,
		); err != nil {
			return nil, 0, fmt.Errorf("scan listing: %w", err)
		}
		listing.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
		listings = append(listings, listing)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("list listings: %w", err)
	}

	rows.Close()
	var total int64
	if err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM listings`).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count listings: %w", err)
	}

	return listings, total, nil
}

func (r *PGRepository) Update(
	ctx context.Context,
	listing model.UpdateListing,
) (*model.Listing, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin update listing: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.WithoutCancel(ctx))
	}()

	var isOwner bool
	err = tx.QueryRow(ctx, `
		SELECT seller_id = $2::uuid
		FROM listings
		WHERE id = $1
		FOR UPDATE
	`, listing.ID, listing.SellerID).Scan(&isOwner)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("check listing owner: %w", err)
	}
	if !isOwner {
		return nil, ErrForbidden
	}

	var updated model.Listing
	var createdAt time.Time
	err = tx.QueryRow(
		ctx,
		`
		UPDATE listings
		SET title = COALESCE($2::text, title),
		    price = COALESCE($3::bigint, price),
		    stock = COALESCE($4::bigint, stock)
		WHERE id = $1
		RETURNING id::text, title, price, stock, sold, seller_id::text, created_at
		`,
		listing.ID,
		listing.Title,
		listing.Price,
		listing.Stock,
	).Scan(
		&updated.ID,
		&updated.Title,
		&updated.Price,
		&updated.Stock,
		&updated.Sold,
		&updated.SellerID,
		&createdAt,
	)
	if err != nil {
		return nil, fmt.Errorf("update listing: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit update listing: %w", err)
	}
	updated.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
	return &updated, nil
}

func (r *PGRepository) Reserve(
	ctx context.Context,
	listingID string,
	qty int64,
) (int64, error) {
	if qty <= 0 {
		return 0, ErrInvalidQuantity
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin reserve listing: %w", err)
	}
	defer rollbackReservation(ctx, tx)

	var stock int64
	err = tx.QueryRow(
		ctx,
		`
		SELECT stock
		FROM listings
		WHERE id = $1
		FOR UPDATE
		`,
		listingID,
	).Scan(&stock)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("get listing stock: %w", err)
	}
	if stock < qty {
		return 0, ErrInsufficientStock
	}

	var remaining int64
	err = tx.QueryRow(
		ctx,
		`
		UPDATE listings
		SET stock = stock - $2
		WHERE id = $1
		RETURNING stock
		`,
		listingID,
		qty,
	).Scan(&remaining)
	if err != nil {
		return 0, fmt.Errorf("reserve listing: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit reserve listing: %w", err)
	}

	return remaining, nil
}

func (r *PGRepository) ApplyOrderCreated(
	ctx context.Context,
	eventID, listingID string,
	qty int64,
) (bool, error) {
	if qty <= 0 {
		return false, ErrInvalidQuantity
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin apply order created: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.WithoutCancel(ctx))
	}()

	result, err := tx.Exec(ctx, `
		INSERT INTO processed_events (event_id)
		VALUES ($1)
		ON CONFLICT (event_id) DO NOTHING
	`, eventID)
	if err != nil {
		return false, fmt.Errorf("record processed event: %w", err)
	}
	if result.RowsAffected() == 0 {
		return false, nil
	}

	result, err = tx.Exec(ctx, `
		UPDATE listings
		SET sold = sold + $2
		WHERE id = $1
	`, listingID, qty)
	if err != nil {
		return false, fmt.Errorf("increment listing sold: %w", err)
	}
	if result.RowsAffected() == 0 {
		return false, ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit apply order created: %w", err)
	}

	return true, nil
}

func (r *PGRepository) Delete(
	ctx context.Context,
	id,
	sellerID string,
) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete listing: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.WithoutCancel(ctx))
	}()

	var isOwner bool
	err = tx.QueryRow(ctx, `
		SELECT seller_id = $2::uuid
		FROM listings
		WHERE id = $1
		FOR UPDATE
	`, id, sellerID).Scan(&isOwner)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("check listing owner: %w", err)
	}
	if !isOwner {
		return ErrForbidden
	}

	res, err := tx.Exec(
		ctx,
		`
		DELETE FROM listings
		WHERE id = $1 AND seller_id = $2
		`,
		id,
		sellerID,
	)
	if err != nil {
		return fmt.Errorf("delete listing: %w", err)
	}

	rowsAffected := res.RowsAffected()
	if rowsAffected == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete listing: %w", err)
	}

	return nil
}
