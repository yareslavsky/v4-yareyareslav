package kafka

import (
	"context"
	"errors"
	"fmt"
	"marketplace/catalog-svc/internal/listings/dto"
	"marketplace/catalog-svc/internal/listings/repository"
	"marketplace/catalog-svc/internal/listings/service"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"github.com/segmentio/kafka-go"
)

type readerStub struct {
	fetch  func(context.Context) (kafka.Message, error)
	commit func(context.Context, ...kafka.Message) error
}

func (r readerStub) FetchMessage(ctx context.Context) (kafka.Message, error) {
	return r.fetch(ctx)
}

func (r readerStub) CommitMessages(ctx context.Context, messages ...kafka.Message) error {
	return r.commit(ctx, messages...)
}

type serviceFunc func(context.Context, dto.OrderCreatedEvent) (bool, error)

func (f serviceFunc) ApplyOrderCreated(ctx context.Context, event dto.OrderCreatedEvent) (bool, error) {
	return f(ctx, event)
}

type writerFunc func(context.Context, ...kafka.Message) error

func (f writerFunc) WriteMessages(ctx context.Context, messages ...kafka.Message) error {
	return f(ctx, messages...)
}

func unexpectedWriter(t *testing.T) MessageWriter {
	return writerFunc(func(context.Context, ...kafka.Message) error {
		t.Fatal("unexpected DLQ publication")
		return nil
	})
}

func TestConsumerRetriesBeforeFetchingNext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		message := kafka.Message{Topic: "order.created", Partition: 1, Offset: 12,
			Value: []byte(`{"event_id":"event","order_id":"order","buyer_id":"buyer","listing_id":"listing","qty":2,"created_at":"2026-10-13T09:00:00Z"}`),
		}
		var calls []string
		applies, commits := 0, 0
		reader := readerStub{
			fetch: func(context.Context) (kafka.Message, error) {
				calls = append(calls, "fetch")
				if len(calls) != 1 {
					t.Fatal("fetched next message before successful commit")
				}
				return message, nil
			},
			commit: func(commitCtx context.Context, messages ...kafka.Message) error {
				calls = append(calls, "commit")
				commits++
				if commitCtx.Err() != nil || len(messages) != 1 || !reflect.DeepEqual(messages[0], message) {
					t.Fatal("unexpected commit arguments")
				}
				if commits == 1 {
					return errors.New("broker unavailable")
				}
				cancel()
				return nil
			},
		}
		service := serviceFunc(func(processCtx context.Context, event dto.OrderCreatedEvent) (bool, error) {
			calls = append(calls, "apply")
			applies++
			deadline, ok := processCtx.Deadline()
			if !ok || time.Until(deadline) != 2*time.Second {
				t.Fatal("missing processing timeout")
			}
			want := dto.OrderCreatedEvent{EventID: "event", OrderID: "order", BuyerID: "buyer", ListingID: "listing", Qty: 2, CreatedAt: "2026-10-13T09:00:00Z"}
			if event != want {
				t.Fatalf("event = %+v, want %+v", event, want)
			}
			if applies == 1 {
				return false, errors.New("database unavailable")
			}
			return false, nil
		})
		NewConsumer(reader, unexpectedWriter(t), service).Start(ctx)
		want := []string{"fetch", "apply", "apply", "commit", "commit"}
		if !reflect.DeepEqual(calls, want) {
			t.Fatalf("calls = %v, want %v", calls, want)
		}
	})
}

func TestConsumerFinishesMessageOnShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	committed := false
	reader := readerStub{
		fetch: func(context.Context) (kafka.Message, error) {
			return kafka.Message{Value: []byte(`{"event_id":"event"}`)}, nil
		},
		commit: func(commitCtx context.Context, messages ...kafka.Message) error {
			if commitCtx.Err() != nil {
				t.Fatalf("commit canceled during shutdown: %v", commitCtx.Err())
			}
			committed = true
			return nil
		},
	}
	service := serviceFunc(func(processCtx context.Context, event dto.OrderCreatedEvent) (bool, error) {
		cancel()
		if processCtx.Err() != nil {
			t.Fatal("in-flight processing canceled")
		}
		return true, nil
	})
	NewConsumer(reader, unexpectedWriter(t), service).Start(ctx)
	if !committed {
		t.Fatal("processed message not committed on shutdown")
	}
}

func TestConsumerDoesNotCommitBeforeDLQPublication(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		writes := 0
		reader := readerStub{
			fetch: func(context.Context) (kafka.Message, error) {
				return kafka.Message{Value: []byte(`{"qty":`)}, nil
			},
			commit: func(context.Context, ...kafka.Message) error {
				t.Fatal("invalid message committed")
				return nil
			},
		}
		service := serviceFunc(func(context.Context, dto.OrderCreatedEvent) (bool, error) {
			t.Fatal("service called for invalid JSON")
			return false, nil
		})
		writer := writerFunc(func(context.Context, ...kafka.Message) error {
			writes++
			return errors.New("DLQ unavailable")
		})
		done := make(chan struct{})
		go func() {
			defer close(done)
			NewConsumer(reader, writer, service).Start(ctx)
		}()
		synctest.Wait()
		cancel()
		<-done
		if writes != 1 {
			t.Fatalf("DLQ writes = %d, want 1", writes)
		}
	})
}

func TestConsumerRecoversAfterFetchFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		fetches := 0
		reader := readerStub{
			fetch: func(context.Context) (kafka.Message, error) {
				fetches++
				if fetches == 1 {
					return kafka.Message{}, errors.New("broker unavailable")
				}
				return kafka.Message{Value: []byte(`{}`)}, nil
			},
			commit: func(context.Context, ...kafka.Message) error {
				cancel()
				return nil
			},
		}
		service := serviceFunc(func(context.Context, dto.OrderCreatedEvent) (bool, error) { return true, nil })
		NewConsumer(reader, unexpectedWriter(t), service).Start(ctx)
		if fetches != 2 {
			t.Fatalf("fetches = %d, want 2", fetches)
		}
	})
}

func TestConsumerDeadLettersPermanentErrors(t *testing.T) {
	validationErr := &service.ValidationError{Fields: []service.FieldError{{Field: "qty", Message: "must be positive"}}}
	tests := []struct {
		name    string
		payload string
		err     error
		reason  string
	}{
		{"invalid JSON", `{"qty":`, nil, "invalid_json"},
		{"wrong JSON type", `{"qty":"invalid"}`, nil, "invalid_json"},
		{"validation", `{"event_id":"bad","qty":-1}`, fmt.Errorf("apply: %w", validationErr), "validation_failed"},
		{"missing listing", `{"event_id":"bad"}`, fmt.Errorf("apply: %w", repository.ErrNotFound), "listing_not_found"},
		{"invalid quantity", `{"event_id":"bad"}`, fmt.Errorf("apply: %w", repository.ErrInvalidQuantity), "validation_failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				message := kafka.Message{
					Topic: "order.created", Partition: 1, Offset: 10,
					Key: []byte("order"), Value: []byte(tt.payload),
					Headers: []kafka.Header{{Key: "original-header", Value: []byte("original-value")}},
				}
				var calls []string
				fetches, writes, commits := 0, 0, 0
				reader := readerStub{
					fetch: func(context.Context) (kafka.Message, error) {
						calls = append(calls, "fetch")
						fetches++
						if fetches == 1 {
							return message, nil
						}
						if writes != 2 || commits != 2 {
							t.Fatal("next message fetched before DLQ and commit succeeded")
						}
						return kafka.Message{Offset: 11, Value: []byte(`{"event_id":"next"}`)}, nil
					},
					commit: func(commitCtx context.Context, messages ...kafka.Message) error {
						calls = append(calls, "commit")
						commits++
						if commitCtx.Err() != nil {
							t.Fatal("commit context canceled")
						}
						if commits <= 2 {
							if writes != 2 || len(messages) != 1 || !reflect.DeepEqual(messages[0], message) {
								t.Fatal("wrong source message committed")
							}
							if commits == 1 {
								return errors.New("commit failed")
							}
							return nil
						}
						cancel()
						return nil
					},
				}
				writer := writerFunc(func(publishCtx context.Context, messages ...kafka.Message) error {
					calls = append(calls, "dlq")
					writes++
					if publishCtx.Err() != nil || len(messages) != 1 {
						t.Fatal("unexpected DLQ arguments")
					}
					got := messages[0]
					if got.Topic != "order.created.dlq" || string(got.Key) != string(message.Key) || string(got.Value) != tt.payload {
						t.Fatalf("invalid DLQ message: %+v", got)
					}
					headers := map[string]string{}
					for _, header := range got.Headers {
						headers[header.Key] = string(header.Value)
					}
					if headers["dlq-source-topic"] != "order.created" || headers["dlq-source-partition"] != "1" || headers["dlq-source-offset"] != "10" || headers["dlq-reason"] != tt.reason || headers["dlq-error"] == "" || headers["original-header"] != "original-value" {
						t.Fatalf("invalid DLQ headers: %v", headers)
					}
					if tt.name == "validation" && headers["dlq-validation-fields"] == "" {
						t.Fatal("validation details missing")
					}
					if writes == 1 {
						return errors.New("DLQ unavailable")
					}
					return nil
				})
				service := serviceFunc(func(_ context.Context, event dto.OrderCreatedEvent) (bool, error) {
					calls = append(calls, "apply")
					if event.EventID == "next" {
						return true, nil
					}
					if tt.err == nil {
						t.Fatal("service called for malformed JSON")
					}
					return false, tt.err
				})
				NewConsumer(reader, writer, service).Start(ctx)
				want := []string{"fetch"}
				if tt.err != nil {
					want = append(want, "apply")
				}
				want = append(want, "dlq", "dlq", "commit", "commit", "fetch", "apply", "commit")
				if !reflect.DeepEqual(calls, want) {
					t.Fatalf("calls = %v, want %v", calls, want)
				}
				if len(message.Headers) != 1 {
					t.Fatal("original headers mutated")
				}
			})
		})
	}
}
