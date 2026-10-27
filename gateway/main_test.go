package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestGatewayMissingEnvironmentReportsStderr(t *testing.T) {
	if os.Getenv("GATEWAY_TEST_MAIN") == "1" {
		main()
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	required := []string{"DATABASE_URL", "JWT_SECRET", "CATALOG_GRPC_ADDR", "ORDERS_GRPC_ADDR"}
	for _, missing := range required {
		t.Run(missing, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, executable, "-test.run=^TestGatewayMissingEnvironmentReportsStderr$")
			command.Dir = t.TempDir()
			command.Env = append(os.Environ(), "GATEWAY_TEST_MAIN=1")
			for _, name := range required {
				value := "test-value"
				if name == missing {
					value = ""
				}
				command.Env = append(command.Env, name+"="+value)
			}
			var stdout, stderr bytes.Buffer
			command.Stdout = &stdout
			command.Stderr = &stderr
			err := command.Run()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
				t.Fatalf("error %v, stdout %s, stderr %s", err, stdout.String(), stderr.String())
			}
			if stdout.Len() != 0 {
				t.Errorf("startup error written to stdout: %s", stdout.String())
			}
			var record map[string]any
			if err := json.Unmarshal(stderr.Bytes(), &record); err != nil {
				t.Fatalf("stderr is not JSON: %s", stderr.String())
			}
			message, _ := record["error"].(string)
			if record["level"] != "ERROR" || !strings.Contains(message, missing) {
				t.Errorf("stderr record: %v", record)
			}
		})
	}
}
