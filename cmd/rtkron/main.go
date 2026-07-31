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
    "os/exec"
    "os/signal"
    "path/filepath"
    "runtime"
    "strings"
    "syscall"
    "time"

    "rtkron/internal/api"
    "rtkron/internal/codeg"
    "rtkron/internal/config"
    "rtkron/internal/store"
    "rtkron/internal/tray"
    "rtkron/internal/worker"

    _ "modernc.org/sqlite"
)

type WebhookEvent struct {
    EventID      string          `json:"event_id"`
    Type         string          `json:"type"`
    ConnectionID string          `json:"connection_id"`
    SessionID    string          `json:"session_id"`
    TurnID       string          `json:"turn_id"`
    Payload      json.RawMessage `json:"payload"`
    RawBody      []byte          `json:"-"`
}

func ensureDirForFile(path string) error {
    dir := filepath.Dir(path)
    if dir == "." || dir == "" {
        return nil
    }
    return os.MkdirAll(dir, 0o755)
}

func main() {
    flag.Parse()
    cfg, err := config.LoadFromEnv()
    if err != nil {
        log.Fatalf("failed to load config: %v", err)
    }

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

    if _, err := db.Exec("PRAGMA journal_mode = WAL;"); err != nil {
        log.Printf("warning: enable WAL failed: %v", err)
    }

    if err := store.Migrate(db); err != nil {
        log.Fatalf("migrations failed: %v", err)
    }
    log.Println("migrations applied")

    // Instantiate DI dependencies
    dbStore := store.NewSQLiteStore(db)
    apiClient := codeg.NewClient(cfg.CodegBaseURL, cfg.CodegAPIKey, cfg.ClientTimeout)
    
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    
    wp := worker.NewWorkerPool(ctx, dbStore, apiClient, cfg)
    wp.Start()

    // HTTP handlers
    mux := http.NewServeMux()
    mux.HandleFunc("/webhook/codeg", func(w http.ResponseWriter, r *http.Request) {
        handleWebhook(w, r, dbStore, cfg, wp)
    })
    mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte("ok"))
    })

    // UI Handlers
    api.RegisterUIHandlers(mux, dbStore)

    // JSON API endpoints for UI data
    api.RegisterUIDataRoutes(mux, dbStore.DB)

    srv := &http.Server{
        Addr:    ":" + cfg.ServerPort,
        Handler: loggingMiddleware(mux),
    }

    idleConnsClosed := make(chan struct{})
    go func() {
        sigCh := make(chan os.Signal, 1)
        signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
        <-sigCh
        log.Println("shutdown signal received")
        ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
        defer cancel()
        wp.Stop()
        if err := srv.Shutdown(ctx); err != nil {
            log.Printf("HTTP server Shutdown: %v", err)
        }
        cancel()
        close(idleConnsClosed)
    }()

    if cfg.AutoOpenBrowser {
        go func() {
            time.Sleep(300 * time.Millisecond)
            url := "http://localhost:" + cfg.ServerPort + "/"
            switch runtime.GOOS {
            case "windows":
                _ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
            case "darwin":
                _ = exec.Command("open", url).Start()
            default:
                _ = exec.Command("xdg-open", url).Start()
            }
        }()
    }

    log.Printf("listening on :%s", cfg.ServerPort)

    shutdownCh := make(chan struct{})
    go func() {
        <-shutdownCh
        log.Println("shutting down HTTP server")
        ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
        defer cancel()
        wp.Stop()
        if err := srv.Shutdown(ctx); err != nil {
            log.Printf("HTTP server Shutdown: %v", err)
        }
        cancel()
        close(idleConnsClosed)
    }()

    tray.StartTray(ctx, cfg.ServerPort, func() {
        log.Println("tray requested quit: shutting down")
        close(shutdownCh)
    })

    if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
        log.Fatalf("server error: %v", err)
    }
    <-idleConnsClosed
    log.Println("server stopped")
}

func loggingMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        start := time.Now()
        next.ServeHTTP(w, r)
        log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
    })
}

func handleWebhook(w http.ResponseWriter, r *http.Request, s *store.SQLiteStore, cfg *config.Config, wp *worker.WorkerPool) {
    if r.Method != http.MethodPost {
        http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
        return
    }
    body, err := io.ReadAll(r.Body)
    if err != nil {
        http.Error(w, "bad request", http.StatusBadRequest)
        return
    }
    var ev WebhookEvent
    if err := json.Unmarshal(body, &ev); err != nil {
        http.Error(w, "invalid json", http.StatusBadRequest)
        return
    }
    ev.RawBody = body

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

    if ev.EventID == "" {
        ev.EventID = fmt.Sprintf("local-%d-%x", time.Now().UnixNano(), sha256.Sum256(body))
    }
    idKey := "event:" + ev.EventID

    inserted, err := s.EnsureIdempotency(idKey)
    if err != nil {
        log.Printf("ensure idempotency err: %v", err)
        http.Error(w, "internal", http.StatusInternalServerError)
        return
    }
    if !inserted {
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte("duplicate"))
        return
    }

    w.WriteHeader(http.StatusOK)
    _, _ = w.Write([]byte("ok"))

    if !wp.Enqueue(ev) {
        log.Println("events channel full; writing to deadletter")
        _ = s.InsertDeadLetter(string(body), 0, "queue_full")
    }
}

func validateHMAC(body []byte, timestamp, headerSig, secret string) bool {
    parts := strings.SplitN(headerSig, "=", 2)
    if len(parts) != 2 {
        return false
    }
    algo, hexSig := parts[0], parts[1]
    if algo != "sha256" {
        return false
    }
    mac := hmac.New(sha256.New, []byte(secret))
    mac.Write([]byte(timestamp))
    mac.Write(body)
    expected := hex.EncodeToString(mac.Sum(nil))
    return hmac.Equal([]byte(expected), []byte(hexSig))
}
