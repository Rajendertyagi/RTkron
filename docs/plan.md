# RTKron Development Plan

## Phase 1: Skeleton & Configuration (Completed)
- Wipe MVP files and scaffold enterprise directory structure
- Set up `Makefile` for Windows compilation (`RTKron.exe`)
- Implement `internal/config` using environment variables and `.env` fallback. Set default port to 3090.

## Phase 2: Interfaces & Database (Completed)
- Implement `internal/store` using modernc.org/sqlite.
- Embed `InitialMigration` string natively in the Go binary for single-executable deployment.

## Phase 3: The API & Bootstrap (Completed)
- Build `internal/api/ui.go` and embed HTML using `go:embed`.
- Write enterprise UI in Vanilla CSS with dark mode, inter font, and micro-animations.
- Auto-reload script via `/healthz` polling.
- Wire dependencies in `cmd/rtkron/main.go`.
- Implement cross-platform system tray using `github.com/getlantern/systray`.
- Add auto-open browser functionality and start-with-windows registry hooks.

## Phase 4: Core Logic (Next)
- Write `internal/worker/pool.go`: implement event processing, retry loops, and dead-letter queueing.
- Write `internal/codeg/client.go`: implement HTTP logic to forward payloads to the Codeg API securely.
- Write `internal/api/ui_data.go`: wire up backend JSON endpoints to populate the frontend dashboard with live data.

## Phase 5: Verification & Deployment
- Run full end-to-end webhook tests.
- Verify binary on CI/CD (GitHub Actions).
