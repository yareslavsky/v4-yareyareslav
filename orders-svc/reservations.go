package main

import (
	"context"
	"log/slog"
	"time"
)

type reservationReconciler interface {
	ReconcileReservations(context.Context, func(context.Context, string) error) error
}

func runCompensations(ctx context.Context, repository reservationReconciler, release func(context.Context, string) error) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := repository.ReconcileReservations(ctx, release); err != nil && ctx.Err() == nil {
				slog.WarnContext(ctx, "reservation reconciliation failed", "error", err)
			}
		}
	}
}
