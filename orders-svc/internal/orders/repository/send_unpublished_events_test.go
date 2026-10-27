package repository

import (
	"context"
	"errors"
	"marketplace/orders-svc/internal/orders/model"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v5"
	"github.com/segmentio/kafka-go"
)

func TestSendUnpublishedEvents(t *testing.T) {
	events := []model.OutboxEvent{
		{ID: 7, Topic: "order.created", Key: "order-1", Payload: []byte(`{"order_id":"order-1","qty":5}`)},
		{ID: 9, Topic: "order.created", Key: "order-2", Payload: []byte(`{"order_id":"order-2","qty":2}`)},
	}
	tests := []struct {
		name       string
		step       string
		wantPrefix string
		wantWrites int
	}{
		{name: "publishes batch", wantWrites: 1},
		{name: "empty queue", step: "empty"},
		{name: "begin fails", step: "begin", wantPrefix: "start get unpublished transaction:"},
		{name: "query fails", step: "query", wantPrefix: "get orders:"},
		{name: "scan fails", step: "scan", wantPrefix: "orders rows scan:"},
		{name: "read fails", step: "read", wantPrefix: "read outbox events:"},
		{name: "Kafka fails", step: "publish", wantPrefix: "publish outbox events:", wantWrites: 1},
		{name: "update fails", step: "update", wantPrefix: "update unpublished events:", wantWrites: 1},
		{name: "update count mismatch", step: "count", wantPrefix: "updated 1 outbox events, expected 2", wantWrites: 1},
		{name: "commit fails", step: "commit", wantPrefix: "commit updated events:", wantWrites: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := newTestPool(t, pgxmock.QueryMatcherEqual)
			failure := errors.New("operation failed")
			begin := pool.ExpectBegin()
			if tt.step == "begin" {
				begin.WillReturnError(failure)
			} else {
				query := pool.ExpectQuery(
					"SELECT id, topic, key, payload FROM outbox WHERE published_at IS NULL ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED",
				).WithArgs(100)
				if tt.step == "query" {
					query.WillReturnError(failure)
				} else {
					rows := pgxmock.NewRows([]string{"id", "topic", "key", "payload"})
					switch tt.step {
					case "empty":
					case "scan":
						rows.AddRow("invalid ID", events[0].Topic, events[0].Key, events[0].Payload)
					default:
						for _, event := range events {
							rows.AddRow(event.ID, event.Topic, event.Key, event.Payload)
						}
					}
					if tt.step == "read" {
						rows.CloseError(failure)
					}
					query.WillReturnRows(rows).RowsWillBeClosed()
				}
				if tt.wantWrites == 0 {
					pool.ExpectRollback()
				}
			}

			writes := 0
			writer := messageWriterFunc(func(ctx context.Context, messages ...kafka.Message) error {
				writes++
				wantMessages := []kafka.Message{
					{Topic: events[0].Topic, Key: []byte(events[0].Key), Value: events[0].Payload},
					{Topic: events[1].Topic, Key: []byte(events[1].Key), Value: events[1].Payload},
				}
				if !reflect.DeepEqual(messages, wantMessages) {
					t.Errorf("messages = %+v, want %+v", messages, wantMessages)
				}
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 5*time.Second {
					t.Errorf("expected publication deadline within 5 seconds, got %v", deadline)
				}
				if tt.step == "publish" {
					pool.ExpectRollback()
					return failure
				}

				update := pool.ExpectExec("UPDATE outbox SET published_at = NOW() WHERE id = ANY($1)").
					WithArgs([]int64{7, 9})
				switch tt.step {
				case "update":
					update.WillReturnError(failure)
				case "count":
					update.WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				default:
					update.WillReturnResult(pgxmock.NewResult("UPDATE", 2))
					commit := pool.ExpectCommit()
					if tt.step == "commit" {
						commit.WillReturnError(failure)
					}
				}
				pool.ExpectRollback()
				return nil
			})

			repo := NewPGRepository(pool, writer)
			published, err := repo.SendUnpublishedEvents(context.Background(), 100)
			if tt.wantPrefix != "" {
				if err == nil || !strings.HasPrefix(err.Error(), tt.wantPrefix) {
					t.Errorf("error = %v, want prefix %q", err, tt.wantPrefix)
				}
				if tt.step != "scan" && tt.step != "count" && !errors.Is(err, failure) {
					t.Errorf("error = %v, want wrapped operation failure", err)
				}
				if published != nil {
					t.Errorf("published = %+v, want nil", published)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if tt.step == "empty" {
					if len(published) != 0 {
						t.Errorf("published = %+v, want empty queue", published)
					}
				} else if !reflect.DeepEqual(published, events) {
					t.Errorf("published = %+v, want %+v", published, events)
				}
			}
			if writes != tt.wantWrites {
				t.Errorf("Kafka writes = %d, want %d", writes, tt.wantWrites)
			}
		})
	}
}
