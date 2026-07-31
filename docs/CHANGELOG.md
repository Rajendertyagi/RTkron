# RTKron AI Synchronization Changelog

**ATTENTION OPENCODE / AI AGENTS:**
Whenever you complete a task or modify code in this repository, you **MUST** add an entry to the top of this file.
This file acts as the primary synchronization bridge to ensure Antigravity (the high-level Architect AI) stays updated on the project's state.

## Format
- **Date/Time:** [Insert current date/time]
- **Agent:** OpenCode (or state if manual user edit)
- **Files Modified:**
  - `path/to/file1.go`
  - `path/to/file2.md`
- **Summary of Changes:**
  - [Brief 1-2 sentence summary of what was added/changed and why]

---

## [Phase 4 - Zero Inline JS/Styles Refactor] - 2026-08-01
- **Agent:** OpenCode
- **Files Modified:**
  - `internal/api/static/index.html`
  - `internal/api/static/app.js`
  - `internal/api/static/style.css`
- **Summary of Changes:**
  - Removed all inline `style="..."` and `onclick="..."` from `index.html`: `#powered-by` and `#error-banner` now use a `.hidden` utility class; the error-banner close button uses `data-action="hide-error"`.
  - Replaced inline `onclick` handlers injected by `renderJobCard` (run, delete, toggle-schedule) with `data-action`/`data-id`/`data-name` attributes handled by event delegation on `#jobs-container`.
  - Toggled `#policy-empty` and schedule-details visibility via `hidden` class instead of `element.style.display`; added `.job-schedule-item` class for the schedule block's bottom margin.
  - Added `escapeAttr` helper (HTML-encodes + `&quot;`) for attribute-safe data binding of `job.id`/`job.name`.
  - HTML now has zero inline styles and zero inline JS; `app.js` no longer writes `element.style.*` anywhere.

---

## [Phase 4 - Codeg Tools (Payload Builder + Auto-Approve Security Center)] - 2026-08-01
- **Agent:** OpenCode
- **Files Modified:**
  - `internal/api/static/index.html`
  - `internal/api/static/app.js`
  - `internal/api/static/style.css`
  - `internal/api/scheduler_api.go`
- **Summary of Changes:**
  - Embedded two Codeg-specific tools into the unified dashboard:
    - **Payload Builder**: form with `cron_expr`, `job_id`/`workflow_id`, and a JSON payload textarea. On submit, app.js parses the payload and `POST /api/jobs`; the new backend handler `handleJobCreate` validates the body and calls `WorkerPool.SchedulePromptCron(cronExpr, jobID, payload)` (which persists the job for rehydration). `job_id` wins over `workflow_id` when both are provided.
    - **Auto-Approve Security Center**: form that takes a `connection_id` and `POST /api/policy` (action `add`) to save an auto-approve rule into `auto_approve_rules`. The allowed-connections list loads via `GET /api/policy` and each row can be removed (`POST /api/policy` action `delete`). Uses event delegation + `escapeHtml` (no inline `onclick` with data attributes) to avoid attribute-injection when a connection_id contains quotes.
  - `POST /api/jobs` added to `scheduler_api.go` (was GET-only); handler decodes the payload with `UseNumber` for int64 precision and returns 400 for invalid JSON / missing fields.

---

## [Phase 4 - Self-Hosted Single-App UI (gocron-ui fork)] - 2026-08-01
- **Agent:** OpenCode
- **Files Modified:**
  - `go.mod`
  - `cmd/rtkron/main.go`
  - `internal/api/ui.go`
  - `internal/api/ui_data.go`
  - `internal/api/scheduler_api.go` (new)
  - `internal/worker/pool.go`
  - `internal/api/static/index.html`, `internal/api/static/app.js`, `internal/api/static/style.css` (new)
  - `internal/api/templates/index.html` (deleted)
- **Summary of Changes:**
  - Replaced the gocron-ui dependency and its `/scheduler/` mount with a self-hosted single-app dashboard at `/`. Dropped `github.com/go-co-op/gocron-ui` from `go.mod`.
  - `internal/api/ui.go` rewritten: `go:embed static/*` + `http.FileServer` via `fs.Sub`; `RegisterUIHandlers(mux)` takes no store param. Old `internal/api/templates/` deleted.
  - Forked gocron-ui's 3 vanilla static files into `internal/api/static/` (no build step): rebranded RTKron title, same DOM IDs; `app.js` polls `./api/jobs` every 3s instead of WebSocket and calls `./api/config`.
  - New `internal/api/scheduler_api.go`: `GET /api/config` (title), `GET /api/jobs` (list from store, enriched with live nextRun/lastRun from gocron and `nextRuns` computed via `robfig/cron/v3`), `POST /api/jobs/{id}/run`, `DELETE /api/jobs/{id}`.
  - Since gocron v2.0.0's `Job` interface has no `RunNow()`, added `WorkerPool.RunJobNow(jobID)` which loads the persisted job and enqueues a `scheduled_prompt` event (same path the cron trigger uses).
  - `main.go`: dropped `gocronui`/`strconv` imports and the `/scheduler/` block; `RegisterUIHandlers(mux)` serves the app at `/`; scheduler routes registered on the admin-protected `apiMux` (so `/api/*` stays token/loopback-protected). Admin caveat unchanged: with `ADMIN_TOKEN` set, browser fetches to `/api/*` still 401.

---

## [Phase 6 - Scheduler Persistence & Rehydration] - 2026-07-31
- **Agent:** OpenCode
- **Files Modified:**
  - `internal/store/migrations.go`
  - `internal/store/sqlite_store.go`
  - `internal/worker/pool.go`
  - `cmd/rtkron/main.go`
- **Summary of Changes:**
  - Ticket 6 (Scheduler Persistence & Rehydration) implemented.
  - Added `jobs.payload` column to `InitialMigration`; `Migrate` now runs idempotent `ensureJobsPayloadColumn` (PRAGMA table_info check + `ALTER TABLE ADD COLUMN`) so existing DBs get the column.
  - Added `Job` model + store methods: `SaveJob` (upsert; creates a placeholder workflow row if missing to satisfy the FK), `GetEnabledJobs`, `UpdateJobLastRun`, `DeleteJob`.
  - `SchedulePromptCron` now persists the job definition after registering it; the cron task records `last_run` via `UpdateJobLastRun`. `RemoveScheduledJob` also deletes the row from the DB.
  - Added `WorkerPool.RehydrateScheduler`, which loads enabled jobs from the store and re-registers them with gocron. Wired into `main.go` right after `wp.Start()` (failures are non-fatal warnings).
  - Note: `SchedulePromptCron`'s `workflow_id` is set to the `jobID` placeholder; `SaveJob` guarantees a matching placeholder workflow row so the FK constraint holds.

---

## [Phase 6 - Restore AcpRespondPermission in Auto-Approve] - 2026-07-31
- **Agent:** OpenCode
- **Files Modified:**
  - `internal/worker/pool.go`
- **Summary of Changes:**
  - Restored the `AcpRespondPermission(..., "approve", ...)` call inside the whitelisted auto-approve path. The flow now: look up `auto_approve_rules` → fetch session snapshot to extract `pending_request_id` → call `acp_respond_permission` with decision `approve` → audit `auto_approved`. This actually unblocks the agent as intended.
  - If no `pending_request_id` is found in the snapshot, it audits `auto_approved` with a note rather than failing.
  - Reverted the Phase 6 regression that audited `auto_approved` without calling the approve endpoint.

---

## [Phase 6 - Auto-Approve Policy Rules] - 2026-07-31
- **Agent:** OpenCode
- **Files Modified:**
  - `internal/store/migrations.go`
  - `internal/store/sqlite_store.go`
  - `internal/api/ui_data.go`
  - `internal/worker/pool.go`
  - `cmd/rtkron/main.go`
- **Summary of Changes:**
  - Added `auto_approve_rules` table (connection_id UNIQUE, max_per_minute) to `InitialMigration` so `Migrate` actually runs it (the standalone const `migrationAddAutoApproveRules` would never execute under the existing `InitialMigration` runner).
  - Added `AutoApproveRule` model + CRUD in `SQLiteStore`: `GetAllAutoApproveRules`, `GetAutoApproveRule`, `AddAutoApproveRule` (upsert), `DeleteAutoApproveRule`.
  - `RegisterUIDataRoutes` signature changed from `*sql.DB` to `*store.SQLiteStore` (main.go:123 updated to pass `dbStore`); existing stats/activity handlers now use `db.DB`.
  - Added `/api/policy` (GET list, POST add/upsert/delete). POST body: `{"connection_id": "...", "max_per_minute": 10, "action": "add"|"delete"}`.
  - `handlePermissionRequest` now consults `auto_approve_rules` instead of `w.Config.AutoApprove`: whitelisted connections are auto-approved (audit `auto_approved` + optional `snapshot_fetched`/`snapshot_fetch_failed`), non-whitelisted are audited `permission_request_pending`.
  - **NOTE:** the previous `AcpRespondPermission` approve call was removed by this redesign (auto-approve now audits + fetches snapshot only). `AcpRespondPermission` remains in `client.go` but is no longer called. Flag if the approve call should be restored in the auto-approve path.

---

## [Phase 5 - Scheduled Prompt Dispatch] - 2026-07-31
- **Agent:** OpenCode
- **Files Modified:**
  - `internal/worker/pool.go`
- **Summary of Changes:**
  - Fixed audit finding #8 (`scheduled_prompt` events dead-ended). `processEvent` now routes `scheduled_prompt` to a dedicated `handleScheduledPrompt` instead of `handleTurnComplete`.
  - `handleScheduledPrompt` enforces event-level idempotency (`event:<eventID>`, synthesized from `scheduled_id` when missing), builds a prompt payload, and dispatches via `AcpPrompt` with external key `scheduled:<scheduled_id>:event:<eventID>`. On dispatch failure it dead-letters and leaves the idempotency row open for manual retry; with no client it audits `scheduled_prompt_no_client`.
  - Cron-sourced events already carry `event_id` + `scheduled_id: jobID` (see `SchedulePromptCron`).

---

## [Phase 5 - Per-Instance Locking] - 2026-07-31
- **Agent:** OpenCode
- **Files Modified:**
  - `internal/worker/pool.go`
- **Summary of Changes:**
  - Fixed audit finding #7 (data race on `WorkflowInstance`). `WorkerPool` now maintains a `locksMu`-guarded `instanceLocks map[string]*sync.Mutex`.
  - `handleTurnComplete` acquires the per-instance lock (via `getInstanceLock`) after loading the instance and holds it (via `defer`) while mutating and persisting `inst` fields; `removeInstanceLock` drops the entry when a workflow completes to prevent unbounded map growth.
  - Events without an instance proceed without a lock (unchanged behavior).

---

## [Phase 5 - Atomic Idempotency Reserve] - 2026-07-31
- **Agent:** OpenCode
- **Files Modified:**
  - `internal/store/sqlite_store.go`
- **Summary of Changes:**
  - Fixed audit finding #6 (non-atomic idempotency check / race). `EnsureIdempotency` now uses a single atomic `INSERT ... ON CONFLICT(key) DO NOTHING` and inspects `RowsAffected()` (1 = newly reserved, 0 = duplicate) instead of the SELECT-then-INSERT pattern.
  - Added exponential-backoff retry for transient SQLITE_BUSY / "database is locked" errors (max 6 attempts, 25ms initial), and a busy-retry loop in `MarkIdempotencyDone` (3 attempts). Added `isSqliteBusyErr` helper.
  - Kept the exported `DB` field (main.go:123 passes `dbStore.DB` to `RegisterUIDataRoutes`); the idempotency logic follows the user's snippet using `s.DB`.

---

## [Phase 5 - Webhook Body Limit + Server Timeouts] - 2026-07-31
- **Agent:** OpenCode
- **Files Modified:**
  - `cmd/rtkron/main.go`
- **Summary of Changes:**
  - Fixed audit finding #5 (DoS surface). `handleWebhook` wraps `r.Body` with `http.MaxBytesReader(w, r.Body, 1<<20)` (1 MiB) and responds 413 Payload Too Large when the limit is exceeded; other read errors return 400.
  - `http.Server` now configures `ReadTimeout: 10s`, `ReadHeaderTimeout: 5s`, `WriteTimeout: 15s`, `IdleTimeout: 120s`.
  - Loopback binding logic (when `ADMIN_TOKEN` is empty) and the loopback override of `srv.Addr` remain unchanged.

---

## [Phase 5 - Turn Retry Attempt Keys + AcpPrompt] - 2026-07-31
- **Agent:** OpenCode
- **Files Modified:**
  - `internal/worker/pool.go`
  - `internal/codeg/client.go`
- **Summary of Changes:**
  - Fixed audit finding #4 (dead retry mechanism). `handleTurnComplete` now uses a per-attempt idempotency key `turn:<turnID>:attempt:<n>`; the next attempt key is **not** pre-reserved when scheduling a retry (that caused "attempt key already reserved" → retry dropped), it is reserved by `handleTurnComplete` when the retry envelope is actually processed.
  - `processEvent` now extracts `connection_id`/`session_id`/`turn_id` and dispatches via the multi-param signature `handleTurnComplete(ctx, eventID, connectionID, sessionID, turnID, ev)`.
  - Renamed `Client.SendPrompt` → `Client.AcpPrompt(ctx, payload []byte, idempotencyKey string)` to strictly match the architecture document. The idempotency key is sent as the `Idempotency-Key` HTTP header; the JSON payload map is NOT modified.
  - Extracted the duplicated retry-scheduling blocks into a shared `scheduleRetry` helper.

---

## [Phase 5 - Dashboard Schema Fix] - 2026-07-31
- **Agent:** OpenCode
- **Files Modified:**
  - `internal/api/templates/index.html`
- **Summary of Changes:**
  - Fixed audit finding #3 (dashboard JS didn't match the backend API schema). `refreshDashboard` now reads the real `StatsResponse` fields (`active_instances`, `successful_webhooks`) and renders `ActivityItem` fields (`kind`, `event_id`, `action`, `created_at`) instead of non-existent `workflows`/`events`/`workflow`/`status`/`time`.
  - Added `escapeHtml` for all DB-derived values injected into the activity table (removes the stored-XSS vector from raw `innerHTML`).

---

## [Phase 4 - Loopback Binding Safety] - 2026-07-31
- **Agent:** OpenCode
- **Files Modified:**
  - `cmd/rtkron/main.go`
- **Summary of Changes:**
  - When `ADMIN_TOKEN` is empty, the HTTP server now binds to `127.0.0.1:<port>` instead of all interfaces, preventing remote access to the UI and admin APIs.
  - **NOTE:** loopback binding also blocks remote senders from reaching `/webhook/codeg`; set `ADMIN_TOKEN` (which restores all-interface binding) if the webhook must accept remote traffic.
  - Design from user (Rahul). User's proposed `gocronui.New()` and `store.OpenDB()` were NOT used — `gocronui.NewServer()` + `srv.Router` (the actual v0.3.0 API) and the inline DB open/migrate remain.

---

## [Phase 4 - Admin Auth Middleware] - 2026-07-31
- **Agent:** OpenCode
- **Files Modified:**
  - `cmd/rtkron/main.go`
- **Summary of Changes:**
  - Fixed critical audit finding #2 (no authentication / dead AdminToken): added `adminAuthMiddleware` (Bearer or `X-Admin-Token`), `requireLocalhostMiddleware` (loopback-only), and `chooseAdminWrapper` (token → auth, else loopback).
  - Wrapped `/scheduler/` (gocron-ui) and `/api/*` (stats/activity) with `chooseAdminWrapper(cfg.AdminToken)`. `/webhook/codeg` and `/healthz` remain public (HMAC-protected / liveness).
  - Design from user (Rahul).
  - **Note:** when `ADMIN_TOKEN` is set, browsers don't send the auth header on page loads or fetch, so the dashboard's `/api/*` polls and direct `/scheduler/` navigation will return 401 — loopback-only mode works without a token.

---

## [Phase 4 - Webhook Idempotency Fix] - 2026-07-31
- **Agent:** OpenCode
- **Files Modified:**
  - `cmd/rtkron/main.go`
  - `internal/worker/pool.go`
- **Summary of Changes:**
  - Fixed critical audit finding #1 (webhook events were always dropped as duplicates): `handleWebhook` now wraps the event in an envelope carrying the reserved idempotency key, and `processEvent` recognizes the envelope, skips the redundant `EnsureIdempotency` re-check for reserved keys, and marks the key done after successful processing.
  - `processEvent` now routes via `handlePermissionRequest(ctx, evMap)` / `handleTurnComplete(ctx, evMap)`; handlers extract fields from the event map themselves.
  - Design from user (Rahul): webhook reserves the key at receipt; worker owns status transitions (`received` → `done`).

---

## [Phase 4 - Code Quality Fixes] - 2026-07-31
- **Agent:** OpenCode
- **Files Modified:**
  - `internal/codeg/client.go`
  - `internal/worker/pool.go`
  - `cmd/rtkron/main.go`
- **Summary of Changes:**
  - Fixed `AcpRespondPermission` to build the request body with `json.Marshal` instead of `fmt.Sprintf` (unescaped-string / malformed JSON bug class).
  - Fixed `SchedulePromptCron` to deep-copy the scheduled payload via `json.Decoder.UseNumber()` so the closure no longer shares nested map/slice references with the caller (data race risk) and int64 IDs keep full precision.
  - Replaced all remaining hand-built JSON (`fmt.Sprintf` with raw strings) in `pool.go` with `json.Marshal` — fixes escaped-string corruption in audit records and the turn_complete prompt payload.
  - Consolidated the duplicate OS-signal / tray-quit shutdown goroutines in `main.go` behind a single `sync.Once` path, eliminating the double `close(idleConnsClosed)` panic risk and double `wp.Stop()`.
  - Mounted gocron-ui with the real configured port and `WithTitle("RTkron Scheduler")` instead of a misleading hard-coded `0` (the port argument is unused by gocron-ui v0.3.0).
  - Known limitation (reported, not fixable upstream): gocron-ui v0.3.0 always starts a background `broadcastJobUpdates` goroutine; there is no `Shutdown()` or handler-only constructor. It is a 1s idle ticker and does not block process exit.

---

## [Phase 4 - gocron v2 Upgrade & gocron-ui] - 2026-07-31
- **Agent:** OpenCode
- **Files Modified:**
  - `go.mod`
  - `internal/worker/pool.go`
  - `cmd/rtkron/main.go`
- **Summary of Changes:**
  - Upgraded `gocron` from v1 to `gocron/v2` for cron-based job scheduling.
  - Added `github.com/go-co-op/gocron-ui` dependency for the scheduler web dashboard.
  - Refactored `WorkerPool` to use gocron v2 API (`NewScheduler()`, `NewJob(CronJob(...), NewTask(...), WithTags(...), WithSingletonMode(...))`, `Start()`, `StopJobs()`, `RemoveByTags()`).
  - Mounted gocron-ui at `/scheduler/` in `main.go` using `gocronui.NewServer(wp.Scheduler(), 0)` and `srv.Router`.
  - Removed gocron v1 and gocron-ui v0.3.0 (incompatible with v1) from go.mod.

---

## [Phase 5 - Dashboard Wiring] - 2026-07-31
- **Agent:** Antigravity
- **Files Modified:**
  - `internal/api/templates/index.html`
- **Summary of Changes:**
  - Injected Vanilla JS logic into the HTML template to actively poll the `/api/stats` and `/api/activity` endpoints every 3 seconds. The frontend is now fully wired to the backend UI data.

## [Phase 4 - UI Data API] - 2026-07-31
- **Agent:** OpenCode
- **Files Modified:**
  - `internal/api/ui_data.go`
  - `cmd/rtkron/main.go`
- **Summary of Changes:**
  - Created `internal/api/ui_data.go` with `/api/stats` and `/api/activity` JSON endpoints for the dashboard.
  - Wired `RegisterUIDataRoutes` in `main.go` passing `dbStore.DB`.

---

## [Phase 4 - gocron Integration] - 2026-07-31
- **Agent:** OpenCode
- **Files Modified:**
  - `go.mod`
  - `internal/worker/pool.go`
  - `internal/codeg/client.go`
- **Summary of Changes:**
  - Added `github.com/go-co-op/gocron` dependency for cron-based job scheduling.
  - Integrated gocron scheduler into `WorkerPool` with `SchedulePromptCron`, `RemoveScheduledJob`, `StartAsync`, and graceful shutdown.
  - Simplified `codeg.Client` by removing circuit breaker (gobreaker), using retryablehttp directly.
  - Removed unused `github.com/sony/gobreaker` from go.mod.
  - NOTE: `gocron-ui` was removed — it requires `gocron/v2` which is incompatible with the `gocron/v1` used in the worker pool.

---

## [Initial Setup] - 2026-07-31
- **Agent:** Antigravity
- **Summary of Changes:**
  - Initialized this CHANGELOG.md file to serve as the synchronization bridge between isolated AIs.
