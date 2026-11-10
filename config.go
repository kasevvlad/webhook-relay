package main

import (
	"os"
	"strconv"
)

type Config struct {
	Addr        string
	DatabaseURL string
	Workers     int
	MaxAttempts int
}

func loadConfig() Config {
	return Config{
		Addr:        getEnv("ADDR", ":8080"),
		DatabaseURL: getEnv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/webhooks?sslmode=disable"),
		Workers:     getEnvInt("WORKERS", 4),
		MaxAttempts: getEnvInt("MAX_ATTEMPTS", 5),
	}
}

func getEnv(name string, defaultValue string) string {
	value := os.Getenv(name)
	if value == "" {
		return defaultValue
	}
	return value
}

func getEnvInt(name string, defaultValue int) int {
	value, err := strconv.Atoi(os.Getenv(name))
	if err != nil || value <= 0 {
		return defaultValue
	}
	return value
}
