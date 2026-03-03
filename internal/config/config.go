package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Port                   string
	DatabasePath           string
	OllamaURL              string
	OllamaModel            string
	PostMaxChars           int
	PostCooldown           time.Duration
	PostRateLimitCount     int
	PostRateLimitWindow    time.Duration
	RateLimitHashRotation  time.Duration
	ClassifierTimeout      time.Duration
	ClassifierPollInterval time.Duration
	ClassifierMaxRetries   int
	IPHashSalt             string
	FormTokenSecret        string
	FormTokenMinAge        time.Duration
	FormTokenMaxAge        time.Duration
	PublicSourceURL        string
	AppEnv                 string
	FeedLimit              int
}

func Load() (Config, error) {
	cfg := Config{
		Port:                   getEnv("PORT", "8080"),
		DatabasePath:           getEnv("DATABASE_PATH", "./data/ventboard.db"),
		OllamaURL:              strings.TrimRight(getEnv("OLLAMA_URL", "http://127.0.0.1:11434"), "/"),
		OllamaModel:            getEnv("OLLAMA_MODEL", "gemma3:12b"),
		PostMaxChars:           getEnvInt("POST_MAX_CHARS", 2000),
		PostCooldown:           time.Duration(getEnvInt("POST_COOLDOWN_SECONDS", 60)) * time.Second,
		PostRateLimitCount:     getEnvInt("POST_RATE_LIMIT_COUNT", 5),
		PostRateLimitWindow:    time.Duration(getEnvInt("POST_RATE_LIMIT_WINDOW_MINUTES", 15)) * time.Minute,
		RateLimitHashRotation:  time.Duration(getEnvInt("RATE_LIMIT_HASH_ROTATION_MINUTES", 60)) * time.Minute,
		ClassifierTimeout:      time.Duration(getEnvInt("CLASSIFIER_TIMEOUT_SECONDS", 20)) * time.Second,
		ClassifierPollInterval: time.Duration(getEnvInt("CLASSIFIER_POLL_INTERVAL_SECONDS", 3)) * time.Second,
		ClassifierMaxRetries:   getEnvInt("CLASSIFIER_MAX_RETRIES", 5),
		IPHashSalt:             getEnv("IP_HASH_SALT", ""),
		FormTokenSecret:        getEnv("FORM_TOKEN_SECRET", ""),
		FormTokenMinAge:        time.Duration(getEnvInt("FORM_TOKEN_MIN_AGE_SECONDS", 3)) * time.Second,
		FormTokenMaxAge:        time.Duration(getEnvInt("FORM_TOKEN_MAX_AGE_MINUTES", 120)) * time.Minute,
		PublicSourceURL:        strings.TrimSpace(getEnv("PUBLIC_SOURCE_URL", "")),
		AppEnv:                 strings.ToLower(getEnv("APP_ENV", "dev")),
		FeedLimit:              getEnvInt("FEED_LIMIT", 100),
	}

	if cfg.Port == "" {
		return Config{}, fmt.Errorf("PORT cannot be empty")
	}
	if cfg.DatabasePath == "" {
		return Config{}, fmt.Errorf("DATABASE_PATH cannot be empty")
	}
	if cfg.PostMaxChars <= 0 {
		return Config{}, fmt.Errorf("POST_MAX_CHARS must be positive")
	}
	if cfg.PostRateLimitCount <= 0 {
		return Config{}, fmt.Errorf("POST_RATE_LIMIT_COUNT must be positive")
	}
	if cfg.RateLimitHashRotation <= 0 {
		return Config{}, fmt.Errorf("RATE_LIMIT_HASH_ROTATION_MINUTES must be positive")
	}
	if cfg.ClassifierMaxRetries <= 0 {
		return Config{}, fmt.Errorf("CLASSIFIER_MAX_RETRIES must be positive")
	}
	if cfg.FormTokenMinAge < 0 {
		return Config{}, fmt.Errorf("FORM_TOKEN_MIN_AGE_SECONDS cannot be negative")
	}
	if cfg.FormTokenMaxAge <= 0 {
		return Config{}, fmt.Errorf("FORM_TOKEN_MAX_AGE_MINUTES must be positive")
	}
	if cfg.FormTokenMaxAge <= cfg.FormTokenMinAge {
		return Config{}, fmt.Errorf("FORM_TOKEN_MAX_AGE_MINUTES must be greater than FORM_TOKEN_MIN_AGE_SECONDS")
	}
	if cfg.FeedLimit <= 0 {
		return Config{}, fmt.Errorf("FEED_LIMIT must be positive")
	}

	if cfg.IPHashSalt == "" {
		if cfg.AppEnv != "dev" {
			return Config{}, fmt.Errorf("IP_HASH_SALT is required when APP_ENV is not dev")
		}
		cfg.IPHashSalt = "dev-salt-change-me"
	}
	if cfg.FormTokenSecret == "" {
		cfg.FormTokenSecret = cfg.IPHashSalt
	}

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}

	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return value
}
