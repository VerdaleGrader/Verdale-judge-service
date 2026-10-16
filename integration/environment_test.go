//go:build integration

package integration

import (
	"os"
	"strconv"
)

func hostname() string {
	name, _ := os.Hostname()
	return name
}

func environmentText(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func environmentNumber(key string, fallback int) int {
	if value, err := strconv.Atoi(os.Getenv(key)); err == nil && value > 0 {
		return value
	}
	return fallback
}
