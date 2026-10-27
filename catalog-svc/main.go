package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"marketplace/catalog-svc/config"
	"marketplace/catalog-svc/internal/listings/handler"
	kfk "marketplace/catalog-svc/internal/listings/kafka"
	"marketplace/catalog-svc/internal/listings/repository"
	"marketplace/catalog-svc/internal/listings/service"
	"marketplace/catalog-svc/internal/listings/service/adapter"
	"marketplace/gen/catalogpb"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/segmentio/kafka-go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

func run(ctx context.Context, errChan chan error) (runErr error) {
	workerStopped := true
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// DATABASE
	pool, err := pgxpool.New(ctx, cfg.Database.URL)
	if err != nil {
		return fmt.Errorf("unable to create pool: %v", err)
	}
	defer func() {
		if workerStopped {
			pool.Close()
		}
	}()

	source, err := iofs.New(migrationFiles, "migrations")
	if err != nil {
		return fmt.Errorf("unable to open embedded migrations: %v", err)
	}

	m, err := migrate.NewWithSourceInstance("iofs", source, cfg.Database.URL)
	if err != nil {
		source.Close()
		return fmt.Errorf("migrations failed to initialize: %v", err)
	}
	defer m.Close()

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("failed to apply migrations: %v", err)
	}

	// REDIS
	parsedRedisUrl, err := redis.ParseURL(cfg.Cache.URL)
	if err != nil {
		return fmt.Errorf("redis url parse: %w", err)
	}
	parsedRedisUrl.MaxRetries = -1
	parsedRedisUrl.DialerRetries = 1
	parsedRedisUrl.ContextTimeoutEnabled = true
	parsedRedisUrl.DialTimeout = 200 * time.Millisecond
	parsedRedisUrl.ReadTimeout = 200 * time.Millisecond
	parsedRedisUrl.WriteTimeout = 200 * time.Millisecond
	parsedRedisUrl.PoolTimeout = 200 * time.Millisecond

	rdb := redis.NewClient(parsedRedisUrl)
	defer func() {
		if workerStopped {
			rdb.Close()
		}
	}()

	pingCtx, cancelPing := context.WithTimeout(ctx, time.Millisecond*200)
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		slog.WarnContext(ctx, "Error starting redis", "error", err.Error())
	}
	cancelPing()

	// SERVICE
	listingsPGRepository := repository.NewPGRepository(pool)

	adaptedRdb := adapter.NewRedisAdapter(rdb)
	listingsService := service.NewCatalogService(listingsPGRepository, adaptedRdb)

	listingsGRPCHandler := handler.NewGRPCHandler(listingsService)

	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:                cfg.Kafka.Brokers,
		GroupID:                "catalog-svc",
		Topic:                  "order.created",
		CommitInterval:         0,
		StartOffset:            kafka.FirstOffset,
		MinBytes:               1,
		MaxBytes:               10 * 1024 * 1024,
		WatchPartitionChanges:  true,
		PartitionWatchInterval: time.Second,
	})
	dlqWriter := &kafka.Writer{
		Addr:                   kafka.TCP(cfg.Kafka.Brokers...),
		Balancer:               &kafka.Hash{},
		RequiredAcks:           kafka.RequireAll,
		Async:                  false,
		BatchSize:              1,
		ReadTimeout:            2 * time.Second,
		WriteTimeout:           2 * time.Second,
		AllowAutoTopicCreation: true,
	}
	consumer := kfk.NewConsumer(reader, dlqWriter, listingsService)
	consumerCtx, cancelConsumer := context.WithCancel(context.Background())
	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		consumer.Start(consumerCtx)
	}()
	defer func() {
		cancelConsumer()
		stopCtx, cancelStop := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelStop()
		var err error
		workerStopped, err = stopConsumer(stopCtx, consumerDone, reader, dlqWriter)
		if err != nil {
			slog.Error("Kafka consumer shutdown failed", "error", err)
			runErr = errors.Join(runErr, fmt.Errorf("stop Kafka consumer: %w", err))
		}
	}()

	// GRPC
	listener, err := net.Listen("tcp", cfg.GRPC.Addr)
	if err != nil {
		return fmt.Errorf("grpc start: %w", err)
	}
	defer listener.Close()

	server := grpc.NewServer(grpc.UnaryInterceptor(handler.LoggingInterceptor))
	catalogpb.RegisterCatalogServiceServer(server, listingsGRPCHandler)
	log.Printf("gRPC server listening on %s", cfg.GRPC.Addr)

	healthServer := health.NewServer()
	healthpb.RegisterHealthServer(server, healthServer)

	healthServer.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthServer.SetServingStatus(
		catalogpb.CatalogService_ServiceDesc.ServiceName,
		healthpb.HealthCheckResponse_SERVING,
	)

	go func() {
		errChan <- server.Serve(listener)
	}()

	select {
	case <-ctx.Done():
		log.Println("shutdown started")
	case err := <-errChan:
		healthServer.Shutdown()
		server.Stop()
		if err == nil {
			return errors.New("gRPC server stopped unexpectedly")
		}
		return fmt.Errorf("gRPC server failed: %v", err)
	}

	grpcStopped := make(chan struct{})
	go func() {
		healthServer.Shutdown()
		server.GracefulStop()
		close(grpcStopped)
	}()

	var shutdownErr error
	grpcTimer := time.NewTimer(10 * time.Second)

	select {
	case <-grpcStopped:
	case <-grpcTimer.C:
		log.Println("ERROR: gRPC shutdown timed out")
		shutdownErr = errors.New("gRPC shutdown timed out")
		server.Stop()
		<-grpcStopped
	}
	grpcTimer.Stop()

	return shutdownErr
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	ctx, stop := signal.NotifyContext(
		context.Background(),
		syscall.SIGINT,
		syscall.SIGTERM,
	)
	defer stop()
	errChan := make(chan error, 1)
	if err := run(ctx, errChan); err != nil {
		log.Printf("ERROR: catalog service failed: %v", err)
		stop()
		os.Exit(1)
	}

	fmt.Println("Application shutdown complete.")
}
