package repository

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func reservationTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("DATABASE_URL_ORDERS")
	if url == "" {
		t.Skip("DATABASE_URL_ORDERS is required")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := pgx.Identifier{"reservation_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	paths, err := filepath.Glob("../../../migrations/*.up.sql")
	if err != nil || len(paths) == 0 {
		t.Fatalf("migrations: %v", err)
	}
	for _, path := range paths {
		sql, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, string(sql)); err != nil {
			t.Fatal(err)
		}
	}
	return pool
}

func TestReservationSagaIntegration(t *testing.T) {
	pool := reservationTestPool(t)
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `ALTER TABLE orders ADD CONSTRAINT test_insert_failure CHECK (qty <> 7)`); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"success", "insert failure", "lost reserve response", "compensation retry", "canceled request", "lost commit response"} {
		t.Run(scenario, func(t *testing.T) {
			requestCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			order := createTestOrder()
			order.BuyerID = uuid.NewString()
			if scenario != "success" && scenario != "lost commit response" {
				order.Qty = 7
			}
			repo := NewPGRepository(pool, nil)
			if scenario == "lost commit response" {
				repo = NewPGRepository(&lostCommitPool{Pool: pool}, nil)
			}
			key := "test-order"
			if ok, err := repo.ReserveIdempotencyKey(ctx, order.BuyerID, key); err != nil || !ok {
				t.Fatalf("key: %v, %v", ok, err)
			}
			stock := 10
			reservationID := ""
			reserve := func(callCtx context.Context, id string) error {
				if _, ok := callCtx.Deadline(); !ok {
					t.Error("reserve deadline is missing")
				}
				reservationID = id
				stock -= order.Qty
				if scenario == "canceled request" {
					cancel()
					return context.Canceled
				}
				if scenario == "lost reserve response" {
					return context.DeadlineExceeded
				}
				return nil
			}
			releaseCalls := 0
			release := func(callCtx context.Context, id string) error {
				if callCtx.Err() != nil || id != reservationID {
					t.Errorf("invalid compensation context or ID: %v, %s", callCtx.Err(), id)
				}
				releaseCalls++
				if scenario == "compensation retry" && releaseCalls == 1 {
					return errors.New("catalog unavailable")
				}
				stock += order.Qty
				return nil
			}
			created, err := repo.CreateReserved(requestCtx, order, key, reserve, release)
			switch scenario {
			case "success":
				if err != nil || created == nil || stock != 5 || releaseCalls != 0 {
					t.Fatalf("success: %+v, %v, stock=%d, releases=%d", created, err, stock, releaseCalls)
				}
			case "lost commit response":
				if err == nil || stock != 5 || releaseCalls != 0 {
					t.Fatalf("ambiguous commit: %v, stock=%d, releases=%d", err, stock, releaseCalls)
				}
				state, lookupErr := repo.GetIdempotencyKeyState(ctx, order.BuyerID, key)
				if lookupErr != nil || state == nil || state.OrderID == nil {
					t.Fatalf("committed order missing: %+v, %v", state, lookupErr)
				}
			default:
				if err == nil {
					t.Fatal("expected creation failure")
				}
				if scenario == "compensation retry" {
					var pending bool
					if err := pool.QueryRow(ctx, `SELECT NOT completed FROM reservation_sagas WHERE id = $1`, reservationID).Scan(&pending); err != nil || !pending {
						t.Fatalf("pending saga: %v, %v", pending, err)
					}
					if ok, err := repo.ReserveIdempotencyKey(ctx, order.BuyerID, key); err != nil || ok {
						t.Fatalf("key released before compensation: %v, %v", ok, err)
					}
					if _, err := pool.Exec(ctx, `UPDATE reservation_sagas SET retry_at = now() WHERE id = $1`, reservationID); err != nil {
						t.Fatal(err)
					}
					if err := NewPGRepository(pool, nil).ReconcileReservations(ctx, release); err != nil {
						t.Fatal(err)
					}
				}
				if stock != 10 {
					t.Fatalf("stock=%d, want 10", stock)
				}
				if ok, err := repo.ReserveIdempotencyKey(ctx, order.BuyerID, key); err != nil || !ok {
					t.Fatalf("key not released: %v, %v", ok, err)
				}
			}
			var pending bool
			if err := pool.QueryRow(ctx, `SELECT NOT completed FROM reservation_sagas WHERE id = $1`, reservationID).Scan(&pending); err != nil || pending {
				t.Fatalf("saga not completed: %v, %v", pending, err)
			}
		})
	}
}

type lostCommitPool struct {
	Pool
	begins int
	failAt int
}

func TestReservationRecoveryIntegration(t *testing.T) {
	pool := reservationTestPool(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	repo := NewPGRepository(pool, nil)
	order := createTestOrder()
	order.BuyerID = uuid.NewString()
	id := uuid.NewString()
	if err := repo.persistReservation(ctx, id, order.BuyerID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE reservation_sagas SET retry_at = now() WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	reserveEntered := make(chan struct{})
	continueReserve := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := repo.createReserved(ctx, id, order, "", func(callCtx context.Context, _ string) error {
			close(reserveEntered)
			select {
			case <-continueReserve:
				return nil
			case <-callCtx.Done():
				return callCtx.Err()
			}
		})
		done <- err
	}()
	select {
	case <-reserveEntered:
	case err := <-done:
		t.Fatalf("reserve did not start: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	releases := 0
	release := func(context.Context, string) error { releases++; return nil }
	err := NewPGRepository(pool, nil).ReconcileReservations(ctx, release)
	close(continueReserve)
	if createErr := <-done; createErr != nil {
		t.Fatal(createErr)
	}
	if err != nil || releases != 0 {
		t.Fatalf("active request compensated: releases=%d, error=%v", releases, err)
	}
	crashedID := uuid.NewString()
	if err := repo.persistReservation(ctx, crashedID, order.BuyerID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE reservation_sagas SET retry_at = now() WHERE id = $1`, crashedID); err != nil {
		t.Fatal(err)
	}
	if err := NewPGRepository(pool, nil).ReconcileReservations(ctx, func(_ context.Context, id string) error {
		if id != crashedID {
			t.Errorf("recovered reservation=%s, want %s", id, crashedID)
		}
		releases++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if releases != 1 {
		t.Fatalf("recovery releases=%d", releases)
	}
}

func (p *lostCommitPool) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := p.Pool.Begin(ctx)
	p.begins++
	failAt := p.failAt
	if failAt == 0 {
		failAt = 2
	}
	if err == nil && p.begins == failAt {
		return lostCommitTx{Tx: tx}, nil
	}
	return tx, err
}

func TestReservationIntentFailureIntegration(t *testing.T) {
	pool := reservationTestPool(t)
	ctx := t.Context()
	for _, ambiguous := range []bool{false, true} {
		t.Run(map[bool]string{false: "insert rejected", true: "lost intent commit response"}[ambiguous], func(t *testing.T) {
			order := createTestOrder()
			order.BuyerID = uuid.NewString()
			repo := NewPGRepository(pool, nil)
			key := "reject"
			if ambiguous {
				key = "ambiguous"
				repo = NewPGRepository(&lostCommitPool{Pool: pool, failAt: 1}, nil)
			}
			if _, err := pool.Exec(ctx, `ALTER TABLE reservation_sagas DROP CONSTRAINT IF EXISTS test_intent_failure`); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `ALTER TABLE reservation_sagas ADD CONSTRAINT test_intent_failure CHECK (idempotency_key <> 'reject')`); err != nil {
				t.Fatal(err)
			}
			if ok, err := repo.ReserveIdempotencyKey(ctx, order.BuyerID, key); err != nil || !ok {
				t.Fatalf("key: %v, %v", ok, err)
			}
			called := false
			_, err := repo.CreateReserved(ctx, order, key, func(context.Context, string) error { called = true; return nil }, func(context.Context, string) error { called = true; return nil })
			if err == nil || called {
				t.Fatalf("failed intent made RPC: called=%v, error=%v", called, err)
			}
			if ambiguous {
				if _, err := pool.Exec(ctx, `UPDATE reservation_sagas SET retry_at = now() WHERE NOT completed`); err != nil {
					t.Fatal(err)
				}
				if err := NewPGRepository(pool, nil).ReconcileReservations(ctx, func(context.Context, string) error { return nil }); err != nil {
					t.Fatal(err)
				}
			}
			if ok, err := repo.ReserveIdempotencyKey(ctx, order.BuyerID, key); err != nil || !ok {
				t.Fatalf("key not released: %v, %v", ok, err)
			}
		})
	}
}

type lostCommitTx struct{ pgx.Tx }

func (tx lostCommitTx) Commit(ctx context.Context) error {
	if err := tx.Tx.Commit(ctx); err != nil {
		return err
	}
	return context.DeadlineExceeded
}
