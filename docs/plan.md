# RTKron (codegmanager): Master Architecture & Detailed Plan

**ATTENTION ALL AI AGENTS:** This is the authoritative, refined architectural blueprint for RTKron. All future implementation must strictly adhere to these patterns, components, and safety controls.

## 1. Architecture and Components

**High Level**
- **Ingress**: `POST /webhook/codeg` (`net/http`) — quick 200 ack, validate signature, enqueue event.
- **Worker Pool**: Bounded goroutine pool processes events asynchronously.
- **API Client**: Centralized Codeg client (`go-retryablehttp`) with retries, timeouts, circuit breaker.
- **Storage**: `bbolt` / `sqlite` for job/workflow definitions, run state, idempotency keys, audit logs.
- **Scheduler**: `gocron/v2` rehydrated from DB; leader election for multi-instance.
- **UI**: Lightweight single-page app embedded in the service (forking `gocron-ui` structure) for CRUD of workflows and schedules, with custom HTML/JS forms for Codeg prompt feeding.
- **Observability**: Prometheus metrics, OpenTelemetry traces, structured logs.

**Go Package Layout**
- `/cmd/rtkron` (Main application entrypoint)
- `/internal/api` (Webhook handlers, UI JSON endpoints)
- `/internal/worker` (Worker pool, event dispatcher)
- `/internal/codeg` (Codeg client, retry/circuit logic)
- `/internal/store` (DB wrappers, schema helpers, idempotency)
- `/internal/scheduler` (gocron integration, leader election)
- `/internal/policy` (Auto-approve rules loaded from SQLite)
- `/internal/telemetry` (Metrics, tracing, logging)
- `/ui` (Static assets for customized gocron-ui fork)

---

## 2. Detailed Workflows

### A. Webhook Listener and Processing
**Ingress handler**
1. Validate HMAC signature and timestamp window.
2. Parse JSON; extract `event_id`, `type`, `connection_id`, `session_id`, `turn_id`.
3. Persist idempotency key `event:{event_id}`. If it exists, return 200.
4. Respond 200 immediately and push event to internal queue.

**Worker**
1. Pull event, process with bounded concurrency.
2. On transient failures, retry with exponential backoff; after N attempts, move to dead-letter bucket.

### B. Auto-Approve Permission Request
**Preconditions**
- Auto-approve disabled by default in prod. Controlled by config/policy.
- Policy rules: allowlist of connection_id patterns managed via the UI and saved in the SQLite database. No static YAML files.

**Flow**
1. Worker receives `permission_request` event. Validate policy.
2. Call `acp_get_session_snapshot`. Extract `pending_request_id`.
3. Persist audit record.
4. Call `acp_respond_permission` with explicit decision.

### C. Chaining on `turn_complete`
**Model**
- Workflows are explicit state machines: nodes = prompts, transitions = success/failure/timeouts.

**Flow**
1. On `turn_complete`, worker loads active workflow instance.
2. Validate idempotency for `turn_id`.
3. Compute next node; persist `in_flight` marker in a single transaction.
4. Dispatch `acp_prompt` for next node.
5. On success, update state to `waiting_for_turn_complete`.

### D. Scheduler and Cron Engine (`gocron/v2`)
**Persistence**
- Store job definitions (cron expr, workflow ID, next run) in DB.

**Execution**
- On schedule hit, trigger job: create run instance, call `acp_prompt` initial payload.
- Use run-level idempotency key `job:{job_id}:run:{timestamp}`.

---

## 3. Development Phases

**Phase 1: Foundation (Completed)**
- Initialize project. Set up `go-retryablehttp` client and API helpers (`acp_prompt`, `acp_get_session_snapshot`, `acp_respond_permission`).

**Phase 2: The Event Loop (Completed)**
- Build `net/http` webhook server to listen for events. Implement Auto-Approve workflow.

**Phase 3: The Engine & Storage (Completed)**
- Integrate SQLite/bbolt. Integrate `gocron/v2` to read DB and trigger jobs.

**Phase 4: The Visual UI (Current Focus)**
- **Fork the basic structure of `gocron-ui`.**
- **Embed custom HTML/JS forms** that allow defining prompt chains visually and mapping them to schedules.
- **Dynamic Dropdowns**: Query Codeg APIs for sessions/folders to populate UI dropdowns.
- **Single Application**: The entire dashboard (scheduler + custom forms) runs natively inside the single `.exe`.

**Phase 5: Verification & Deployment**
- Webhook verification + idempotency checks.
- Build CI with unit/integration tests and a staging environment.

---

## 4. Data Models and API Contracts

### A. bbolt Buckets and Key Patterns
- **workflows** — `key: workflow:{id}` | `value: JSON {id,name,nodes,transitions,meta}`
- **instances** — `key: instance:{id}` | `value: JSON {workflow_id,current_node,status,retries,created_at}`
- **jobs** — `key: job:{id}` | `value: JSON {cron,workflow_id,enabled,last_run,next_run,owner}`
- **idempotency** — `key: event:{event_id}` | `value: {ts,status}`. (TTL via periodic cleanup)
- **audit** — `key: audit:{ts}:{id}` | `value: encrypted JSON {event,action,response}`
- **deadletter** — `key: dead:{id}` | `value: JSON {event,attempts,last_error}`

### B. Webhook Handler Contract
- **Endpoint**: `POST /webhook/codeg`
- **Headers**: `X-Codeg-Signature: sha256=...`, `X-Codeg-Timestamp: ...`
- **Body**: JSON event with `event_id`, `type`, `connection_id`, `session_id`, `turn_id`, `payload`.
- **Behavior**: validate signature and timestamp; if idempotency key exists return 200; otherwise persist idempotency key, return 200, enqueue event.

### C. Codeg Client Helpers
- `acp_get_session_snapshot(ctx, sessionID) -> (snapshot, error)`
- `acp_respond_permission(ctx, pendingRequestID, decision, reason) -> (response, error)`
- `acp_prompt(ctx, payload, idempotencyKey) -> (response, error)`
*(All client calls must accept context.Context, use timeouts, and return structured errors).*

---

## 5. Non-Functional Requirements & Observability

### Reliability & Security
- **SLOs**: Webhook ack latency < 100ms; worker processing success rate > 99% over 24h.
- **Retries**: Exponential backoff with jitter; classify 4xx as permanent.
- **Dead-letter**: Events failing after N attempts go to deadletter for manual inspection.
- **Security**: TLS for inbound/outbound. Auto-approve DISABLED by default (requires `policy.yaml` allowlist and `AUTO_APPROVE=true`).

### Observability
- **Logs**: Structured JSON with `event_id`, `workflow_id`, `connection_id`, `instance_id`, `trace_id`.
- **Metrics**: Counters for `webhook.received`, `webhook.duplicates`, `autoapprove.attempts`, `job.runs`, `job.failures`.
- **Tracing**: OpenTelemetry spans across webhook → snapshot → respond flows.
- **Alerts**: Dead-letter growth, scheduler lag, high retry rates.
