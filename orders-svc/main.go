package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"marketplace/gen/catalogpb"
	"marketplace/gen/orderspb"
	"marketplace/orders-svc/config"
	"marketplace/orders-svc/internal/catalog"
	"marketplace/orders-svc/internal/orders/handler"
	kfk "marketplace/orders-svc/internal/orders/kafka"
	"marketplace/orders-svc/internal/orders/repository"
	"marketplace/orders-svc/internal/orders/service"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/segmentio/kafka-go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
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

	// SERVICE
	writer := &kafka.Writer{
		Addr:         kafka.TCP(cfg.Kafka.Brokers...),
		Balancer:     &kafka.Hash{},
		RequiredAcks: kafka.RequireAll,
		Async:        false,
	}
	defer func() {
		if workerStopped {
			if err := writer.Close(); err != nil {
				runErr = errors.Join(runErr, fmt.Errorf("close Kafka writer: %w", err))
			}
		}
	}()

	ordersRepository := repository.NewPGRepository(pool, writer)
	conn, err := grpc.NewClient(
		cfg.Catalog.Addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return fmt.Errorf("create catalog client: %w", err)
	}
	defer conn.Close()

	kafkaWorkerCtx, cancelWorker := context.WithCancel(context.Background())
	workerDone := make(chan struct{})
	defer func() {
		cancelWorker()
		stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := waitWorker(stopCtx, workerDone); err != nil {
			workerStopped = false
			slog.Error("outbox worker shutdown timed out", "error", err)
			runErr = errors.Join(runErr, fmt.Errorf("stop outbox worker: %w", err))
		}
	}()

	kafkaWorker := kfk.NewWorker(ordersRepository)

	catalogClient := catalog.NewClient(catalogpb.NewCatalogServiceClient(conn))
	go func() {
		defer close(workerDone)
		var workers sync.WaitGroup
		workers.Go(func() { kafkaWorker.Start(kafkaWorkerCtx) })
		workers.Go(func() { runCompensations(kafkaWorkerCtx, ordersRepository, catalogClient.ReleaseReservation) })
		workers.Wait()
	}()
	ordersService := service.NewOrdersService(ordersRepository, catalogClient)

	// GRPC
	listener, err := net.Listen("tcp", cfg.GRPC.Addr)
	if err != nil {
		return fmt.Errorf("unable to listen grpc: %v", err)
	}
	defer listener.Close()

	server := grpc.NewServer(grpc.UnaryInterceptor(handler.LoggingInterceptor))

	ordersServer := handler.NewOrdersServer(ordersService)
	orderspb.RegisterOrdersServiceServer(server, ordersServer)
	log.Printf("gRPC server listening on %s", cfg.GRPC.Addr)

	healthServer := health.NewServer()
	healthpb.RegisterHealthServer(server, healthServer)

	healthServer.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthServer.SetServingStatus(
		orderspb.OrdersService_ServiceDesc.ServiceName,
		healthpb.HealthCheckResponse_SERVING,
	)

	go func() {
		errChan <- server.Serve(listener)
	}()

	// SHUTDOWN
	select {
	case <-ctx.Done():
		log.Println("shutdown started")
	case err := <-errChan:
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

	log.Println("allowing outbox worker to publish remaining events")

	drainTimer := time.NewTimer(5 * time.Second)
	<-drainTimer.C

	return shutdownErr
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	errChan := make(chan error, 1)
	ctx, stop := signal.NotifyContext(
		context.Background(),
		syscall.SIGINT,
		syscall.SIGTERM,
	)
	defer stop()

	if err := run(ctx, errChan); err != nil {
		log.Printf("ERROR: orders service failed: %v", err)
		stop()
		os.Exit(1)
	}

	fmt.Println("Application shutdown complete.")
}
