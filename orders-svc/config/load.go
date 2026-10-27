package config

import (
	"fmt"
	"os"
	"strings"
)

type GRPCConfig struct {
	Addr string
}

type DatabaseConfig struct {
	URL string
}

type KafkaConfig struct {
	Brokers []string
}

type CatalogConfig struct {
	Addr string
}

type Config struct {
	GRPC     GRPCConfig
	Database DatabaseConfig
	Kafka    KafkaConfig
	Catalog  CatalogConfig
}

func Load() (*Config, error) {
	grpcAddr := getEnv("GRPC_ADDR", ":50052")

	databaseUrl, ok := os.LookupEnv("DATABASE_URL")
	if !ok || databaseUrl == "" {
		return nil, fmt.Errorf("DATABASE_URL is required. Value: %s", databaseUrl)
	}

	kafkaBrokersEnv, ok := os.LookupEnv("KAFKA_BROKERS")
	if !ok || kafkaBrokersEnv == "" {
		return nil, fmt.Errorf("KAFKA_BROKERS are required. Value: %s", kafkaBrokersEnv)
	}
	kafkaBrokers := strings.Split(kafkaBrokersEnv, ",")

	catalogAddr, ok := os.LookupEnv("CATALOG_GRPC_ADDR")
	if !ok || catalogAddr == "" {
		return nil, fmt.Errorf("CATALOG_GRPC_ADDR is required. Value: %s", catalogAddr)
	}

	cfg := &Config{
		GRPC: GRPCConfig{
			Addr: grpcAddr,
		},
		Database: DatabaseConfig{
			URL: databaseUrl,
		},
		Kafka: KafkaConfig{
			Brokers: kafkaBrokers,
		},
		Catalog: CatalogConfig{
			Addr: catalogAddr,
		},
	}

	return cfg, nil
}

func getEnv(env, fallback string) string {
	if value, ok := os.LookupEnv(env); ok {
		return value
	}
	return fallback
}
