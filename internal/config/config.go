package config

import (
    "errors"
    "os"
    "strconv"
    "time"
)

type Config struct {
    DBPath                     string
    CodegBaseURL               string
    CodegAPIKey                string
    AutoApprove                bool
    AdminToken                 string
    AuditEncryptionKey         string
    DisableSignatureValidation bool
    ServerPort                 string
    LogLevel                   string
    WorkerCount                int
    EventQueueSize             int
    ClientTimeout              time.Duration
}

func LoadFromEnv() (*Config, error) {
    cfg := &Config{
        DBPath:                     getEnv("DB_PATH", "./data/codegmanager.db"),
        CodegBaseURL:               getEnv("CODEG_BASE_URL", ""),
        CodegAPIKey:                getEnv("CODEG_API_KEY", ""),
        AutoApprove:                getEnvBool("AUTO_APPROVE", false),
        AdminToken:                 getEnv("ADMIN_TOKEN", ""),
        AuditEncryptionKey:         getEnv("AUDIT_ENCRYPTION_KEY", ""),
        DisableSignatureValidation: getEnvBool("DISABLE_SIGNATURE_VALIDATION", true),
        ServerPort:                 getEnv("SERVER_PORT", "8080"),
        LogLevel:                   getEnv("LOG_LEVEL", "info"),
        WorkerCount:                getEnvInt("WORKER_COUNT", 4),
        EventQueueSize:             getEnvInt("EVENT_QUEUE_SIZE", 100),
        ClientTimeout:              getEnvDuration("CLIENT_TIMEOUT_SECONDS", 15),
    }

    // Basic validation
    if cfg.DBPath == "" {
        return nil, errors.New("DB_PATH is required")
    }
    if cfg.CodegBaseURL == "" {
        // allow empty for local dev, but warn caller
    }
    return cfg, nil
}

func getEnv(key, def string) string {
    if v := os.Getenv(key); v != "" {
        return v
    }
    return def
}

func getEnvBool(key string, def bool) bool {
    v := os.Getenv(key)
    if v == "" {
        return def
    }
    switch v {
    case "1", "true", "TRUE", "True", "yes", "YES":
        return true
    default:
        return false
    }
}

func getEnvInt(key string, def int) int {
    v := os.Getenv(key)
    if v == "" {
        return def
    }
    n, err := strconv.Atoi(v)
    if err != nil {
        return def
    }
    return n
}

func getEnvDuration(key string, defSeconds int) time.Duration {
    v := os.Getenv(key)
    if v == "" {
        return time.Duration(defSeconds) * time.Second
    }
    n, err := strconv.Atoi(v)
    if err != nil {
        return time.Duration(defSeconds) * time.Second
    }
    return time.Duration(n) * time.Second
}
