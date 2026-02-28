package config

import (
	"testing"
)

func TestLoad_AllVarsSet(t *testing.T) {
	t.Setenv("CLAUDEGATE_API_KEYS", "key1,key2")
	t.Setenv("CLAUDEGATE_LISTEN_ADDR", ":9090")
	t.Setenv("CLAUDEGATE_CLAUDE_PATH", "/usr/bin/claude")
	t.Setenv("CLAUDEGATE_DEFAULT_MODEL", "sonnet")
	t.Setenv("CLAUDEGATE_CONCURRENCY", "4")
	t.Setenv("CLAUDEGATE_DB_PATH", "/tmp/test.db")
	t.Setenv("CLAUDEGATE_QUEUE_SIZE", "500")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if cfg.ListenAddr != ":9090" {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, ":9090")
	}
	if len(cfg.APIKeys) != 2 {
		t.Errorf("APIKeys len = %d, want 2", len(cfg.APIKeys))
	}
	if cfg.APIKeys[0] != "key1" || cfg.APIKeys[1] != "key2" {
		t.Errorf("APIKeys = %v, want [key1 key2]", cfg.APIKeys)
	}
	if cfg.ClaudePath != "/usr/bin/claude" {
		t.Errorf("ClaudePath = %q, want %q", cfg.ClaudePath, "/usr/bin/claude")
	}
	if cfg.DefaultModel != "sonnet" {
		t.Errorf("DefaultModel = %q, want %q", cfg.DefaultModel, "sonnet")
	}
	if cfg.Concurrency != 4 {
		t.Errorf("Concurrency = %d, want 4", cfg.Concurrency)
	}
	if cfg.DBPath != "/tmp/test.db" {
		t.Errorf("DBPath = %q, want %q", cfg.DBPath, "/tmp/test.db")
	}
	if cfg.QueueSize != 500 {
		t.Errorf("QueueSize = %d, want 500", cfg.QueueSize)
	}
}

func TestLoad_MissingAPIKeys(t *testing.T) {
	// Ensure the env var is absent.
	t.Setenv("CLAUDEGATE_API_KEYS", "")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error when CLAUDEGATE_API_KEYS is empty, got nil")
	}
}

func TestLoad_InvalidModel(t *testing.T) {
	t.Setenv("CLAUDEGATE_API_KEYS", "somekey")
	t.Setenv("CLAUDEGATE_DEFAULT_MODEL", "gpt-4")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid model, got nil")
	}
}

func TestLoad_Defaults(t *testing.T) {
	// Only required variable set; all others should use defaults.
	t.Setenv("CLAUDEGATE_API_KEYS", "defaultkey")
	t.Setenv("CLAUDEGATE_LISTEN_ADDR", "")
	t.Setenv("CLAUDEGATE_CLAUDE_PATH", "")
	t.Setenv("CLAUDEGATE_DEFAULT_MODEL", "")
	t.Setenv("CLAUDEGATE_CONCURRENCY", "")
	t.Setenv("CLAUDEGATE_DB_PATH", "")
	t.Setenv("CLAUDEGATE_QUEUE_SIZE", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("expected no error with defaults, got: %v", err)
	}
	if cfg.ListenAddr != ":8080" {
		t.Errorf("default ListenAddr = %q, want %q", cfg.ListenAddr, ":8080")
	}
	if cfg.ClaudePath != "/root/.local/bin/claude" {
		t.Errorf("default ClaudePath = %q, want %q", cfg.ClaudePath, "/root/.local/bin/claude")
	}
	if cfg.DefaultModel != "haiku" {
		t.Errorf("default DefaultModel = %q, want %q", cfg.DefaultModel, "haiku")
	}
	if cfg.Concurrency != 1 {
		t.Errorf("default Concurrency = %d, want 1", cfg.Concurrency)
	}
	if cfg.DBPath != "claudegate.db" {
		t.Errorf("default DBPath = %q, want %q", cfg.DBPath, "claudegate.db")
	}
	if cfg.QueueSize != 1000 {
		t.Errorf("default QueueSize = %d, want 1000", cfg.QueueSize)
	}
}
