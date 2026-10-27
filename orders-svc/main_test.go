package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestMainProcess(t *testing.T) {
	if os.Getenv("ORDERS_SVC_TEST_MAIN") != "1" {
		return
	}
	main()
}

func TestMain_MissingConfiguration(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, missing := range []string{"DATABASE_URL", "KAFKA_BROKERS", "CATALOG_GRPC_ADDR"} {
		t.Run(missing, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestMainProcess$")
			for _, entry := range os.Environ() {
				name, _, _ := strings.Cut(entry, "=")
				switch strings.ToUpper(name) {
				case "DATABASE_URL", "KAFKA_BROKERS", "CATALOG_GRPC_ADDR", "ORDERS_SVC_TEST_MAIN":
					continue
				}
				cmd.Env = append(cmd.Env, entry)
			}
			cmd.Env = append(cmd.Env, "ORDERS_SVC_TEST_MAIN=1")
			for name, value := range map[string]string{
				"DATABASE_URL":      "postgres://app:app@localhost:5432/orders?sslmode=disable",
				"KAFKA_BROKERS":     "localhost:9092",
				"CATALOG_GRPC_ADDR": "localhost:50051",
			} {
				if name != missing {
					cmd.Env = append(cmd.Env, name+"="+value)
				}
			}

			output, err := cmd.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("process did not exit promptly: %v", ctx.Err())
			}
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
				t.Fatalf("process error = %v, want exit code 1; output: %s", err, output)
			}
			if !strings.Contains(string(output), "load config:") || !strings.Contains(string(output), missing+" is required") && !strings.Contains(string(output), missing+" are required") {
				t.Errorf("output does not identify missing %s: %s", missing, output)
			}
			if strings.Contains(string(output), "Application shutdown complete.") {
				t.Error("failed startup printed successful shutdown")
			}
		})
	}
}

func TestRun_InvalidDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://%")
	t.Setenv("KAFKA_BROKERS", "localhost:9092")
	t.Setenv("CATALOG_GRPC_ADDR", "localhost:50051")
	errChan := make(chan error, 1)
	err := run(context.Background(), errChan)
	if err == nil || !strings.HasPrefix(err.Error(), "unable to create pool:") {
		t.Fatalf("error = %v, want pool configuration error", err)
	}
	select {
	case serverErr := <-errChan:
		t.Errorf("gRPC server started with invalid database URL: %v", serverErr)
	default:
	}
}
