// Package config loads and validates all application settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds every setting the application needs. All values are populated
// from environment variables; missing required vars cause a startup failure.
type Config struct {
	// Server
	ServerPort string // SERVER_PORT — e.g. "8080"

	// MongoDB
	MongoURI string // MONGO_URI — e.g. "mongodb://mongo:27017"
	MongoDB  string // MONGO_DB  — database name

	// Redis
	RedisAddr     string // REDIS_ADDR — e.g. "redis:6379"
	RedisPassword string // REDIS_PASSWORD — empty string if none

	// JWT
	JWTSecret string        // JWT_SECRET — signing key for tokens
	JWTExpiry time.Duration // JWT_EXPIRY_HOURS — default 24h

	// Google OAuth 2.0
	GoogleClientID     string // GOOGLE_CLIENT_ID
	GoogleClientSecret string // GOOGLE_CLIENT_SECRET
	GoogleRedirectURL  string // GOOGLE_REDIRECT_URL

	// Authorization
	AdminEmails []string // ADMIN_EMAILS — comma-separated list of admin addresses

	// Seat locking
	LockTTL time.Duration // LOCK_TTL_SECONDS — how long a seat hold lasts

	// Development / demo
	SeedOnStart bool // SEED_ON_START — if "true", run the idempotent seed on startup
	DevAuth     bool // DEV_AUTH — if "true", enable POST /dev/token (never set in production)
}

// Load reads all settings from the environment and returns a validated Config.
// It collects every missing-var error before returning so operators see all
// problems at once rather than fixing them one-by-one.
func Load() (*Config, error) {
	var errs []string

	required := func(key string) string {
		v := os.Getenv(key)
		if v == "" {
			errs = append(errs, fmt.Sprintf("required env var %q is not set", key))
		}
		return v
	}

	optional := func(key, def string) string {
		if v := os.Getenv(key); v != "" {
			return v
		}
		return def
	}

	cfg := &Config{
		ServerPort:         optional("SERVER_PORT", "8080"),
		MongoURI:           required("MONGO_URI"),
		MongoDB:            optional("MONGO_DB", "cinema"),
		RedisAddr:          required("REDIS_ADDR"),
		RedisPassword:      optional("REDIS_PASSWORD", ""),
		JWTSecret:          required("JWT_SECRET"),
		GoogleClientID:     required("GOOGLE_CLIENT_ID"),
		GoogleClientSecret: required("GOOGLE_CLIENT_SECRET"),
		GoogleRedirectURL:  required("GOOGLE_REDIRECT_URL"),
	}

	// ADMIN_EMAILS — comma-separated
	adminRaw := required("ADMIN_EMAILS")
	if adminRaw != "" {
		for _, e := range strings.Split(adminRaw, ",") {
			if trimmed := strings.TrimSpace(e); trimmed != "" {
				cfg.AdminEmails = append(cfg.AdminEmails, trimmed)
			}
		}
	}

	// LOCK_TTL_SECONDS — default 300 (5 minutes)
	lockSec, err := strconv.Atoi(optional("LOCK_TTL_SECONDS", "300"))
	if err != nil || lockSec <= 0 {
		errs = append(errs, "LOCK_TTL_SECONDS must be a positive integer (got: "+os.Getenv("LOCK_TTL_SECONDS")+")")
	} else {
		cfg.LockTTL = time.Duration(lockSec) * time.Second
	}

	// JWT_EXPIRY_HOURS — default 24
	jwtHours, err := strconv.Atoi(optional("JWT_EXPIRY_HOURS", "24"))
	if err != nil || jwtHours <= 0 {
		errs = append(errs, "JWT_EXPIRY_HOURS must be a positive integer")
	} else {
		cfg.JWTExpiry = time.Duration(jwtHours) * time.Hour
	}

	cfg.SeedOnStart = optional("SEED_ON_START", "false") == "true"
	cfg.DevAuth = optional("DEV_AUTH", "false") == "true"

	if len(errs) > 0 {
		return nil, errors.New("configuration errors:\n  - " + strings.Join(errs, "\n  - "))
	}
	return cfg, nil
}
