# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Quick Reference

- **Language:** Go 1.26
- **Build:** `make build` (output: `bin/claudegate`)
- **Test all:** `make test` (runs `go test ./... -v -count=1`, all must pass)
- **Test single package:** `go test ./internal/job -v -count=1`
- **Test single test:** `go test ./internal/api -v -count=1 -run TestCreateJob`
- **Lint:** `make lint` (installs golangci-lint if missing)
- **Run:** `make run` (requires .env configured)
- **Clean:** `make clean`
- **Toolchain:** mise (Go installed via mise, see `mise.toml`). Run `make setup` to install.

## Architecture

Flow in one line:

```
POST /jobs → Handler → SQLite → Queue (chan) → Worker (claude CLI) → SQLite + SSE + Webhook
```

### Packages

- **cmd/claudegate** (`main.go`): Entry point. Wires all dependencies in order: config → store → queue → recovery → workers → HTTP server. Handles graceful shutdown on SIGINT/SIGTERM with a 10s timeout.

- **internal/config** (`config.go`): Loads all configuration from env vars. Fails fast at startup if anything is missing or invalid. `defaultSecurityPrompt` is hardcoded here, not user-configurable.

- **internal/job** (`model.go`, `store.go`, `sqlite.go`): `Job` struct and status constants. `Store` interface decouples callers from storage. `SQLiteStore` implements `Store` using `modernc.org/sqlite` (pure Go, no CGO). WAL mode enabled on open. Schema migration is idempotent (`CREATE TABLE IF NOT EXISTS`).

- **internal/queue** (`queue.go`): Buffered `chan string` holds job IDs. `Start()` launches N worker goroutines. `Subscribe/Unsubscribe` manage per-job SSE fan-out via `map[string][]chan SSEEvent` protected by `sync.RWMutex`. `Recovery()` re-enqueues jobs stuck in `processing`.

- **internal/worker** (`worker.go`): Execs claude CLI with `--print --verbose --output-format stream-json --dangerously-skip-permissions`. Parses stdout line by line (NDJSON). Calls `onChunk` for each `"assistant"` message, returns the `"result"` string at the end. Strips all `CLAUDE*` env vars from the subprocess.

- **internal/webhook** (`webhook.go`): Fire-and-forget `goroutine`. 3 retries with exponential backoff (1s, 2s, 4s). 30s per-request timeout. No dead-letter queue — failures are logged and dropped.

- **internal/api** (`handler.go`, `middleware.go`, `sse.go`, `static/index.html`): Eight routes on Go 1.22 native mux (method+path patterns). Middleware chain: `CORSMiddleware → LoggingMiddleware → RequestIDMiddleware → AuthMiddleware → mux`. CORS is outermost so OPTIONS preflight bypasses auth. Auth uses `subtle.ConstantTimeCompare`. `/api/v1/health` and `/` are exempt from auth. The frontend SPA (`static/index.html`) is embedded at compile time via `//go:embed` — no filesystem access at runtime.

## Critical Implementation Details

Things a developer MUST know before touching the code:

**1. CLAUDE env vars — do not remove the filter**

`worker.go:filteredEnv()` strips every env var starting with `CLAUDE` before exec-ing the CLI. The Claude CLI detects a parent session via `CLAUDE_*` vars and refuses to start with "nested session" error. This is mandatory when developing inside Claude Code. Never remove this filter.

**2. `--verbose` is required**

The CLI must be called with `--verbose` alongside `--print --output-format stream-json`. Without it, the CLI refuses to emit the stream-json format.

**3. `--dangerously-skip-permissions` and the root restriction**

Required for daemon/non-interactive mode. The flag does NOT work when running as root — the service must run as a dedicated non-root user. If you see permission-related startup failures, check which user owns the process.

**4. Security system prompt**

`config.go:defaultSecurityPrompt` is prepended to every job's system prompt in `queue.go:processJob()`. It instructs Claude to refuse filesystem, shell, and network operations. This is a soft guardrail (LLM instruction, not a technical sandbox). Disable with `CLAUDEGATE_UNSAFE_NO_SECURITY_PROMPT=true`. If both config security prompt and job `system_prompt` are set, they are concatenated: `securityPrompt + "\n\n" + jobSystemPrompt`.

**5. SQLite WAL mode**

Enabled at startup with `PRAGMA journal_mode=WAL`. Without it, any write (job result) locks the entire file and blocks concurrent GET poll requests. Do not remove this pragma.

**6. Crash recovery**

`queue.Recovery()` runs before workers start. It calls `store.ResetProcessing()`, which moves all `processing` jobs back to `queued` and returns their IDs, then re-enqueues them. This gives at-least-once execution guarantees across restarts.

**7. `Store.Get` returns `(nil, nil)` for missing jobs**

When a job ID does not exist, `Get` returns `nil, nil` — not an error. Every handler that calls `Get` must check `if j == nil` before using the result. This is already done in all current handlers; maintain this pattern.

**8. Enqueue order matters**

In `CreateJob`, the job is written to SQLite first, then enqueued. This is intentional: if enqueue fails (queue full), the job record exists and will be recovered at next startup. The reverse order would silently lose the job.

**9. Job cancellation flow**

Cancel uses a two-phase approach: the handler marks the job as `cancelled` in the DB, then calls `queue.Cancel(id)` to cancel the worker's context. If the job is still queued (in the channel), `processJob` checks the DB status before `MarkProcessing` and skips it. The `cancels` map in Queue stores per-job `context.CancelFunc` entries protected by the existing `sync.RWMutex`. `Status.IsTerminal()` is the single source of truth for terminal state checks — use it instead of listing statuses manually.

**10. Per-job timeout**

When `CLAUDEGATE_JOB_TIMEOUT_MINUTES > 0`, `processJob` wraps the job context with `context.WithTimeout`. The timeout context is layered on top of the cancel context, so both mechanisms compose. `context.DeadlineExceeded` maps to `StatusFailed`, `context.Canceled` maps to `StatusCancelled`.

**11. TTL auto-cleanup**

`Queue.StartCleanup()` runs a background goroutine with a `time.Ticker` that calls `store.DeleteTerminalBefore()`. Only deletes jobs in terminal states (`completed`, `failed`, `cancelled`) with a `completed_at` older than the TTL. The `idx_jobs_completed_at` index supports this query. Disabled when `CLAUDEGATE_JOB_TTL_HOURS=0`.

## Configuration

All configuration via environment variables. No config file is loaded by the application — use `.env` with `make run` or `systemd EnvironmentFile`.

| Variable | Default | Description |
|---|---|---|
| `CLAUDEGATE_LISTEN_ADDR` | `:8080` | Address and port to listen on. Use `127.0.0.1:8077` in production behind a reverse proxy. |
| `CLAUDEGATE_API_KEYS` | *(required)* | Comma-separated list of valid API keys. No default — process will not start without this. |
| `CLAUDEGATE_CLAUDE_PATH` | `/root/.local/bin/claude` | Path to the Claude CLI binary accessible by the service user. |
| `CLAUDEGATE_DEFAULT_MODEL` | `haiku` | Default model when job request omits `model`. Must be `haiku`, `sonnet`, or `opus`. |
| `CLAUDEGATE_CONCURRENCY` | `1` | Number of parallel workers. Each worker holds one Claude CLI process at a time. |
| `CLAUDEGATE_DB_PATH` | `claudegate.db` | Path to SQLite database file. Created on first run. |
| `CLAUDEGATE_QUEUE_SIZE` | `1000` | In-memory channel capacity. Jobs beyond this are rejected with HTTP 500. |
| `CLAUDEGATE_UNSAFE_NO_SECURITY_PROMPT` | `false` | Set `true` to disable the server-side security system prompt. Gives Claude full filesystem and shell access within service user permissions. |
| `CLAUDEGATE_JOB_TIMEOUT_MINUTES` | `0` | Per-job execution timeout in minutes. `0` disables timeout. |
| `CLAUDEGATE_CORS_ORIGINS` | *(empty)* | Comma-separated allowed CORS origins. `*` allows all origins. Empty disables CORS. |
| `CLAUDEGATE_JOB_TTL_HOURS` | `0` | Auto-delete terminal jobs older than this many hours. `0` disables cleanup. |
| `CLAUDEGATE_CLEANUP_INTERVAL_MINUTES` | `60` | How often the cleanup goroutine runs (in minutes). Only applies when TTL is enabled. |

## API Endpoints

All endpoints except `/` and `/api/v1/health` require header `X-API-Key: <key>`.

| Method | Path | Status | Description |
|---|---|---|---|
| `GET` | `/` | 200 | Embedded frontend SPA (playground + job history + API docs). No auth. |
| `POST` | `/api/v1/jobs` | 202 | Submit a job. Returns job object immediately. |
| `GET` | `/api/v1/jobs` | 200 | List jobs with pagination (`?limit=20&offset=0`). Max 100 per page. |
| `GET` | `/api/v1/jobs/{id}` | 200/404 | Poll job status and result. |
| `DELETE` | `/api/v1/jobs/{id}` | 204/404 | Delete job record from DB. |
| `POST` | `/api/v1/jobs/{id}/cancel` | 200/404/409 | Cancel a queued or processing job. Returns 409 if already terminal. |
| `GET` | `/api/v1/jobs/{id}/sse` | 200 | Stream SSE events: `status`, `chunk`, `result`. |
| `GET` | `/api/v1/health` | 200 | Health check. No auth required. |

SSE events: `status` (job moved to processing), `chunk` (incremental text), `result` (final — connection closes after this). If the job is already terminal when the client connects, a single `result` event is sent immediately.

## Deployment

**Dedicated user:** The service must run as a non-root user (e.g., `claudegate`). `--dangerously-skip-permissions` does not work as root.

**Claude CLI:** Must be installed and accessible to the service user (e.g., `/usr/local/bin/claude`). Set `CLAUDEGATE_CLAUDE_PATH` accordingly.

**Claude auth:** The service user needs a valid `~/.claude.json` with authentication tokens. Run `claude login` as that user before starting the service.

**Apache reverse proxy:** The production instance runs behind Apache on `anime-sanctuary.net/claudegate/` proxying to `127.0.0.1:8077`. Set `CLAUDEGATE_LISTEN_ADDR=127.0.0.1:8077`.

**systemd:** `claudegate.service` is included at the repo root. Uses `EnvironmentFile=/opt/claudegate/.env`. Adjust `ExecStart`, `WorkingDirectory`, and `EnvironmentFile` paths if your layout differs. `Restart=on-failure` combined with crash recovery ensures interrupted jobs are retried.

```bash
cp claudegate.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now claudegate
journalctl -u claudegate -f
```

## Testing

- Unit tests live in each package as `*_test.go` files.
- `testdata/mock-claude.sh` simulates the Claude CLI stream-json format for integration tests. Tests set `CLAUDEGATE_CLAUDE_PATH` to point to this script.
- SQLite tests use `:memory:` — no file created, no cleanup needed.
- Always run `make test` before pushing. All tests must pass.

## Code Conventions

- Standard Go idioms, no HTTP framework, no DI container.
- Errors wrapped with `fmt.Errorf("context: %w", err)` for stack tracing.
- Fail fast: validate early, return early on error (guard clauses).
- `log.Printf` for all logging — no external logging library.
- No CGO anywhere (`modernc.org/sqlite` is pure Go). Keep it that way for cross-compilation.
- `Store` interface used everywhere — never depend directly on `*SQLiteStore` outside the `job` package.
- Path parameters via `r.PathValue("id")` (Go 1.22 std routing).

## Frontend

Single-file SPA at `internal/api/static/index.html`, embedded via `//go:embed`. Served at `GET /` (no auth required).

- **Playground**: model selector, optional system prompt, prompt textarea, SSE streaming response. Uses `fetch` + `ReadableStream` (not `EventSource`) because custom headers (`X-API-Key`) are required.
- **Job History**: last 10 jobs, click to view result, delete button per row.
- **API Documentation**: collapsible endpoint cards with curl examples.
- **API key**: stored in `localStorage` (`cg_api_key`), validated live against `GET /api/v1/jobs?limit=1`.
- **JSON field**: the `Job` struct uses `json:"job_id"` for the ID — frontend must always use `job.job_id`, never `job.id`.
- **JSON mode**: `response_format: "json"` in the job request appends a JSON-only instruction to the system prompt and post-processes the result with `stripCodeFences` to remove markdown code fences LLMs sometimes add despite instructions.

## Known Limitations and Future Work

- No rate limiting on job submission.
- CORS is opt-in via `CLAUDEGATE_CORS_ORIGINS`. If not configured, cross-origin requests from SPAs will fail.
- Webhook payload is minimal: `job_id`, `status`, `result`, `error` — does not include the full job object.
- Jobs in the in-memory channel at shutdown time are lost. `Recovery()` on next start handles jobs that were already `processing`, but freshly enqueued jobs that never left the channel are dropped. True drain-on-shutdown would require flushing the channel before exit.
- No metrics or observability (Prometheus, OpenTelemetry, etc.).
- No model aliasing — `haiku`, `sonnet`, `opus` are passed as-is to the CLI. If Anthropic renames a model tier, `validModels` in both `config.go` and `model.go` must be updated (duplication).
