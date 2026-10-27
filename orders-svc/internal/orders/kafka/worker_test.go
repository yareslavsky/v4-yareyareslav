package kafka

import (
	"context"
	"errors"
	"marketplace/orders-svc/internal/orders/model"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

type publisherFunc func(context.Context, int) ([]model.OutboxEvent, error)

func (f publisherFunc) SendUnpublishedEvents(ctx context.Context, limit int) ([]model.OutboxEvent, error) {
	return f(ctx, limit)
}

func startTestWorker(ctx context.Context, publisher MessagePublisher) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		NewWorker(publisher).Start(ctx)
	}()
	return done
}

func TestWorker_PeriodicPublication(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		var contexts []context.Context
		var callTimes []time.Time
		var mu sync.Mutex
		snapshot := func() ([]context.Context, []time.Time) {
			mu.Lock()
			defer mu.Unlock()
			return append([]context.Context(nil), contexts...), append([]time.Time(nil), callTimes...)
		}
		publisher := publisherFunc(func(ctx context.Context, limit int) ([]model.OutboxEvent, error) {
			if limit != 100 {
				t.Errorf("limit = %d, want 100", limit)
			}
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) != 2*time.Second {
				t.Errorf("publication deadline = %v, want 2 seconds from now", deadline)
			}
			mu.Lock()
			contexts = append(contexts, ctx)
			callTimes = append(callTimes, time.Now())
			mu.Unlock()
			return []model.OutboxEvent{{ID: 1}}, nil
		})
		done := startTestWorker(ctx, publisher)
		defer func() { cancel(); <-done }()
		synctest.Wait()
		started := time.Now()

		time.Sleep(199 * time.Millisecond)
		synctest.Wait()
		_, times := snapshot()
		if len(times) != 0 {
			t.Fatal("publication occurred before the first tick")
		}
		for i := 1; i <= 2; i++ {
			if i == 1 {
				time.Sleep(time.Millisecond)
			} else {
				time.Sleep(200 * time.Millisecond)
			}
			synctest.Wait()
			publicationContexts, times := snapshot()
			if len(times) != i {
				t.Fatalf("calls = %d, want %d", len(times), i)
			}
			if elapsed := times[i-1].Sub(started); elapsed != time.Duration(i)*200*time.Millisecond {
				t.Errorf("call %d at %v, want %v", i, elapsed, time.Duration(i)*200*time.Millisecond)
			}
			if !errors.Is(publicationContexts[i-1].Err(), context.Canceled) {
				t.Errorf("publication context was not canceled after return: %v", publicationContexts[i-1].Err())
			}
		}
		cancel()
		<-done
		time.Sleep(time.Second)
		_, times = snapshot()
		if len(times) != 2 {
			t.Errorf("calls after shutdown = %d, want 2", len(times))
		}
	})
}

func TestWorker_ContinuesAfterPublication(t *testing.T) {
	for _, failure := range []error{nil, errors.New("Kafka unavailable"), context.DeadlineExceeded} {
		name := "empty queue"
		if failure != nil {
			name = failure.Error()
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				calls := 0
				publisher := publisherFunc(func(context.Context, int) ([]model.OutboxEvent, error) {
					calls++
					if calls == 1 {
						return nil, failure
					}
					cancel()
					return []model.OutboxEvent{{ID: 1}}, nil
				})
				done := startTestWorker(ctx, publisher)
				<-done
				if calls != 2 {
					t.Errorf("calls = %d, want retry on next tick", calls)
				}
			})
		})
	}
}

func TestWorker_AlreadyCanceled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		publisher := publisherFunc(func(context.Context, int) ([]model.OutboxEvent, error) {
			t.Error("publisher called with canceled worker context")
			return nil, nil
		})
		<-startTestWorker(ctx, publisher)
	})
}

func TestWorker_CancelsActivePublication(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		entered := make(chan struct{})
		calls := 0
		publisher := publisherFunc(func(ctx context.Context, _ int) ([]model.OutboxEvent, error) {
			calls++
			close(entered)
			<-ctx.Done()
			if !errors.Is(ctx.Err(), context.Canceled) {
				t.Errorf("publication error = %v, want context.Canceled", ctx.Err())
			}
			return nil, ctx.Err()
		})
		done := startTestWorker(ctx, publisher)
		defer func() { cancel(); <-done }()
		<-entered
		cancel()
		<-done
		if calls != 1 {
			t.Errorf("calls = %d, want 1", calls)
		}
	})
}

func TestWorker_PublicationTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		calls := 0
		publisher := publisherFunc(func(ctx context.Context, _ int) ([]model.OutboxEvent, error) {
			calls++
			if calls == 1 {
				started := time.Now()
				<-ctx.Done()
				if !errors.Is(ctx.Err(), context.DeadlineExceeded) || time.Since(started) != 2*time.Second {
					t.Errorf("publication ended after %v with %v, want 2 seconds and DeadlineExceeded", time.Since(started), ctx.Err())
				}
				return nil, ctx.Err()
			}
			cancel()
			return nil, nil
		})
		<-startTestWorker(ctx, publisher)
		if calls != 2 {
			t.Errorf("calls = %d, want retry after timeout", calls)
		}
	})
}
