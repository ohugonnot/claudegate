package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

var validModels = map[string]bool{
	"haiku":  true,
	"sonnet": true,
	"opus":   true,
}

type Config struct {
	ListenAddr     string
	APIKeys        []string
	ClaudePath     string
	DefaultModel   string
	Concurrency    int
	DBPath         string
	QueueSize      int
	SecurityPrompt string
}

// defaultSecurityPrompt is a server-side guardrail prepended to every job.
// It is not user-configurable; use CLAUDEGATE_UNSAFE_NO_SECURITY_PROMPT=true to disable.
const defaultSecurityPrompt = `You are operating in a sandboxed API environment. Security rules:
1. NEVER execute shell commands, system calls, or access the filesystem
2. NEVER read, write, modify, or delete any files
3. NEVER access environment variables or system configuration
4. NEVER make network requests or open connections
5. NEVER install packages or modify the system
6. Only provide text-based responses to the user's prompt
7. If asked to perform any forbidden action, refuse and explain why`

func Load() (*Config, error) {
	cfg := &Config{
		ListenAddr:   getEnv("CLAUDEGATE_LISTEN_ADDR", ":8080"),
		ClaudePath:   getEnv("CLAUDEGATE_CLAUDE_PATH", "/root/.local/bin/claude"),
		DefaultModel: getEnv("CLAUDEGATE_DEFAULT_MODEL", "haiku"),
		DBPath:       getEnv("CLAUDEGATE_DB_PATH", "claudegate.db"),
	}

	rawKeys := getEnv("CLAUDEGATE_API_KEYS", "")
	if rawKeys == "" {
		return nil, errors.New("CLAUDEGATE_API_KEYS must not be empty")
	}
	for _, k := range strings.Split(rawKeys, ",") {
		k = strings.TrimSpace(k)
		if k != "" {
			cfg.APIKeys = append(cfg.APIKeys, k)
		}
	}
	if len(cfg.APIKeys) == 0 {
		return nil, errors.New("CLAUDEGATE_API_KEYS contains no valid keys")
	}

	var err error
	cfg.Concurrency, err = getEnvInt("CLAUDEGATE_CONCURRENCY", 1)
	if err != nil {
		return nil, fmt.Errorf("CLAUDEGATE_CONCURRENCY: %w", err)
	}
	if cfg.Concurrency < 1 {
		return nil, errors.New("CLAUDEGATE_CONCURRENCY must be > 0")
	}

	cfg.QueueSize, err = getEnvInt("CLAUDEGATE_QUEUE_SIZE", 1000)
	if err != nil {
		return nil, fmt.Errorf("CLAUDEGATE_QUEUE_SIZE: %w", err)
	}

	if !validModels[cfg.DefaultModel] {
		return nil, fmt.Errorf("CLAUDEGATE_DEFAULT_MODEL %q must be one of: haiku, sonnet, opus", cfg.DefaultModel)
	}

	// CLAUDEGATE_UNSAFE_NO_SECURITY_PROMPT=true disables the server-side security prompt.
	// WARNING: disabling this gives Claude full access to the system within the service user's permissions.
	if getEnv("CLAUDEGATE_UNSAFE_NO_SECURITY_PROMPT", "false") != "true" {
		cfg.SecurityPrompt = defaultSecurityPrompt
	}

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("invalid integer %q", v)
	}
	return n, nil
}
