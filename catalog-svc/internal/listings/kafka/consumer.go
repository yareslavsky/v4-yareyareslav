package kafka

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"marketplace/catalog-svc/internal/listings/dto"
	"marketplace/catalog-svc/internal/listings/repository"
	"marketplace/catalog-svc/internal/listings/service"
	"strconv"
	"time"

	"github.com/segmentio/kafka-go"
)

type MessageReader interface {
	FetchMessage(context.Context) (kafka.Message, error)
	CommitMessages(context.Context, ...kafka.Message) error
}

type OrderCreatedService interface {
	ApplyOrderCreated(context.Context, dto.OrderCreatedEvent) (bool, error)
}

type MessageWriter interface {
	WriteMessages(context.Context, ...kafka.Message) error
}

type KafkaConsumer struct {
	reader  MessageReader
	writer  MessageWriter
	service OrderCreatedService
}

func NewConsumer(reader MessageReader, writer MessageWriter, service OrderCreatedService) *KafkaConsumer {
	return &KafkaConsumer{
		reader:  reader,
		writer:  writer,
		service: service,
	}
}

func (c *KafkaConsumer) Start(ctx context.Context) {
	for ctx.Err() == nil {
		message, err := c.reader.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.WarnContext(ctx, "Kafka fetch failed", "error", err)
			if !waitRetry(ctx) {
				return
			}
			continue
		}

		if !c.apply(ctx, message) || !c.commit(ctx, message) {
			return
		}
	}
}

func (c *KafkaConsumer) apply(ctx context.Context, message kafka.Message) bool {
	for {
		var event dto.OrderCreatedEvent
		err := json.Unmarshal(message.Value, &event)
		reason := "invalid_json"
		if err == nil {
			processCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			_, err = c.service.ApplyOrderCreated(processCtx, event)
			cancel()
			reason = permanentErrorReason(err)
		}
		if err == nil {
			return true
		}
		if reason != "" {
			return c.deadLetter(ctx, message, reason, err)
		}

		slog.WarnContext(ctx, "order.created processing failed",
			"partition", message.Partition, "offset", message.Offset, "error", err,
		)
		if !waitRetry(ctx) {
			return false
		}
	}
}

func permanentErrorReason(err error) string {
	switch {
	case errors.Is(err, service.ErrValidation), errors.Is(err, repository.ErrInvalidQuantity):
		return "validation_failed"
	case errors.Is(err, repository.ErrNotFound):
		return "listing_not_found"
	default:
		return ""
	}
}

func (c *KafkaConsumer) deadLetter(ctx context.Context, message kafka.Message, reason string, processingErr error) bool {
	headers := append([]kafka.Header(nil), message.Headers...)
	headers = append(headers,
		kafka.Header{Key: "dlq-source-topic", Value: []byte(message.Topic)},
		kafka.Header{Key: "dlq-source-partition", Value: []byte(strconv.Itoa(message.Partition))},
		kafka.Header{Key: "dlq-source-offset", Value: []byte(strconv.FormatInt(message.Offset, 10))},
		kafka.Header{Key: "dlq-reason", Value: []byte(reason)},
		kafka.Header{Key: "dlq-error", Value: []byte(processingErr.Error())},
	)
	var validationErr *service.ValidationError
	if errors.As(processingErr, &validationErr) {
		fields, err := json.Marshal(validationErr.Fields)
		if err == nil {
			headers = append(headers, kafka.Header{Key: "dlq-validation-fields", Value: fields})
		}
	}
	deadLetter := kafka.Message{
		Topic:   "order.created.dlq",
		Key:     message.Key,
		Value:   message.Value,
		Headers: headers,
	}
	for {
		publishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		err := c.writer.WriteMessages(publishCtx, deadLetter)
		cancel()
		if err == nil {
			slog.WarnContext(ctx, "order.created sent to DLQ",
				"partition", message.Partition, "offset", message.Offset, "reason", reason, "error", processingErr,
			)
			return true
		}
		slog.WarnContext(ctx, "Kafka DLQ publication failed",
			"partition", message.Partition, "offset", message.Offset, "error", err,
		)
		if !waitRetry(ctx) {
			return false
		}
	}
}

func (c *KafkaConsumer) commit(ctx context.Context, message kafka.Message) bool {
	for {
		commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		err := c.reader.CommitMessages(commitCtx, message)
		cancel()
		if err == nil {
			return true
		}

		slog.WarnContext(ctx, "Kafka commit failed",
			"partition", message.Partition, "offset", message.Offset, "error", err,
		)
		if !waitRetry(ctx) {
			return false
		}
	}
}

func waitRetry(ctx context.Context) bool {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return ctx.Err() == nil
	}
}
