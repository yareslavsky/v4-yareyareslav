package config

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://app:app@localhost:5432/catalog?sslmode=disable")
	t.Setenv("KAFKA_BROKERS", "localhost:9092,localhost:9093")
	t.Setenv("REDIS_URL", "redis://localhost:6379/1")
}

func unsetEnv(t *testing.T, name string) {
	t.Helper()
	t.Setenv(name, "")
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
}

func TestLoad(t *testing.T) {
	for _, tc := range []struct {
		name, addr string
		unset      bool
	}{
		{name: "default address", addr: ":50051", unset: true},
		{name: "custom address", addr: "127.0.0.1:60051"},
		{name: "explicit empty address"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setRequiredEnv(t)
			if tc.unset {
				unsetEnv(t, "GRPC_ADDR")
			} else {
				t.Setenv("GRPC_ADDR", tc.addr)
			}
			got, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			want := &Config{
				GRPC:     GRPCConfig{Addr: tc.addr},
				Database: DatabaseConfig{URL: os.Getenv("DATABASE_URL")},
				Cache:    CacheConfig{URL: os.Getenv("REDIS_URL")},
				Kafka:    KafkaConfig{Brokers: []string{"localhost:9092", "localhost:9093"}},
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("config = %+v, want %+v", got, want)
			}
		})
	}
}

func TestLoadRequiredVariables(t *testing.T) {
	for _, name := range []string{"DATABASE_URL", "KAFKA_BROKERS", "REDIS_URL"} {
		for _, state := range []string{"unset", "empty"} {
			t.Run(name+"/"+state, func(t *testing.T) {
				setRequiredEnv(t)
				if state == "unset" {
					unsetEnv(t, name)
				} else {
					t.Setenv(name, "")
				}
				got, err := Load()
				if got != nil || err == nil || !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), "required") {
					t.Fatalf("config = %+v, error = %v, want missing %s", got, err, name)
				}
			})
		}
	}
}
