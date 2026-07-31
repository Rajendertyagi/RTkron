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
    "net"
    "net/http"
    "os"
    "os/exec"
    "os/signal"
    "path/filepath"
    "runtime"
    "strconv"
    "strings"
    "sync"
    "syscall"
    "time"

    gocronui "github.com/go-co-op/gocron-ui/server"
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
    
    wp, err := worker.NewWorkerPool(ctx, dbStore, apiClient, cfg)
    if err != nil {
        log.Fatalf("create worker pool: %v", err)
    }
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

    adminWrap := chooseAdminWrapper(cfg.AdminToken)

    // gocron-ui scheduler dashboard at /scheduler (admin-protected)
    if wp.Scheduler() != nil {
        port, _ := strconv.Atoi(cfg.ServerPort)
        srv := gocronui.NewServer(wp.Scheduler(), port, gocronui.WithTitle("RTkron Scheduler"))
        mux.Handle("/scheduler/", adminWrap(http.StripPrefix("/scheduler", srv.Router)))
    }

    // JSON API endpoints for UI data (admin-protected)
    apiMux := http.NewServeMux()
    api.RegisterUIDataRoutes(apiMux, dbStore)
    mux.Handle("/api/", adminWrap(apiMux))

    srv := &http.Server{
        Addr:              ":" + cfg.ServerPort,
        Handler:           loggingMiddleware(mux),
        ReadTimeout:       10 * time.Second,
        ReadHeaderTimeout: 5 * time.Second,
        WriteTimeout:      15 * time.Second,
        IdleTimeout:       120 * time.Second,
    }

    // If ADMIN_TOKEN is empty, bind the whole server to loopback only for safety.
    // NOTE: this also blocks remote senders from reaching /webhook/codeg.
    if strings.TrimSpace(cfg.AdminToken) == "" {
        log.Println("WARNING: ADMIN_TOKEN not set; binding to loopback only for safety")
        srv.Addr = "127.0.0.1:" + cfg.ServerPort
    }

    idleConnsClosed := make(chan struct{})
    shutdownCh := make(chan struct{})
    var shutdownOnce sync.Once
    go func() {
        sigCh := make(chan os.Signal, 1)
        signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
        select {
        case <-sigCh:
            log.Println("shutdown signal received")
        case <-shutdownCh:
            log.Println("tray requested quit: shutting down")
        }
        shutdownOnce.Do(func() {
            ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
            defer cancel()
            wp.Stop()
            if err := srv.Shutdown(ctx); err != nil {
                log.Printf("HTTP server Shutdown: %v", err)
            }
            close(idleConnsClosed)
        })
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

// adminAuthMiddleware enforces a bearer token or X-Admin-Token header.
// If token is empty, this middleware should not be used (use requireLocalhostMiddleware instead).
func adminAuthMiddleware(token string, next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // Accept Authorization: Bearer <token> or X-Admin-Token: <token>
        auth := r.Header.Get("Authorization")
        if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
            if strings.TrimSpace(auth[len("bearer "):]) == token {
                next.ServeHTTP(w, r)
                return
            }
        }
        if xt := r.Header.Get("X-Admin-Token"); xt != "" && xt == token {
            next.ServeHTTP(w, r)
            return
        }
        // Not authorized
        w.Header().Set("WWW-Authenticate", `Bearer realm="admin"`)
        http.Error(w, "unauthorized", http.StatusUnauthorized)
    })
}

// requireLocalhostMiddleware restricts access to requests coming from loopback addresses.
// Useful when ADMIN_TOKEN is not set and you want the UI bound to local-only access.
func requireLocalhostMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        host, _, err := net.SplitHostPort(r.RemoteAddr)
        if err != nil {
            // If RemoteAddr isn't host:port, fall back to raw value
            host = r.RemoteAddr
        }
        // Accept IPv4/IPv6 loopback and "localhost"
        if host == "127.0.0.1" || host == "::1" || strings.HasPrefix(host, "localhost") {
            next.ServeHTTP(w, r)
            return
        }
        http.Error(w, "forbidden", http.StatusForbidden)
    })
}

// chooseAdminWrapper returns a handler wrapper that enforces admin access according to cfg.AdminToken.
// If cfg.AdminToken != "" -> use adminAuthMiddleware; otherwise use requireLocalhostMiddleware.
func chooseAdminWrapper(adminToken string) func(http.Handler) http.Handler {
    if strings.TrimSpace(adminToken) != "" {
        return func(h http.Handler) http.Handler { return adminAuthMiddleware(adminToken, h) }
    }
    // no token configured -> restrict to loopback only
    return func(h http.Handler) http.Handler { return requireLocalhostMiddleware(h) }
}

func handleWebhook(w http.ResponseWriter, r *http.Request, s *store.SQLiteStore, cfg *config.Config, wp *worker.WorkerPool) {
    if r.Method != http.MethodPost {
        http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
        return
    }
    const maxBodySize = 1 << 20 // 1 MiB

    // wrap the request body to enforce a hard limit
    r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
    defer r.Body.Close()

    body, err := io.ReadAll(r.Body)
    if err != nil {
        if strings.Contains(err.Error(), "http: request body too large") {
            http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
            return
        }
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

    // enqueue for async processing (non-blocking); envelope carries the reserved key
    envelope := map[string]interface{}{
        "reserved_idempotency_key": idKey,
        "event":                    ev,
        "raw_body":                 string(body),
    }

    if !wp.Enqueue(envelope) {
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
