package main

import (
	"context"
	"errors"
	"fmt"
	"io"
)

func stopConsumer(ctx context.Context, done <-chan struct{}, reader, writer io.Closer) (bool, error) {
	select {
	case <-done:
	default:
		select {
		case <-done:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}

	closed := make(chan error, 2)
	go func() {
		err := reader.Close()
		if err != nil {
			err = fmt.Errorf("close Kafka reader: %w", err)
		}
		closed <- err
	}()
	go func() {
		err := writer.Close()
		if err != nil {
			err = fmt.Errorf("close Kafka DLQ writer: %w", err)
		}
		closed <- err
	}()

	var shutdownErr error
	for i := 0; i < 2; i++ {
		select {
		case err := <-closed:
			shutdownErr = errors.Join(shutdownErr, err)
		case <-ctx.Done():
			return true, errors.Join(shutdownErr, ctx.Err())
		}
	}
	return true, shutdownErr
}
