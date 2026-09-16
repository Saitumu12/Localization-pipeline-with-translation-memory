// Package config reads the handful of settings both binaries need.
package config

import "os"

// EmbeddingDimensions must match the vector column in the migration and the
// model the embedding service serves.
const EmbeddingDimensions = 384

type Config struct {
	Addr             string
	DatabaseURL      string
	EmbeddingsURL    string
	AnthropicKey     string
	AnthropicBaseURL string
	LLMModel         string
	WebDir           string
}

func FromEnv() Config {
	return Config{
		Addr:             env("ADDR", ":8080"),
		DatabaseURL:      env("DATABASE_URL", "postgres://loc:locpass@localhost:5433/loc?sslmode=disable"),
		EmbeddingsURL:    env("EMBEDDINGS_URL", "http://localhost:8081"),
		AnthropicKey:     os.Getenv("ANTHROPIC_API_KEY"),
		AnthropicBaseURL: os.Getenv("ANTHROPIC_BASE_URL"),
		LLMModel:         os.Getenv("LLM_MODEL"),
		WebDir:           env("WEB_DIR", "web/dist"),
	}
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
