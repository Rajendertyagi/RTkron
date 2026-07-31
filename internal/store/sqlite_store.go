package store

import (
    "database/sql"
    "encoding/json"
    "errors"
    "time"
)

type SQLiteStore struct {
    DB *sql.DB
}

func NewSQLiteStore(db *sql.DB) *SQLiteStore {
    return &SQLiteStore{DB: db}
}

// EnsureIdempotency tries to insert the idempotency key.
// Returns true if inserted (caller should proceed), false if already exists.
func (s *SQLiteStore) EnsureIdempotency(key string) (bool, error) {
    tx, err := s.DB.Begin()
    if err != nil {
        return false, err
    }
    defer tx.Rollback()

    var existing string
    err = tx.QueryRow("SELECT key FROM idempotency WHERE key = ?", key).Scan(&existing)
    if err != nil && err != sql.ErrNoRows {
        return false, err
    }
    if existing != "" {
        _ = tx.Commit()
        return false, nil
    }
    _, err = tx.Exec("INSERT INTO idempotency(key, created_at, status) VALUES (?, datetime('now'), ?)", key, "received")
    if err != nil {
        return false, err
    }
    if err := tx.Commit(); err != nil {
        return false, err
    }
    return true, nil
}

func (s *SQLiteStore) MarkIdempotencyDone(key string) error {
    _, err := s.DB.Exec("UPDATE idempotency SET status = ? WHERE key = ?", "done", key)
    return err
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
