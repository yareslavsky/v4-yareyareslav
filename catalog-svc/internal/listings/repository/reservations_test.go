package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/pashagolub/pgxmock/v5"
)

func TestReleaseReservationIdempotency(t *testing.T) {
	for _, released := range []bool{false, true} {
		t.Run(map[bool]string{false: "active", true: "already released"}[released], func(t *testing.T) {
			pool, err := pgxmock.NewPool()
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			storedListing, storedQty := "listing", int64(3)
			pool.ExpectBegin()
			pool.ExpectExec("INSERT INTO reservations").WithArgs("reservation").WillReturnResult(pgxmock.NewResult("INSERT", 0))
			pool.ExpectQuery("SELECT listing_id::text, qty, released").WithArgs("reservation").WillReturnRows(pgxmock.NewRows([]string{"listing_id", "qty", "released"}).AddRow(&storedListing, &storedQty, released))
			if !released {
				pool.ExpectExec("UPDATE listings SET stock = stock \\+ \\$2").WithArgs("listing", int64(3)).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				pool.ExpectExec("UPDATE reservations SET released = true").WithArgs("reservation").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			}
			pool.ExpectCommit()
			pool.ExpectRollback()
			listingID, err := NewPGRepository(pool).ReleaseReservation(context.Background(), "reservation")
			if err != nil || listingID != "listing" {
				t.Fatalf("release=%s, %v", listingID, err)
			}
			if err := pool.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReserveReservationReplay(t *testing.T) {
	for _, released := range []bool{false, true} {
		pool, err := pgxmock.NewPool()
		if err != nil {
			t.Fatal(err)
		}
		pool.ExpectBegin()
		storedListing, storedQty, storedRemaining := "listing", int64(3), int64(7)
		pool.ExpectExec("INSERT INTO reservations").WithArgs("reservation", "listing", int64(3)).WillReturnResult(pgxmock.NewResult("INSERT", 0))
		pool.ExpectQuery("SELECT listing_id::text, qty, remaining, released").WithArgs("reservation").WillReturnRows(pgxmock.NewRows([]string{"listing_id", "qty", "remaining", "released"}).AddRow(&storedListing, &storedQty, &storedRemaining, released))
		pool.ExpectRollback()
		remaining, err := NewPGRepository(pool).ReserveReservation(context.Background(), "reservation", "listing", 3)
		if released {
			if !errors.Is(err, ErrReservationConflict) {
				t.Fatalf("released reservation: %v", err)
			}
		} else if err != nil || remaining != 7 {
			t.Fatalf("replay=%d, %v", remaining, err)
		}
		if err := pool.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
		pool.Close()
	}
}
