package config

import (
	"log"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

type Security struct {
	AutoApprove                  bool `yaml:"auto_approve"`
	DisableSignatureValidation   bool `yaml:"disable_signature_validation"`
}

type Config struct {
	Port           int      `yaml:"port"`
	DBPath         string   `yaml:"db_path"`
	CodegBaseURL   string   `yaml:"codeg_base_url"`
	CodegAPIKey    string   // Loaded from Env only
	AdminToken     string   // Loaded from Env only
	AuditEncryptKey string  // Loaded from Env only
	Security       Security `yaml:"security"`
}

// Load reads from config.yaml and overrides with Environment Variables
func Load() (*Config, error) {
	cfg := &Config{
		Port:         3090,
		DBPath:       "./data/codegmanager.db",
		CodegBaseURL: "http://localhost:3080/api",
		Security: Security{
			AutoApprove:                false,
			DisableSignatureValidation: true,
		},
	}

	// Try loading YAML
	data, err := os.ReadFile("config.yaml")
	if err == nil {
		if err := yaml.Unmarshal(data, cfg); err != nil {
			log.Printf("Warning: Failed to parse config.yaml: %v", err)
		}
	}

	// Override with Env vars
	if v := os.Getenv("PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			cfg.Port = p
		}
	}
	if v := os.Getenv("DB_PATH"); v != "" {
		cfg.DBPath = v
	}
	if v := os.Getenv("CODEG_BASE_URL"); v != "" {
		cfg.CodegBaseURL = v
	}
	if v := os.Getenv("CODEG_API_KEY"); v != "" {
		cfg.CodegAPIKey = v
	}
	if v := os.Getenv("ADMIN_TOKEN"); v != "" {
		cfg.AdminToken = v
	}
	if v := os.Getenv("AUDIT_ENCRYPTION_KEY"); v != "" {
		cfg.AuditEncryptKey = v
	}
	if v := os.Getenv("AUTO_APPROVE"); v != "" {
		cfg.Security.AutoApprove = v == "true"
	}
	if v := os.Getenv("DISABLE_SIGNATURE_VALIDATION"); v != "" {
		cfg.Security.DisableSignatureValidation = v == "true"
	}

	return cfg, nil
}
