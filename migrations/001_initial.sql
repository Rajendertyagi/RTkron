-- 001_initial.sql
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
