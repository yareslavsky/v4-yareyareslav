package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"marketplace/catalog-svc/internal/cache"
	"marketplace/catalog-svc/internal/listings/model"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

type commandHook struct {
	process redis.ProcessHook
}

func (h commandHook) DialHook(next redis.DialHook) redis.DialHook     { return next }
func (h commandHook) ProcessHook(redis.ProcessHook) redis.ProcessHook { return h.process }
func (h commandHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func newTestAdapter(t *testing.T, process redis.ProcessHook) *RedisAdapter {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	client.AddHook(commandHook{process: process})
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	})
	return NewRedisAdapter(client)
}

func cachedListing() model.Listing {
	return model.Listing{
		ID: "listing", SellerID: "seller", Title: "Молоко", Price: 1 << 40,
		Stock: 1 << 41, Sold: 1 << 42, CreatedAt: "2026-10-13T12:00:00.123456789+03:00",
	}
}

func TestRedisSet(t *testing.T) {
	for _, tc := range []struct {
		name       string
		ttl        time.Duration
		expiration []any
	}{
		{name: "seconds", ttl: 30 * time.Second, expiration: []any{"ex", int64(30)}},
		{name: "milliseconds", ttl: 1500 * time.Millisecond, expiration: []any{"px", int64(1500)}},
		{name: "no expiration"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			want := cachedListing()
			calls := 0
			adapter := newTestAdapter(t, func(gotCtx context.Context, cmd redis.Cmder) error {
				calls++
				args := cmd.Args()
				if gotCtx != ctx || cmd.Name() != "set" || args[1] != "listing:listing" {
					t.Fatalf("unexpected command: %v", args)
				}
				data, ok := args[2].([]byte)
				if !ok {
					t.Fatalf("value type = %T, want JSON bytes", args[2])
				}
				var got model.Listing
				if err := json.Unmarshal(data, &got); err != nil {
					t.Fatal(err)
				}
				if got != want {
					t.Fatalf("cached listing = %+v, want %+v", got, want)
				}
				if len(args[3:]) != len(tc.expiration) || len(tc.expiration) > 0 && !reflect.DeepEqual(args[3:], tc.expiration) {
					t.Fatalf("expiration = %v, want %v", args[3:], tc.expiration)
				}
				cmd.(*redis.StatusCmd).SetVal("OK")
				return nil
			})
			if err := adapter.Set(ctx, "listing:listing", want, tc.ttl); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("commands = %d, want 1", calls)
			}
		})
	}
}

func TestRedisGet(t *testing.T) {
	want := cachedListing()
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("redis unavailable")
	for _, tc := range []struct {
		name, value       string
		redisErr, wantErr error
		invalidJSON       bool
	}{
		{name: "hit", value: string(data)},
		{name: "miss", redisErr: redis.Nil, wantErr: cache.ErrCacheMiss},
		{name: "redis failure", redisErr: failure, wantErr: failure},
		{name: "corrupt JSON", value: "{", invalidJSON: true},
		{name: "wrong field type", value: `{"Stock":"many"}`, invalidJSON: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			calls := 0
			adapter := newTestAdapter(t, func(gotCtx context.Context, cmd redis.Cmder) error {
				calls++
				if gotCtx != ctx || !reflect.DeepEqual(cmd.Args(), []any{"get", "listing:listing"}) {
					t.Fatalf("unexpected command: %v", cmd.Args())
				}
				cmd.(*redis.StringCmd).SetVal(tc.value)
				return tc.redisErr
			})
			got, err := adapter.Get(ctx, "listing:listing")
			if calls != 1 {
				t.Fatalf("commands = %d, want 1", calls)
			}
			if tc.invalidJSON {
				if got != nil || err == nil || !strings.HasPrefix(err.Error(), "redis get unmarshal listing:") {
					t.Fatalf("listing = %+v, error = %v, want JSON error", got, err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr != nil {
				if got != nil {
					t.Fatalf("listing on failure: %+v", got)
				}
				return
			}
			if got == nil || *got != want {
				t.Fatalf("listing = %+v, want %+v", got, want)
			}
		})
	}
}

func TestRedisDelete(t *testing.T) {
	failure := errors.New("redis unavailable")
	for _, tc := range []struct {
		name  string
		count int64
		err   error
	}{
		{name: "all keys deleted", count: 2},
		{name: "some keys deleted", count: 1},
		{name: "no keys found"},
		{name: "redis failure", err: failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			calls := 0
			adapter := newTestAdapter(t, func(gotCtx context.Context, cmd redis.Cmder) error {
				calls++
				if gotCtx != ctx || !reflect.DeepEqual(cmd.Args(), []any{"del", "listing:first", "listing:second"}) {
					t.Fatalf("unexpected command: %v", cmd.Args())
				}
				cmd.(*redis.IntCmd).SetVal(tc.count)
				return tc.err
			})
			deleted, err := adapter.Delete(ctx, "listing:first", "listing:second")
			if calls != 1 || deleted != (tc.count > 0 && tc.err == nil) || !errors.Is(err, tc.err) {
				t.Fatalf("commands = %d, deleted = %v, error = %v", calls, deleted, err)
			}
		})
	}
}

func TestRedisSetErrors(t *testing.T) {
	for _, failure := range []error{errors.New("redis unavailable"), context.Canceled, context.DeadlineExceeded} {
		t.Run(failure.Error(), func(t *testing.T) {
			adapter := newTestAdapter(t, func(context.Context, redis.Cmder) error { return failure })
			err := adapter.Set(t.Context(), "listing:listing", cachedListing(), time.Minute)
			if !errors.Is(err, failure) || !strings.HasPrefix(err.Error(), "redis set:") {
				t.Fatalf("error = %v, want wrapped %v", err, failure)
			}
		})
	}
}
