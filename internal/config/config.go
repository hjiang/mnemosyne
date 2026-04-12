// Package config handles application configuration.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds all application configuration.
type Config struct {
	Listen     string        `yaml:"listen"`
	DataDir    string        `yaml:"data_dir"`
	BaseURL    string        `yaml:"base_url"`
	SessionTTL time.Duration `yaml:"session_ttl"`
	Backup     BackupConfig  `yaml:"backup"`
}

// BackupConfig holds backup-related settings.
type BackupConfig struct {
	DefaultSchedule string `yaml:"default_schedule"`
	MaxConcurrent   int    `yaml:"max_concurrent"`
}

// Defaults returns a Config populated with default values.
func Defaults() Config {
	return Config{
		Listen:     ":8080",
		DataDir:    "/var/lib/mnemosyne",
		BaseURL:    "http://localhost:8080",
		SessionTTL: 720 * time.Hour,
		Backup: BackupConfig{
			DefaultSchedule: "0 3 * * *",
			MaxConcurrent:   2,
		},
	}
}

// Load reads a YAML config file and applies environment variable overrides.
// Missing fields retain their default values.
func Load(path string) (Config, error) {
	cfg := Defaults()

	data, err := os.ReadFile(path) //nolint:gosec // G304 - path is from config, not user input
	if err != nil {
		return Config{}, fmt.Errorf("reading config file: %w", err)
	}

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parsing config file: %w", err)
	}

	ApplyEnvOverrides(&cfg)

	if err := validate(cfg); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// ApplyEnvOverrides applies MNEMOSYNE_* environment variable overrides.
func ApplyEnvOverrides(cfg *Config) {
	if v := os.Getenv("MNEMOSYNE_LISTEN"); v != "" {
		cfg.Listen = v
	}
	if v := os.Getenv("MNEMOSYNE_DATA_DIR"); v != "" {
		cfg.DataDir = v
	}
	if v := os.Getenv("MNEMOSYNE_BASE_URL"); v != "" {
		cfg.BaseURL = v
	}
}

func validate(cfg Config) error {
	if cfg.Listen == "" {
		return fmt.Errorf("config: listen address must not be empty")
	}
	if cfg.DataDir == "" {
		return fmt.Errorf("config: data_dir must not be empty")
	}
	if cfg.Backup.MaxConcurrent < 1 {
		return fmt.Errorf("config: backup.max_concurrent must be >= 1, got %d", cfg.Backup.MaxConcurrent)
	}
	return nil
}
