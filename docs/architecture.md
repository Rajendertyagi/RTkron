# RTKron (codegmanager): Detailed Architecture & Database Schema

**ATTENTION ALL AI AGENTS:** This document contains the strict schema, interface contracts, and design rules for RTKron.

## 1. High-Level Design Decisions & Tradeoffs
| Area | Chosen Approach | Why |
|---|---|---|
| **DB** | SQLite (Single-host) | Simple, zero infra, fast for single user. (Note: Originally bbolt, but evolved to SQLite for relational tracking of workflows/instances). |
| **Scheduler Leader** | Single-host | Matches single-user constraint; simplest. |
| **Auto-Approve** | Disabled by default | Safety first; reduces risk of accidental approvals. Managed dynamically via UI options form (saved to SQLite), NOT a static YAML file. |
| **Webhook Handling** | Quick ack + async worker | Avoids timeouts, keeps webhook reliability high. |
| **Retries** | Centralized backoff + circuit breaker | Prevents cascading failures and noisy retries. |
| **UI** | Minimal SPA embedded in binary | Low ops, easy local use. (Forking `gocron-ui` structure). |

## 2. Implementation Rules (No Hardcoding)
- **Single DB**: Use SQLite file (e.g., `data/codegmanager.db`) with WAL enabled.
- **Config Pattern**: Load env vars first, then override with `config.yaml`. Use a typed `Config` struct.
- **Interfaces and DI**: Define interfaces for `Store`, `Scheduler`, `CodegClient`, `Policy` and wire concrete implementations in `main`. This prevents hardcoding and makes testing easy.
- **Migrations**: Use a migration tool and keep SQL migrations in `migrations/`. Run migrations at startup.
- **Dependency Injection**: In `main`, build `store := NewSQLiteStore(cfg.DB.Path)`, `client := NewCodegClient(cfg)`, `scheduler := NewScheduler(store, client, cfg)`. Pass interfaces to handlers.
- **Idempotency Helper**: Single function `EnsureIdempotent(ctx, key, fn)` that writes idempotency key in a transaction and runs `fn` only if key absent.

## 3. Concrete Database Schema (SQLite)

### `workflows`
- `id TEXT PRIMARY KEY`
- `name TEXT`
- `definition JSON` (nodes, transitions)
- `created_at DATETIME`

### `instances`
- `id TEXT PRIMARY KEY`
- `workflow_id TEXT` (Index)
- `current_node TEXT`
- `status TEXT` (running, waiting, failed, completed)
- `retries INTEGER`
- `created_at DATETIME`, `updated_at DATETIME`

### `jobs`
- `id TEXT PRIMARY KEY`
- `workflow_id TEXT`
- `cron_expr TEXT`
- `enabled BOOLEAN`
- `last_run DATETIME`, `next_run DATETIME` (Index)
- `owner TEXT`

### `idempotency`
- `key TEXT PRIMARY KEY` (e.g., `event:{event_id}` or `job:{job_id}:run:{ts}`)
- `created_at DATETIME` (Index for TTL cleanup)
- `status TEXT`

### `audit`
- `id INTEGER PRIMARY KEY AUTOINCREMENT`
- `event_id TEXT`
- `action TEXT`
- `payload BLOB` (encrypted snapshot)
- `created_at DATETIME`

### `deadletter`
- `id INTEGER PRIMARY KEY AUTOINCREMENT`
- `event_json JSON`
- `attempts INTEGER`
- `last_error TEXT`
- `created_at DATETIME`

*Note: Use transactions for state transitions and idempotency writes to guarantee atomicity.*

## 4. Operational and Safety Defaults
- **Auto-approve**: Disabled by default. Managed exclusively via the UI Option Forms and saved directly into the SQLite database. We do not use `policy.yaml`.
- **Audit encryption**: Use `AUDIT_KEY` env var to encrypt `audit.payload`.
- **Backups and compaction**: Scripts to copy DB file safely and run `VACUUM` periodically.
- **Local admin**: Rely on tokenless localhost loopback mode for UI access (`requireLocalhostMiddleware`). Do NOT build complex UI login screens or token prompts. Keep it simple and frictionless for local use.
