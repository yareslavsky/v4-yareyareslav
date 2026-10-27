package main

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestWaitWorker_AlreadyStopped(t *testing.T) {
	done := make(chan struct{})
	close(done)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitWorker(ctx, done); err != nil {
		t.Errorf("stopped worker with canceled context returned %v, want nil", err)
	}
}

func TestWaitWorker_Finishes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		done := make(chan struct{})
		go func() {
			time.Sleep(time.Second)
			close(done)
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		started := time.Now()
		if err := waitWorker(ctx, done); err != nil {
			t.Fatalf("waitWorker returned %v, want nil", err)
		}
		if elapsed := time.Since(started); elapsed != time.Second {
			t.Errorf("waited %v, want 1 second", elapsed)
		}
	})
}

func TestWaitWorker_Deadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		started := time.Now()
		err := waitWorker(ctx, make(chan struct{}))
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want DeadlineExceeded", err)
		}
		if elapsed := time.Since(started); elapsed != 3*time.Second {
			t.Errorf("waited %v, want 3 seconds", elapsed)
		}
	})
}

func TestWaitWorker_Canceled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			time.Sleep(time.Second)
			cancel()
		}()
		err := waitWorker(ctx, make(chan struct{}))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want Canceled", err)
		}
	})
}
