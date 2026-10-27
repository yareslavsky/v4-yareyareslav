package handler

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func LoggingInterceptor(
	ctx context.Context,
	req any,
	info *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler,
) (any, error) {
	started := time.Now()
	response, err := handler(ctx, req)
	code := status.Code(err)
	requestID := ""
	if incoming, ok := metadata.FromIncomingContext(ctx); ok {
		if values := incoming.Get("x-request-id"); len(values) > 0 {
			requestID = values[0]
		}
	}
	level := slog.LevelInfo
	if code == codes.Internal || code == codes.Unknown {
		level = slog.LevelError
	}
	fields := []any{
		"request_id", requestID,
		"method", info.FullMethod,
		"code", code.String(),
		"dur_ms", time.Since(started).Milliseconds(),
	}
	if err != nil {
		fields = append(fields, "error", err)
	}
	slog.Log(ctx, level, "gRPC request completed", fields...)
	return response, err
}
