package main

import (
    "context"
    "crypto/hmac"
    "crypto/sha256"
    "database/sql"
    "encoding/hex"
    "encoding/json"
    "errors"
    "flag"
    "fmt"
    "io"
    "log"
    "net/http"
    "os"
    "os/signal"
    "path/filepath"
    "strings"
    "syscall"
    "time"

    _ "modernc.org/sqlite"
)

const (
    defaultDBPath = "./data/codegmanager.db"
    migrationFile = "migrations/001_initial.sql"
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
}

type WebhookEvent struct {
    EventID      string          `json:"event_id"`
    Type         string          `json:"type"`
    ConnectionID string          `json:"connection_id"`
    SessionID    string          `json:"session_id"`
    TurnID       string          `json:"turn_id"`
    Payload      json.RawMessage `json:"payload"`
    RawBody      []byte          `json:"-"`
}

func loadConfig() Config {
    cfg := Config{
        DBPath:                     getenv("DB_PATH", defaultDBPath),
        CodegBaseURL:               getenv("CODEG_BASE_URL", ""),
        CodegAPIKey:                getenv("CODEG_API_KEY", ""),
        AutoApprove:                getenvBool("AUTO_APPROVE", false),
        AdminToken:                 getenv("ADMIN_TOKEN", ""),
        AuditEncryptionKey:         getenv("AUDIT_ENCRYPTION_KEY", ""),
        DisableSignatureValidation: getenvBool("DISABLE_SIGNATURE_VALIDATION", true),
        ServerPort:                 getenv("SERVER_PORT", "8080"),
        LogLevel:                   getenv("LOG_LEVEL", "info"),
    }
    return cfg
}

func getenv(key, def string) string {
    if v := os.Getenv(key); v != "" {
        return v
    }
    return def
}

func getenvBool(key string, def bool) bool {
    v := os.Getenv(key)
    if v == "" {
        return def
    }
    l := strings.ToLower(v)
    return l == "1" || l == "true" || l == "yes"
}

func ensureDirForFile(path string) error {
    dir := filepath.Dir(path)
    if dir == "." || dir == "" {
        return nil
    }
    return os.MkdirAll(dir, 0o755)
}

func runMigrations(db *sql.DB) error {
    f, err := os.Open(migrationFile)
    if err != nil {
        return fmt.Errorf("open migration file: %w", err)
    }
    defer f.Close()
    content, err := io.ReadAll(f)
    if err != nil {
        return fmt.Errorf("read migration file: %w", err)
    }
    // naive split on semicolon; migration file is simple and idempotent
    stmts := splitSQLStatements(string(content))
    tx, err := db.Begin()
    if err != nil {
        return err
    }
    for _, s := range stmts {
        s = strings.TrimSpace(s)
        if s == "" {
            continue
        }
        if _, err := tx.Exec(s); err != nil {
            _ = tx.Rollback()
            return fmt.Errorf("exec migration stmt: %w; stmt: %s", err, s)
        }
    }
    return tx.Commit()
}

func splitSQLStatements(sqlText string) []string {
    // Very simple splitter for our controlled migration file.
    parts := strings.Split(sqlText, ";")
    out := make([]string, 0, len(parts))
    for _, p := range parts {
        if strings.TrimSpace(p) != "" {
            out = append(out, p)
        }
    }
    return out
}

func main() {
    flag.Parse()
    cfg := loadConfig()

    log.Printf("starting codegmanager (port=%s) db=%s auto_approve=%v disable_sig=%v",
        cfg.ServerPort, cfg.DBPath, cfg.AutoApprove, cfg.DisableSignatureValidation)

    if err := ensureDirForFile(cfg.DBPath); err != nil {
        log.Fatalf("ensure db dir: %v", err)
    }

    db, err := sql.Open("sqlite", cfg.DBPath+"?_busy_timeout=5000&_foreign_keys=1")
    if err != nil {
        log.Fatalf("open sqlite: %v", err)
    }
    defer db.Close()

    // Enable WAL mode for better concurrency
    if _, err := db.Exec("PRAGMA journal_mode = WAL;"); err != nil {
        log.Printf("warning: enable WAL failed: %v", err)
    }

    // Run migrations (idempotent)
    if err := runMigrations(db); err != nil {
        log.Fatalf("migrations failed: %v", err)
    }
    log.Println("migrations applied")

    // worker channel and simple bounded pool
    eventsCh := make(chan WebhookEvent, 100)
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    startWorkerPool(ctx, db, cfg, eventsCh, 4)

    // HTTP handlers
    mux := http.NewServeMux()
    mux.HandleFunc("/webhook/codeg", func(w http.ResponseWriter, r *http.Request) {
        handleWebhook(w, r, db, cfg, eventsCh)
    })
    mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte("ok"))
    })

    srv := &http.Server{
        Addr:    ":" + cfg.ServerPort,
        Handler: loggingMiddleware(mux),
    }

    // graceful shutdown
    idleConnsClosed := make(chan struct{})
    go func() {
        sigCh := make(chan os.Signal, 1)
        signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
        <-sigCh
        log.Println("shutdown signal received")
        ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
        defer cancel()
        if err := srv.Shutdown(ctx); err != nil {
            log.Printf("HTTP server Shutdown: %v", err)
        }
        cancel()
        close(idleConnsClosed)
    }()

    log.Printf("listening on :%s", cfg.ServerPort)
    if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
        log.Fatalf("server error: %v", err)
    }
    <-idleConnsClosed
    log.Println("server stopped")
}

// loggingMiddleware is minimal request logging
func loggingMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        start := time.Now()
        next.ServeHTTP(w, r)
        log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
    })
}

// handleWebhook validates signature (optional), enforces idempotency, quick-ack, and enqueues event.
func handleWebhook(w http.ResponseWriter, r *http.Request, db *sql.DB, cfg Config, eventsCh chan<- WebhookEvent) {
    if r.Method != http.MethodPost {
        http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
        return
    }
    body, err := io.ReadAll(r.Body)
    if err != nil {
        http.Error(w, "bad request", http.StatusBadRequest)
        return
    }
    // parse minimal fields to get event_id for idempotency
    var ev WebhookEvent
    if err := json.Unmarshal(body, &ev); err != nil {
        http.Error(w, "invalid json", http.StatusBadRequest)
        return
    }
    ev.RawBody = body

    // signature validation (optional)
    if !cfg.DisableSignatureValidation {
        sig := r.Header.Get("X-Codeg-Signature")
        ts := r.Header.Get("X-Codeg-Timestamp")
        if sig == "" || ts == "" {
            http.Error(w, "missing signature headers", http.StatusBadRequest)
            return
        }
        if !validateHMAC(body, ts, sig, cfg.CodegAPIKey) {
            http.Error(w, "invalid signature", http.StatusUnauthorized)
            return
        }
    }

    // idempotency: key = event:{event_id}
    if ev.EventID == "" {
        // fallback to a generated key using timestamp+payload hash
        ev.EventID = fmt.Sprintf("local-%d-%x", time.Now().UnixNano(), sha256.Sum256(body))
    }
    idKey := "event:" + ev.EventID

    // quick idempotency check and insert in a transaction
    tx, err := db.Begin()
    if err != nil {
        log.Printf("db begin: %v", err)
        http.Error(w, "internal", http.StatusInternalServerError)
        return
    }
    var exists string
    err = tx.QueryRow("SELECT key FROM idempotency WHERE key = ?", idKey).Scan(&exists)
    if err != nil && err != sql.ErrNoRows {
        _ = tx.Rollback()
        log.Printf("idempotency query: %v", err)
        http.Error(w, "internal", http.StatusInternalServerError)
        return
    }
    if exists != "" {
        // already processed; quick ack
        _ = tx.Commit()
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte("duplicate"))
        return
    }
    _, err = tx.Exec("INSERT INTO idempotency(key, created_at, status) VALUES (?, datetime('now'), ?)", idKey, "received")
    if err != nil {
        _ = tx.Rollback()
        log.Printf("idempotency insert: %v", err)
        http.Error(w, "internal", http.StatusInternalServerError)
        return
    }
    if err := tx.Commit(); err != nil {
        log.Printf("idempotency commit: %v", err)
        http.Error(w, "internal", http.StatusInternalServerError)
        return
    }

    // quick 200 ack
    w.WriteHeader(http.StatusOK)
    _, _ = w.Write([]byte("ok"))

    // enqueue for async processing (non-blocking)
    select {
    case eventsCh <- ev:
    default:
        // channel full: persist to deadletter for later processing
        log.Println("events channel full; writing to deadletter")
        _, _ = db.Exec("INSERT INTO deadletter(event_json, attempts, last_error, created_at) VALUES (?, 0, ?, datetime('now'))", string(body), "queue_full")
    }
}

func validateHMAC(body []byte, timestamp, headerSig, secret string) bool {
    // headerSig expected format: sha256=hex
    parts := strings.SplitN(headerSig, "=", 2)
    if len(parts) != 2 {
        return false
    }
    algo, hexSig := parts[0], parts[1]
    if algo != "sha256" {
        return false
    }
    mac := hmac.New(sha256.New, []byte(secret))
    // include timestamp in HMAC to mitigate replay
    mac.Write([]byte(timestamp))
    mac.Write(body)
    expected := hex.EncodeToString(mac.Sum(nil))
    return hmac.Equal([]byte(expected), []byte(hexSig))
}

func startWorkerPool(ctx context.Context, db *sql.DB, cfg Config, eventsCh <-chan WebhookEvent, workers int) {
    for i := 0; i < workers; i++ {
        go func(id int) {
            log.Printf("worker-%d started", id)
            for {
                select {
                case <-ctx.Done():
                    log.Printf("worker-%d stopping", id)
                    return
                case ev := <-eventsCh:
                    processEvent(ctx, db, cfg, ev)
                }
            }
        }(i + 1)
    }
}

func processEvent(ctx context.Context, db *sql.DB, cfg Config, ev WebhookEvent) {
    // attach a short timeout for processing
    ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
    defer cancel()

    log.Printf("processing event %s type=%s conn=%s", ev.EventID, ev.Type, ev.ConnectionID)

    // Example: auto-approve flow for permission_request
    if ev.Type == "permission_request" && cfg.AutoApprove {
        if err := autoApprove(ctx, db, cfg, ev); err != nil {
            log.Printf("auto-approve failed for %s: %v", ev.EventID, err)
            // record to deadletter
            _, _ = db.Exec("INSERT INTO deadletter(event_json, attempts, last_error, created_at) VALUES (?, 1, ?, datetime('now'))", string(ev.RawBody), err.Error())
            return
        }
        // mark idempotency status = done
        _, _ = db.Exec("UPDATE idempotency SET status = ? WHERE key = ?", "done", "event:"+ev.EventID)
        return
    }

    // For other event types, just log and mark done
    log.Printf("no handler for event type=%s; storing audit", ev.Type)
    _, _ = db.Exec("INSERT INTO audit(event_id, action, payload, created_at) VALUES (?, ?, ?, datetime('now'))", ev.EventID, "unhandled_event", ev.RawBody)
    _, _ = db.Exec("UPDATE idempotency SET status = ? WHERE key = ?", "done", "event:"+ev.EventID)
}

// autoApprove is a minimal placeholder that demonstrates the pattern.
// In a full implementation this would call acp_get_session_snapshot and acp_respond_permission.
func autoApprove(ctx context.Context, db *sql.DB, cfg Config, ev WebhookEvent) error {
    // policy check: for local convenience we allow all when AutoApprove is true.
    // In real use, consult policy table or config.
    log.Printf("auto-approving event %s (conn=%s)", ev.EventID, ev.ConnectionID)

    // write audit record
    _, err := db.Exec("INSERT INTO audit(event_id, action, payload, created_at) VALUES (?, ?, ?, datetime('now'))", ev.EventID, "auto_approve", ev.RawBody)
    if err != nil {
        return fmt.Errorf("audit insert: %w", err)
    }

    // TODO: call Codeg endpoints using Codeg client (not implemented in this minimal main)
    // Example: client.AcpGetSessionSnapshot(ctx, ev.SessionID) -> pendingRequestID
    // then client.AcpRespondPermission(ctx, pendingRequestID, "approve", "auto-approved by local config")

    // simulate success
    time.Sleep(200 * time.Millisecond)
    return nil
}
