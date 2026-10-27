package repository

import (
	"context"
	"errors"
	"marketplace/catalog-svc/internal/listings/model"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRepositoryIntegration(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL_CATALOG")
	if databaseURL == "" {
		t.Skip("DATABASE_URL_CATALOG is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := "catalog_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	migrations, err := filepath.Glob("../../../migrations/*.up.sql")
	if err != nil || len(migrations) == 0 {
		t.Fatalf("find migrations: %v", err)
	}
	for _, path := range migrations {
		sql, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("apply %s: %v", path, err)
		}
	}
	repo := NewPGRepository(pool)
	t.Run("tracked reservations", func(t *testing.T) {
		listing, err := repo.Create(ctx, model.Listing{SellerID: uuid.NewString(), Title: "reserved", Price: 100, Stock: 10})
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Exec(ctx, `DELETE FROM listings WHERE id = $1`, listing.ID)
		id := uuid.NewString()
		for range 2 {
			remaining, err := repo.ReserveReservation(ctx, id, listing.ID, 3)
			if err != nil || remaining != 7 {
				t.Fatalf("reserve: remaining=%d, error=%v", remaining, err)
			}
		}
		if _, err := repo.ReserveReservation(ctx, id, listing.ID, 4); !errors.Is(err, ErrReservationConflict) {
			t.Fatalf("changed quantity: %v", err)
		}
		var workers sync.WaitGroup
		for range 20 {
			workers.Go(func() {
				if _, err := repo.ReleaseReservation(ctx, id); err != nil {
					t.Error(err)
				}
			})
		}
		workers.Wait()
		got, err := repo.Get(ctx, listing.ID)
		if err != nil || got.Stock != 10 {
			t.Fatalf("released stock: %+v, %v", got, err)
		}
		if _, err := repo.ReserveReservation(ctx, id, listing.ID, 3); !errors.Is(err, ErrReservationConflict) {
			t.Fatalf("late reserve: %v", err)
		}
		cancelled := uuid.NewString()
		if _, err := repo.ReleaseReservation(ctx, cancelled); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.ReserveReservation(ctx, cancelled, listing.ID, 3); !errors.Is(err, ErrReservationConflict) {
			t.Fatalf("reserve after early release: %v", err)
		}
		concurrentID := uuid.NewString()
		for range 20 {
			workers.Go(func() {
				if _, err := repo.ReserveReservation(ctx, concurrentID, listing.ID, 2); err != nil {
					t.Error(err)
				}
			})
		}
		workers.Wait()
		got, err = repo.Get(ctx, listing.ID)
		if err != nil || got.Stock != 8 {
			t.Fatalf("concurrent reserve stock: %+v, %v", got, err)
		}
	})
	sellerID := uuid.NewString()
	listing, err := repo.Create(ctx, model.Listing{SellerID: sellerID, Title: "milk", Price: 5000, Stock: 10, Sold: 99})
	if err != nil {
		t.Fatal(err)
	}
	if listing.Sold != 0 {
		t.Fatalf("fresh sold = %d", listing.Sold)
	}
	if _, err := repo.Create(ctx, model.Listing{SellerID: sellerID, Title: "invalid", Price: 0}); err == nil {
		t.Fatal("zero price was accepted")
	}
	page, total, err := repo.List(ctx, 20, 0)
	if err != nil || len(page) != 1 || total != 1 || page[0].ID != listing.ID {
		t.Fatalf("page = %+v, total = %d, error = %v", page, total, err)
	}
	page, total, err = repo.List(ctx, 20, 1)
	if err != nil || len(page) != 0 || total != 1 {
		t.Fatalf("empty page = %+v, total = %d, error = %v", page, total, err)
	}

	results := make(chan error, 100)
	var workers sync.WaitGroup
	for i := 0; i < 100; i++ {
		workers.Go(func() {
			_, err := repo.Reserve(ctx, listing.ID, 1)
			results <- err
		})
	}
	workers.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrInsufficientStock) {
			t.Fatal(err)
		}
	}
	if successes != 10 {
		t.Fatalf("successful reservations = %d, want 10", successes)
	}

	eventID := uuid.NewString()
	applications := make(chan bool, 10)
	eventErrors := make(chan error, 10)
	for i := 0; i < 10; i++ {
		workers.Go(func() {
			applied, err := repo.ApplyOrderCreated(ctx, eventID, listing.ID, 3)
			applications <- applied
			eventErrors <- err
		})
	}
	workers.Wait()
	close(applications)
	close(eventErrors)
	for err := range eventErrors {
		if err != nil {
			t.Fatal(err)
		}
	}
	appliedCount := 0
	for applied := range applications {
		if applied {
			appliedCount++
		}
	}
	got, err := repo.Get(ctx, listing.ID)
	if err != nil || appliedCount != 1 || got == nil || got.Stock != 0 || got.Sold != 3 {
		t.Fatalf("listing = %+v, applied = %d, error = %v", got, appliedCount, err)
	}
	title := "updated"
	updated, err := repo.Update(ctx, model.UpdateListing{ID: listing.ID, SellerID: sellerID, Title: &title})
	if err != nil || updated == nil || updated.Title != title || updated.Stock != 0 || updated.Sold != 3 {
		t.Fatalf("updated = %+v, error = %v", updated, err)
	}
	if err := repo.Delete(ctx, listing.ID, uuid.NewString()); !errors.Is(err, ErrForbidden) {
		t.Fatalf("delete by another seller: %v", err)
	}
	if err := repo.Delete(ctx, listing.ID, sellerID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Get(ctx, listing.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get deleted listing: %v", err)
	}
	if err := repo.Delete(ctx, listing.ID, sellerID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete missing listing: %v", err)
	}
}
