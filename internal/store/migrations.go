package store

import (
    "database/sql"
    "fmt"
    "strings"
)

const InitialMigration = `
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS workflows (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  definition JSON NOT NULL,
  created_at DATETIME DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS instances (
  id TEXT PRIMARY KEY,
  workflow_id TEXT NOT NULL,
  current_node TEXT,
  status TEXT NOT NULL,
  retries INTEGER DEFAULT 0,
  created_at DATETIME DEFAULT (datetime('now')),
  updated_at DATETIME DEFAULT (datetime('now')),
  FOREIGN KEY (workflow_id) REFERENCES workflows(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_instances_workflow_id ON instances(workflow_id);

CREATE TABLE IF NOT EXISTS jobs (
  id TEXT PRIMARY KEY,
  workflow_id TEXT NOT NULL,
  cron_expr TEXT,
  enabled INTEGER DEFAULT 1,
  payload TEXT,
  last_run DATETIME,
  next_run DATETIME,
  owner TEXT,
  FOREIGN KEY (workflow_id) REFERENCES workflows(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_jobs_next_run ON jobs(next_run);

CREATE TABLE IF NOT EXISTS idempotency (
  key TEXT PRIMARY KEY,
  created_at DATETIME DEFAULT (datetime('now')),
  status TEXT
);

CREATE INDEX IF NOT EXISTS idx_idempotency_created_at ON idempotency(created_at);

CREATE TABLE IF NOT EXISTS audit (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  event_id TEXT,
  action TEXT,
  payload BLOB,
  created_at DATETIME DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS deadletter (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  event_json JSON,
  attempts INTEGER DEFAULT 0,
  last_error TEXT,
  created_at DATETIME DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS auto_approve_rules (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  connection_id TEXT NOT NULL UNIQUE,
  max_per_minute INTEGER DEFAULT 0,
  created_at DATETIME DEFAULT (datetime('now'))
);
`

func splitSQLStatements(sqlText string) []string {
    parts := strings.Split(sqlText, ";")
    out := make([]string, 0, len(parts))
    for _, p := range parts {
        if strings.TrimSpace(p) != "" {
            out = append(out, p)
        }
    }
    return out
}

func Migrate(db *sql.DB) error {
    stmts := splitSQLStatements(InitialMigration)
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
    if err := tx.Commit(); err != nil {
        return err
    }
    return ensureJobsPayloadColumn(db)
}

// ensureJobsPayloadColumn adds the jobs.payload column to databases created
// before the scheduler persistence feature. It inspects PRAGMA table_info and
// only runs ALTER TABLE when the column is missing, so it is idempotent.
func ensureJobsPayloadColumn(db *sql.DB) error {
    rows, err := db.Query("PRAGMA table_info(jobs)")
    if err != nil {
        return fmt.Errorf("inspect jobs table: %w", err)
    }
    defer rows.Close()

    has := false
    for rows.Next() {
        var cid int
        var name, ctype string
        var notnull int
        var dflt sql.NullString
        var pk int
        if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
            return fmt.Errorf("scan jobs table_info: %w", err)
        }
        if name == "payload" {
            has = true
            break
        }
    }
    if has {
        return nil
    }

    _, err = db.Exec("ALTER TABLE jobs ADD COLUMN payload TEXT")
    if err != nil {
        return fmt.Errorf("add jobs.payload column: %w", err)
    }
    return nil
}
