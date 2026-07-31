# Persistent Memory (ICM Context Backup)

## 1. Resolved Errors & Solutions
- **Go Mod Cache Warning (`actions/setup-go@v5`)**: CI warned that the `go.sum` dependencies cache could not be found. We disabled `setup-go` caching entirely using `cache: false` in `.github/workflows/build.yml` because `go.sum` is auto-generated locally.
- **Missing Migrations Folder**: When `RTKron.exe` was moved out of the workspace, it crashed on startup trying to read `./migrations/001_initial.sql`. We solved this by removing external file-based migrations and rewriting `internal/store/migrations.go` to use an embedded raw string.
- **Embedded Templates Bug**: `go:embed templates/*` failed because the directive evaluates paths relative to its source file (`internal/api/ui.go`), not the root project path. We fixed this by moving the `templates` directory into `internal/api/templates/`.

## 2. Core Decisions & Architecture
- **Monolith to Modular**: Refactored the original monolithic `main.go` into `config`, `store`, `codeg`, `worker`, and `api` packages.
- **Dependency Injection**: Dependencies (`dbStore`, `apiClient`, `cfg`) are initialized in `main.go` and injected downward to keep code testable and decoupled.
- **System Tray**: Added `github.com/getlantern/systray` for Windows. Used build tags (`//go:build !windows`) to write an empty fallback so Linux/macOS compilation doesn't break.
- **Graceful Shutdown**: The HTTP server and worker loops listen to context cancellation and an internal `shutdownCh` to cleanly tear down and flush buffers before exiting.

## 3. User Preferences
- **CGO_ENABLED=0**: Hard requirement. Do not use any library requiring CGO or native headers.
- **Name**: Output binary must ALWAYS be `RTKron.exe`.
- **"Do things properly from the start"**: Produce scalable, elegant, and standard Go code. Use proper DI, retry-mechanisms, dead-letter queues, and robust SQLite practices (WAL mode, busy timeouts).
