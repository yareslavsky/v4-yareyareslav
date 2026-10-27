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

type CacheConfig struct {
	URL string
}

type KafkaConfig struct {
	Brokers []string
}

type Config struct {
	GRPC     GRPCConfig
	Database DatabaseConfig
	Cache    CacheConfig
	Kafka    KafkaConfig
}

func Load() (*Config, error) {
	grpcAddr := getEnv("GRPC_ADDR", ":50051")

	databaseUrl, ok := os.LookupEnv("DATABASE_URL")
	if !ok || databaseUrl == "" {
		return nil, fmt.Errorf("DATABASE_URL is required. Value: %s", databaseUrl)
	}

	kafkaBrokersEnv, ok := os.LookupEnv("KAFKA_BROKERS")
	if !ok || kafkaBrokersEnv == "" {
		return nil, fmt.Errorf("KAFKA_BROKERS are required. Value: %s", kafkaBrokersEnv)
	}
	kafkaBrokers := strings.Split(kafkaBrokersEnv, ",")

	redisUrl, ok := os.LookupEnv("REDIS_URL")
	if !ok || redisUrl == "" {
		return nil, fmt.Errorf("REDIS_URL is required. Value: %s", redisUrl)
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
		Cache: CacheConfig{
			URL: redisUrl,
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
