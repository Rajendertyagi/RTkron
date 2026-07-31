package store

import (
    "database/sql"
    "encoding/json"
    "errors"
    "fmt"
    "strings"
    "time"
)

type SQLiteStore struct {
    DB *sql.DB
}

func NewSQLiteStore(db *sql.DB) *SQLiteStore {
    return &SQLiteStore{DB: db}
}

// EnsureIdempotency attempts to atomically reserve an idempotency key.
// Returns (true, nil) if the key was newly reserved by this caller.
// Returns (false, nil) if the key already existed (duplicate).
// Returns (false, err) on unrecoverable DB errors.
//
// This implementation uses a single INSERT ... ON CONFLICT DO NOTHING
// and checks RowsAffected to determine whether the insert succeeded.
// It also retries transient SQLITE_BUSY / "database is locked" errors with
// exponential backoff to reduce race conditions under concurrency.
func (s *SQLiteStore) EnsureIdempotency(key string) (bool, error) {
    const (
        maxAttempts    = 6
        initialBackoff = 25 * time.Millisecond
    )

    if key == "" {
        return false, fmt.Errorf("empty idempotency key")
    }

    query := `
    INSERT INTO idempotency(key, status, created_at)
    VALUES (?, 'received', datetime('now'))
    ON CONFLICT(key) DO NOTHING;
    `

    backoff := initialBackoff
    for attempt := 1; attempt <= maxAttempts; attempt++ {
        res, err := s.DB.Exec(query, key)
        if err != nil {
            // treat SQLITE_BUSY / "database is locked" as transient and retry
            if isSqliteBusyErr(err) && attempt < maxAttempts {
                time.Sleep(backoff)
                backoff *= 2
                continue
            }
            return false, fmt.Errorf("ensure idempotency exec: %w", err)
        }

        // If RowsAffected > 0, we inserted the row and reserved the key.
        if ra, err := res.RowsAffected(); err == nil && ra > 0 {
            return true, nil
        }

        // RowsAffected == 0 means the key already existed (ON CONFLICT DO NOTHING).
        return false, nil
    }

    return false, fmt.Errorf("ensure idempotency failed after %d attempts", maxAttempts)
}

// MarkIdempotencyDone marks the idempotency key as completed.
// It's safe to call even if the key does not exist (no-op).
func (s *SQLiteStore) MarkIdempotencyDone(key string) error {
    if key == "" {
        return fmt.Errorf("empty idempotency key")
    }
    _, err := s.DB.Exec("UPDATE idempotency SET status = 'done', updated_at = datetime('now') WHERE key = ?", key)
    if err != nil {
        // treat SQLITE_BUSY as transient; do a couple of retries
        if isSqliteBusyErr(err) {
            for i := 0; i < 3; i++ {
                time.Sleep(time.Duration(50*(1<<i)) * time.Millisecond)
                if _, err2 := s.DB.Exec("UPDATE idempotency SET status = 'done', updated_at = datetime('now') WHERE key = ?", key); err2 == nil {
                    return nil
                }
            }
        }
        return fmt.Errorf("mark idempotency done: %w", err)
    }
    return nil
}

// isSqliteBusyErr returns true for common SQLite busy/locked errors.
func isSqliteBusyErr(err error) bool {
    if err == nil {
        return false
    }
    msg := strings.ToLower(err.Error())
    return strings.Contains(msg, "database is locked") ||
        strings.Contains(msg, "database is busy") ||
        strings.Contains(msg, "sqlite_busy") ||
        strings.Contains(msg, "sqlite3: busy")
}

func (s *SQLiteStore) InsertAudit(eventID, action string, payload []byte) error {
    _, err := s.DB.Exec("INSERT INTO audit(event_id, action, payload, created_at) VALUES (?, ?, ?, datetime('now'))", eventID, action, payload)
    return err
}

func (s *SQLiteStore) InsertDeadLetter(eventJSON string, attempts int, lastError string) error {
    _, err := s.DB.Exec("INSERT INTO deadletter(event_json, attempts, last_error, created_at) VALUES (?, ?, ?, datetime('now'))", eventJSON, attempts, lastError)
    return err
}

// Job and workflow helpers (minimal)
type Workflow struct {
    ID         string          `json:"id"`
    Name       string          `json:"name"`
    Definition json.RawMessage `json:"definition"`
    CreatedAt  time.Time       `json:"created_at"`
}

func (s *SQLiteStore) GetWorkflow(id string) (*Workflow, error) {
    row := s.DB.QueryRow("SELECT id, name, definition, created_at FROM workflows WHERE id = ?", id)
    var w Workflow
    var def []byte
    var created string
    if err := row.Scan(&w.ID, &w.Name, &def, &created); err != nil {
        if errors.Is(err, sql.ErrNoRows) {
            return nil, nil
        }
        return nil, err
    }
    w.Definition = def
    w.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
    return &w, nil
}

type WorkflowInstance struct {
    ID          string
    WorkflowID  string
    CurrentNode string
    Status      string
    Retries     int
    CreatedAt   time.Time
    UpdatedAt   time.Time
}

func (s *SQLiteStore) GetInstanceBySession(sessionID string) (*WorkflowInstance, error) {
    row := s.DB.QueryRow("SELECT id, workflow_id, current_node, status, retries FROM instances WHERE id = ?", sessionID)
    var i WorkflowInstance
    if err := row.Scan(&i.ID, &i.WorkflowID, &i.CurrentNode, &i.Status, &i.Retries); err != nil {
        return nil, err
    }
    return &i, nil
}

func (s *SQLiteStore) GetInstanceByConnection(connectionID string) (*WorkflowInstance, error) {
    row := s.DB.QueryRow("SELECT id, workflow_id, current_node, status, retries FROM instances WHERE id = ?", connectionID)
    var i WorkflowInstance
    if err := row.Scan(&i.ID, &i.WorkflowID, &i.CurrentNode, &i.Status, &i.Retries); err != nil {
        return nil, err
    }
    return &i, nil
}

func (s *SQLiteStore) UpdateInstance(inst *WorkflowInstance) error {
    _, err := s.DB.Exec("UPDATE instances SET current_node = ?, status = ?, retries = ?, updated_at = datetime('now') WHERE id = ?",
        inst.CurrentNode, inst.Status, inst.Retries, inst.ID)
    return err
}

// AutoApproveRule represents a row in auto_approve_rules.
type AutoApproveRule struct {
    ID           int64
    ConnectionID string
    MaxPerMinute int
    CreatedAt    string
}

// GetAllAutoApproveRules returns all rules.
func (s *SQLiteStore) GetAllAutoApproveRules() ([]AutoApproveRule, error) {
    rows, err := s.DB.Query("SELECT id, connection_id, max_per_minute, created_at FROM auto_approve_rules ORDER BY created_at DESC")
    if err != nil {
        return nil, fmt.Errorf("query auto_approve_rules: %w", err)
    }
    defer rows.Close()

    var out []AutoApproveRule
    for rows.Next() {
        var r AutoApproveRule
        if err := rows.Scan(&r.ID, &r.ConnectionID, &r.MaxPerMinute, &r.CreatedAt); err != nil {
            continue
        }
        out = append(out, r)
    }
    return out, nil
}

// GetAutoApproveRule returns a rule for a given connection_id or sql.ErrNoRows.
func (s *SQLiteStore) GetAutoApproveRule(connectionID string) (*AutoApproveRule, error) {
    var r AutoApproveRule
    err := s.DB.QueryRow("SELECT id, connection_id, max_per_minute, created_at FROM auto_approve_rules WHERE connection_id = ?", connectionID).
        Scan(&r.ID, &r.ConnectionID, &r.MaxPerMinute, &r.CreatedAt)
    if err != nil {
        return nil, err
    }
    return &r, nil
}

// AddAutoApproveRule inserts or updates a rule (upsert).
func (s *SQLiteStore) AddAutoApproveRule(connectionID string, maxPerMinute int) error {
    _, err := s.DB.Exec(`
    INSERT INTO auto_approve_rules(connection_id, max_per_minute, created_at)
    VALUES (?, ?, datetime('now'))
    ON CONFLICT(connection_id) DO UPDATE SET max_per_minute = excluded.max_per_minute;
    `, connectionID, maxPerMinute)
    if err != nil {
        return fmt.Errorf("add auto_approve_rule: %w", err)
    }
    return nil
}

// DeleteAutoApproveRule removes a rule by connection_id.
func (s *SQLiteStore) DeleteAutoApproveRule(connectionID string) error {
    _, err := s.DB.Exec("DELETE FROM auto_approve_rules WHERE connection_id = ?", connectionID)
    if err != nil {
        return fmt.Errorf("delete auto_approve_rule: %w", err)
    }
    return nil
}

// Job represents a persisted scheduled job definition for rehydration.
type Job struct {
    ID         string
    WorkflowID string
    CronExpr   string
    Enabled    bool
    Payload    []byte
    LastRun    sql.NullString
    NextRun    sql.NullString
    Owner      string
}

// SaveJob upserts a job definition. WorkflowID is required (FK to workflows);
// a placeholder workflow row is created if it does not exist so cron-only jobs
// (which dispatch scheduled_prompt events) persist cleanly.
func (s *SQLiteStore) SaveJob(j *Job) error {
    if j.WorkflowID == "" {
        j.WorkflowID = j.ID
    }
    _, err := s.DB.Exec("INSERT OR IGNORE INTO workflows(id, name, definition) VALUES (?, ?, '{}')", j.WorkflowID, j.WorkflowID)
    if err != nil {
        return fmt.Errorf("ensure placeholder workflow: %w", err)
    }

    enabled := 0
    if j.Enabled {
        enabled = 1
    }
    _, err = s.DB.Exec(`
    INSERT INTO jobs(id, workflow_id, cron_expr, enabled, payload, last_run, next_run, owner)
    VALUES (?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(id) DO UPDATE SET
      workflow_id = excluded.workflow_id,
      cron_expr = excluded.cron_expr,
      enabled = excluded.enabled,
      payload = excluded.payload,
      owner = excluded.owner;
    `, j.ID, j.WorkflowID, j.CronExpr, enabled, j.Payload, j.LastRun, j.NextRun, j.Owner)
    if err != nil {
        return fmt.Errorf("save job: %w", err)
    }
    return nil
}

// GetEnabledJobs returns all enabled job definitions for scheduler rehydration.
func (s *SQLiteStore) GetEnabledJobs() ([]Job, error) {
    rows, err := s.DB.Query("SELECT id, workflow_id, cron_expr, enabled, payload, last_run, next_run, owner FROM jobs WHERE enabled = 1 ORDER BY id")
    if err != nil {
        return nil, fmt.Errorf("query jobs: %w", err)
    }
    defer rows.Close()

    var out []Job
    for rows.Next() {
        var j Job
        var enabled int
        if err := rows.Scan(&j.ID, &j.WorkflowID, &j.CronExpr, &enabled, &j.Payload, &j.LastRun, &j.NextRun, &j.Owner); err != nil {
            continue
        }
        j.Enabled = enabled == 1
        out = append(out, j)
    }
    return out, nil
}

// UpdateJobLastRun records the last execution time for a job.
func (s *SQLiteStore) UpdateJobLastRun(jobID string, ts time.Time) error {
    _, err := s.DB.Exec("UPDATE jobs SET last_run = ? WHERE id = ?", ts.UTC().Format("2006-01-02 15:04:05"), jobID)
    if err != nil {
        return fmt.Errorf("update job last_run: %w", err)
    }
    return nil
}

// DeleteJob removes a job definition by id.
func (s *SQLiteStore) DeleteJob(jobID string) error {
    _, err := s.DB.Exec("DELETE FROM jobs WHERE id = ?", jobID)
    if err != nil {
        return fmt.Errorf("delete job: %w", err)
    }
    return nil
}
