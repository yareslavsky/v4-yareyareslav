package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"marketplace/gateway/config"
	"marketplace/gateway/internal/auth"
	"marketplace/gateway/internal/healthz"
	"marketplace/gateway/internal/listings"
	"marketplace/gateway/internal/orders"
	"marketplace/gateway/pkg/cors"
	"marketplace/gateway/pkg/httplog"
	"marketplace/gateway/pkg/jwt"
	"marketplace/gateway/pkg/recoverer"
	"marketplace/gateway/pkg/requestid"
	"marketplace/gateway/pkg/response"
	"marketplace/gen/catalogpb"
	"marketplace/gen/orderspb"
	"marketplace/web"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi"
	"github.com/go-playground/validator"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func connectToDB(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second*3)
	defer cancel()

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("create database: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return pool, nil
}

func run(ctx context.Context) (runErr error) {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	pool, err := connectToDB(ctx, cfg.Database.URL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := runMigrations(cfg.Database.URL); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}

	r := chi.NewRouter()
	r.NotFound(response.NotFound)
	r.MethodNotAllowed(response.MethodNotAllowed)
	r.Use(requestid.Middleware)
	r.Use(httplog.Middleware(slog.Default()))
	r.Use(recoverer.Middleware(slog.Default()))
	r.Use(cors.Middleware)
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if _, err := w.Write(web.Index); err != nil {
			slog.ErrorContext(r.Context(), "write frontend response", "error", err)
		}
	})
	validate := validator.New()

	jwtManager := jwt.NewManager(cfg.JWT.Secret, jwt.AccessTTL, jwt.RefreshTTL)
	jwtMiddleware := jwt.NewJWTMiddleware(jwtManager)
	authConfig := config.NewAuthConfig(jwtMiddleware)

	authDBRepository := auth.NewAuthDBRepository(pool)
	authService := auth.NewAuthService(authDBRepository, jwtManager)
	auth.NewAuthHandler(r, validate, authService)

	catalogConn, err := grpc.NewClient(cfg.Catalog.Addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("create catalog client: %w", err)
	}
	defer func() {
		if err := catalogConn.Close(); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("close catalog client: %w", err))
		}
	}()
	listingsService := listings.NewListingsService(catalogpb.NewCatalogServiceClient(catalogConn))
	listings.NewListingsHandler(r, validate, authConfig, listingsService)

	ordersConn, err := grpc.NewClient(cfg.Orders.Addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("create orders client: %w", err)
	}
	defer func() {
		if err := ordersConn.Close(); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("close orders client: %w", err))
		}
	}()
	ordersService := orders.NewOrdersService(orderspb.NewOrdersServiceClient(ordersConn))
	orders.NewOrdersHandler(r, validate, authConfig, ordersService)
	healthz.NewHealthzHandler(r, healthpb.NewHealthClient(catalogConn), healthpb.NewHealthClient(ordersConn))

	server := http.Server{
		Addr:    cfg.HTTP.Addr,
		Handler: r,
	}
	listener, err := net.Listen("tcp", cfg.HTTP.Addr)
	if err != nil {
		return fmt.Errorf("listen HTTP: %w", err)
	}
	return serveHTTP(ctx, &server, listener)
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).Error("gateway failed", "error", err)
		stop()
		os.Exit(1)
	}
	log.Println("Application shutdown complete.")
}
