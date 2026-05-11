// config.go
package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// DatabaseConfig struct for database connection details.
type DatabaseConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	DBName   string `json:"dbname"`
	SSLMode  string `json:"sslmode"`
}

type LoginRateLimitConfig struct {
	MaxAttempts   int `json:"max_attempts"`
	WindowSeconds int `json:"window_seconds"`
}

type SecurityConfig struct {
	AllowedOrigins    []string             `json:"allowed_origins"`
	SessionTTLMinutes int                  `json:"session_ttl_minutes"`
	TrustProxyHeaders bool                 `json:"trust_proxy_headers"`
	LoginRateLimit    LoginRateLimitConfig `json:"login_rate_limit"`
}

// Config struct for overall application configuration.
type Config struct {
	Database    DatabaseConfig `json:"database"`
	ModulesPath string         `json:"modules_path"`
	Security    SecurityConfig `json:"security"`
}

// LoadConfigFromFile reads configuration from a JSON file.
func LoadConfigFromFile(filePath string) (*Config, error) {
	file, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("greška pri čitanju konfiguracionog fajla '%s': %w", filePath, err)
	}

	var config Config
	if err := json.Unmarshal(file, &config); err != nil {
		return nil, fmt.Errorf("greška pri parsiranju konfiguracionog fajla '%s': %w", filePath, err)
	}

	return &config, nil
}
