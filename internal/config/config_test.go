package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad_AllFields(t *testing.T) {
	path := writeConfig(t, `
listen: ":9090"
data_dir: "/tmp/mnemosyne-test"
base_url: "https://mail.example.com"
session_ttl: 48h
backup:
  default_schedule: "0 5 * * *"
  max_concurrent: 4
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":9090" {
		t.Errorf("Listen = %q, want %q", cfg.Listen, ":9090")
	}
	if cfg.DataDir != "/tmp/mnemosyne-test" {
		t.Errorf("DataDir = %q, want %q", cfg.DataDir, "/tmp/mnemosyne-test")
	}
	if cfg.BaseURL != "https://mail.example.com" {
		t.Errorf("BaseURL = %q, want %q", cfg.BaseURL, "https://mail.example.com")
	}
	if cfg.SessionTTL != 48*time.Hour {
		t.Errorf("SessionTTL = %v, want %v", cfg.SessionTTL, 48*time.Hour)
	}
	if cfg.Backup.DefaultSchedule != "0 5 * * *" {
		t.Errorf("DefaultSchedule = %q, want %q", cfg.Backup.DefaultSchedule, "0 5 * * *")
	}
	if cfg.Backup.MaxConcurrent != 4 {
		t.Errorf("MaxConcurrent = %d, want %d", cfg.Backup.MaxConcurrent, 4)
	}
}

func TestLoad_Defaults(t *testing.T) {
	path := writeConfig(t, "{}")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	defaults := Defaults()
	if cfg.Listen != defaults.Listen {
		t.Errorf("Listen = %q, want default %q", cfg.Listen, defaults.Listen)
	}
	if cfg.DataDir != defaults.DataDir {
		t.Errorf("DataDir = %q, want default %q", cfg.DataDir, defaults.DataDir)
	}
	if cfg.SessionTTL != defaults.SessionTTL {
		t.Errorf("SessionTTL = %v, want default %v", cfg.SessionTTL, defaults.SessionTTL)
	}
	if cfg.Backup.MaxConcurrent != defaults.Backup.MaxConcurrent {
		t.Errorf("MaxConcurrent = %d, want default %d", cfg.Backup.MaxConcurrent, defaults.Backup.MaxConcurrent)
	}
}

func TestLoad_EnvOverrides(t *testing.T) {
	path := writeConfig(t, `
listen: ":8080"
data_dir: "/var/lib/mnemosyne"
`)
	t.Setenv("MNEMOSYNE_LISTEN", ":9000")
	t.Setenv("MNEMOSYNE_DATA_DIR", "/tmp/override")
	t.Setenv("MNEMOSYNE_BASE_URL", "https://override.example.com")

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":9000" {
		t.Errorf("Listen = %q, want %q (env override)", cfg.Listen, ":9000")
	}
	if cfg.DataDir != "/tmp/override" {
		t.Errorf("DataDir = %q, want %q (env override)", cfg.DataDir, "/tmp/override")
	}
	if cfg.BaseURL != "https://override.example.com" {
		t.Errorf("BaseURL = %q, want %q (env override)", cfg.BaseURL, "https://override.example.com")
	}
}

func TestLoad_ValidationEmptyListen(t *testing.T) {
	path := writeConfig(t, `listen: ""`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for empty listen address")
	}
}

func TestLoad_ValidationBadMaxConcurrent(t *testing.T) {
	path := writeConfig(t, `
backup:
  max_concurrent: 0
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for max_concurrent = 0")
	}
}

func TestLoad_NonexistentFile(t *testing.T) {
	_, err := Load("/nonexistent/config.yaml")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestLoad_InvalidYAML(t *testing.T) {
	path := writeConfig(t, `listen: [invalid`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}
