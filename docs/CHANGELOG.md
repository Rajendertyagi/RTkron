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
