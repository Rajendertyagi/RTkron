# RTKron Agent Rules & Guidelines

## 1. Architectural Philosophy
- **Single Binary Deployments**: RTKron must compile into a single self-contained executable. Do not rely on external directories (e.g., `templates/`, `migrations/`). Always use `go:embed` to bake assets directly into the `.exe`.
- **Minimal Dependencies**: Use standard library Go packages where possible. For external libraries, prefer pure-Go (CGO_ENABLED=0) libraries to guarantee easy cross-compilation. Avoid JS frameworks like React/Tailwind for the dashboard; use Vanilla HTML/CSS/JS.
- **Enterprise Aesthetics**: The UI must use sleek dark mode styling, curated color palettes, micro-animations, and modern typography. 

## 2. Windows Targeting
- Output binary must always be named `RTKron.exe`.
- Rely on Windows native registry hooks for startup (`golang.org/x/sys/windows/registry`).
- Auto-open browser using `rundll32 url.dll,FileProtocolHandler`.

## 3. Configuration Management
- Configuration is loaded via `internal/config/config.go` with `.env` as a fallback.
- Never hardcode configurations (like Ports) deep in the application. Always bubble them up to the `Config` struct.
- The default port is `3090`. Default AutoApprove is `false`.

## 4. Resilience
- Idempotency must be tracked on every incoming webhook event.
- Failed events must be written to a Dead Letter Queue (DLQ).
- Ensure graceful shutdown sequences are tied to system signals (SIGINT/SIGTERM) and the system tray.
