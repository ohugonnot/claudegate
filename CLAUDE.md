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

- **internal/worker** (`worker.go`): Execs claude CLI with `--print --verbose --output-format stream-json --dangerously-skip-permissions`. Parses stdout line by line (NDJSON). Calls `onChunk` for each `"assistant"` message, returns the `"result"` string at the end. Strips all `CLAUDE*` env vars from the subprocess. **Streaming granularity:** the CLI emits one complete `assistant` message per response — not token-by-token. Clients receive a single `chunk` SSE event containing the full text, followed by the `result` event. True token streaming is not possible via the CLI (it would require calling the Anthropic API directly, which defeats the purpose of using a Max subscription).

- **internal/webhook** (`webhook.go`): Fire-and-forget `goroutine`. 8 retries max with full-jitter exponential backoff (base 1s, cap 5 min). 30s per-request timeout. No dead-letter queue — failures are logged and dropped.

- **internal/api** (`handler.go`, `middleware.go`, `sse.go`, `static/index.html`): Eight routes on Go 1.22 native mux (method+path patterns). Middleware chain: `CORSMiddleware → LoggingMiddleware → RequestIDMiddleware → AuthMiddleware → mux`. CORS is outermost so OPTIONS preflight bypasses auth. Auth uses `subtle.ConstantTimeCompare`. `/api/v1/health` and `/` are exempt from auth. The frontend SPA (`static/index.html`) is embedded at compile time via `//go:embed` — no filesystem access at runtime.

## Critical Implementation Details

Things a developer MUST know before touching the code:

**1. CLAUDE env vars — do not remove the filter**

`worker.go:filteredEnv()` strips every env var starting with `CLAUDE` before exec-ing the CLI. The Claude CLI detects a parent session via `CLAUDE_*` vars and refuses to start with "nested session" error. This is mandatory when developing inside Claude Code. Never remove this filter.

**2. `--verbose` is required**

The CLI must be called with `--verbose` alongside `--print --output-format stream-json`. Without it, the CLI refuses to emit the stream-json format.

**3. CLI streaming is coarse-grained — one chunk per response**

The Claude CLI does not stream tokens progressively. With `--output-format stream-json`, it emits a single `assistant` JSON message containing the complete response text once generation is done. The SSE `chunk` event therefore carries the full response in one shot, not word-by-word. This is an inherent limitation of the CLI approach — the entire point of this gateway is to reuse a Claude Max subscription (OAuth), which precludes calling the Anthropic streaming API directly. Do not attempt to "fix" this by switching to direct API calls.

**3. `--dangerously-skip-permissions` and the root restriction**

Required for daemon/non-interactive mode. The flag does NOT work when running as root — the service must run as a dedicated non-root user. If you see permission-related startup failures, check which user owns the process.

**4. Security system prompt**

`config.go:defaultSecurityPrompt` is prepended to every job's system prompt in `queue.go:processJob()`. It instructs Claude to refuse filesystem, shell, and network operations. This is a soft guardrail (LLM instruction, not a technical sandbox). Disable with `CLAUDEGATE_UNSAFE_NO_SECURITY_PROMPT=true`. If both config security prompt and job `system_prompt` are set, they are concatenated: `securityPrompt + "\n\n" + jobSystemPrompt`.

**5. SQLite WAL mode and busy_timeout**

Enabled at startup with `PRAGMA journal_mode=WAL` followed by `PRAGMA busy_timeout = 10000`. WAL avoids full-file write locks under concurrent reads. `busy_timeout` prevents `SQLITE_BUSY` errors under concurrent worker writes — SQLite will retry for up to 10s instead of returning immediately. Do not remove either pragma.

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

**12. Docker entrypoint and credentials**

The `docker-entrypoint.sh` script runs as root, copies `.credentials.json` (and optionally `settings.json`) from the read-only mount at `/claude-credentials` into `/home/claudegate/.claude/`, sets ownership to `claudegate:claudegate`, then uses `gosu claudegate` to drop privileges before exec-ing the binary. This is necessary because: (a) host credential files have `600 root:root` permissions, (b) Claude CLI requires a writable `~/.claude/` directory for session state — it creates `session-env/`, `debug/`, and `plugins/` subdirectories at runtime. Mounting credentials read-only directly at `~/.claude/` fails; the entrypoint copy pattern solves this.

**13. Claude OAuth token lifecycle**

Claude CLI uses OAuth tokens that expire every ~8 hours. The health endpoint (`GET /api/v1/health`) reads `~/.claude/.credentials.json` and reports token status: `claude_auth` ("valid", "expired", or "unknown"), `token_expires_at` (RFC3339), and `token_expires_in` (Go duration string like "7h30m0s"). The frontend displays this as a badge in the header bar (green > 2h, yellow <= 2h, red = expired), refreshed every 60s.

**14. Worker error messages from CLI**

When the Claude CLI exits with an error, the actual error message is often in stdout (JSON stream) rather than stderr. `worker.go` now falls back to `finalResult` (from the parsed JSON stream) when stderr is empty. This ensures auth errors like "OAuth token has expired" are surfaced to the user instead of a blank "stderr: " message.

## Configuration

All configuration via environment variables. No config file is loaded by the application — use `.env` with `make run` or `systemd EnvironmentFile`.

| Variable | Default | Description |
|---|---|---|
| `CLAUDEGATE_LISTEN_ADDR` | `:8080` | Address and port to listen on. Use `127.0.0.1:8077` in production behind a reverse proxy. |
| `CLAUDEGATE_API_KEYS` | *(required)* | Comma-separated list of valid API keys. No default — process will not start without this. |
| `CLAUDEGATE_CLAUDE_PATH` | `/usr/local/bin/claude` | Path to the Claude CLI binary accessible by the service user. |
| `CLAUDEGATE_DEFAULT_MODEL` | `haiku` | Default model when job request omits `model`. Must be `haiku`, `sonnet`, or `opus`. |
| `CLAUDEGATE_CONCURRENCY` | `1` | Number of parallel workers. Each worker holds one Claude CLI process at a time. |
| `CLAUDEGATE_DB_PATH` | `claudegate.db` | Path to SQLite database file. Created on first run. |
| `CLAUDEGATE_QUEUE_SIZE` | `1000` | In-memory channel capacity. Jobs beyond this are rejected with HTTP 500. |
| `CLAUDEGATE_UNSAFE_NO_SECURITY_PROMPT` | `false` | Set `true` to disable the server-side security system prompt. Gives Claude full filesystem and shell access within service user permissions. |
| `CLAUDEGATE_JOB_TIMEOUT_MINUTES` | `0` | Per-job execution timeout in minutes. `0` disables timeout. |
| `CLAUDEGATE_CORS_ORIGINS` | *(empty)* | Comma-separated allowed CORS origins. `*` allows all origins. Empty disables CORS. |
| `CLAUDEGATE_JOB_TTL_HOURS` | `0` | Auto-delete terminal jobs older than this many hours. `0` disables cleanup. |
| `CLAUDEGATE_CLEANUP_INTERVAL_MINUTES` | `60` | How often the cleanup goroutine runs (in minutes). Only applies when TTL is enabled. |
| `CLAUDEGATE_DISABLE_KEEPALIVE` | `false` | Set `true` to disable the automatic tmux keepalive session for OAuth token refresh. |
| `CLAUDEGATE_RATE_LIMIT` | `0` | Max job submissions per second per IP. `0` disables rate limiting. |

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
| `GET` | `/api/v1/health` | 200 | Health check + Claude token status. No auth required. Returns `claude_auth`, `token_expires_at`, `token_expires_in`. |

SSE events: `status` (job moved to processing), `chunk` (incremental text), `result` (final — connection closes after this). If the job is already terminal when the client connects, a single `result` event is sent immediately.

## Deployment

**Dedicated user:** The service must run as a non-root user (e.g., `claudegate`). `--dangerously-skip-permissions` does not work as root.

**Claude CLI:** Must be installed and accessible to the service user (e.g., `/usr/local/bin/claude`). Set `CLAUDEGATE_CLAUDE_PATH` accordingly.

**Claude auth:** The service user needs valid OAuth tokens in `~/.claude/.credentials.json`. Run `claude` interactively as that user and use `/login` to authenticate. Tokens expire every ~8 hours. The binary automatically starts a `claude-keepalive` tmux session at startup to keep the token refreshed indefinitely (see Token Auto-Refresh section below).

**Apache reverse proxy:** The production instance runs behind Apache on `anime-sanctuary.net/claudegate/` proxying to `127.0.0.1:8077`. Set `CLAUDEGATE_LISTEN_ADDR=127.0.0.1:8077`.

**systemd:** `claudegate.service` is included at the repo root. Uses `EnvironmentFile=/opt/claudegate/.env`. Adjust `ExecStart`, `WorkingDirectory`, and `EnvironmentFile` paths if your layout differs. `Restart=on-failure` combined with crash recovery ensures interrupted jobs are retried.

```bash
cp claudegate.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now claudegate
journalctl -u claudegate -f
```

**Docker:** The Dockerfile uses `node:22-bookworm-slim` as runtime base (Node.js is required because Claude CLI is a Node.js package). Claude CLI is pre-installed via `npm install -g @anthropic-ai/claude-code`. The `docker-entrypoint.sh` runs as root to copy credentials from the read-only mount `/claude-credentials` to a writable `~/.claude/` inside the container, then drops privileges to the `claudegate` user via `gosu`. The data volume `/app/data` must be writable by the `claudegate` user (UID from `useradd -r`).

Key learning: Claude CLI needs a WRITABLE `~/.claude/` directory — it creates `session-env/`, `debug/`, `plugins/` subdirectories at runtime. Mounting credentials read-only directly at `~/.claude/` fails. The entrypoint copy pattern solves this.

```bash
docker build -t claudegate .
docker run -d \
  -p 8080:8080 \
  -v ~/.claude:/claude-credentials:ro \
  -v claudegate-data:/app/data \
  -e CLAUDEGATE_API_KEYS=your-key \
  claudegate
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
- Frontend HTML built with string concatenation (not template literals) to avoid whitespace issues in `<pre>` and code blocks. Template literal indentation creates visible extra whitespace inside `<pre>` tags — learned the hard way.

## Frontend

Single-file SPA at `internal/api/static/index.html`, embedded via `//go:embed`. Served at `GET /` (no auth required).

- **Playground**: model selector, optional system prompt, prompt textarea, SSE streaming response. Uses `fetch` + `ReadableStream` (not `EventSource`) because custom headers (`X-API-Key`) are required.
- **Job History**: last 10 jobs, click to view result, delete button per row.
- **API Documentation**: collapsible endpoint cards with curl examples, inline parameter documentation (POST body params, query params, path params), and a "Try it" panel on every endpoint with input fields, a "Send Request" button, a response display area showing HTTP status badge, response headers, and JSON-highlighted body.
- **Draggable splitter**: resizable left/right panel layout. Position saved in `localStorage` under key `cg_split_pos`. Min 20% / max 80% constraint. Hidden on mobile (single-column layout).
- **Getting Started guide**: 6 collapsible steps — Install CLI, Install Go, Build, Configure, Run & Test, Reverse Proxy.
- **Integration Examples**: 4 collapsible code examples with PrismJS syntax highlighting — SSE Streaming (JavaScript), Polling + Webhook (Python/httpx), Webhook (Node.js/Express), Polling (PHP/Guzzle).
- **Token status badge**: displays Claude OAuth token remaining time in the header bar. Green (> 2h), yellow (<= 2h), red (expired). Auto-refreshes every 60s via the health endpoint.
- **Elapsed timer**: shows real-time elapsed time (e.g. `14.3s`, `2m 15s`) during SSE streaming in the playground. Starts on Send, stops on result/error.
- **PrismJS**: loaded from CDN (tomorrow theme) for syntax highlighting in integration examples (languages: bash, javascript, php, python, json).
- **API key**: stored in `localStorage` (`cg_api_key`), validated live against `GET /api/v1/jobs?limit=1`.
- **JSON field**: the `Job` struct uses `json:"job_id"` for the ID — frontend must always use `job.job_id`, never `job.id`.
- **JSON mode**: `response_format: "json"` in the job request appends a JSON-only instruction to the system prompt and post-processes the result with `stripCodeFences` to remove markdown code fences LLMs sometimes add despite instructions.
- **Response schema**: API doc response examples show ALL Job fields including optional ones (`system_prompt`, `callback_url`, `response_format`, `metadata`, `result`, `error`, `started_at`, `completed_at`). These fields use `omitempty` in Go — they are omitted from JSON when empty, not missing from the schema.

## Known Limitations and Future Work

- No rate limiting on job submission.
- CORS is opt-in via `CLAUDEGATE_CORS_ORIGINS`. If not configured, cross-origin requests from SPAs will fail.
- Webhook payload is minimal: `job_id`, `status`, `result`, `error` — does not include the full job object.
- Jobs in the in-memory channel at shutdown time are lost. `Recovery()` on next start handles jobs that were already `processing`, but freshly enqueued jobs that never left the channel are dropped. True drain-on-shutdown would require flushing the channel before exit.
- No metrics or observability (Prometheus, OpenTelemetry, etc.).
- **SSE streaming is coarse-grained:** clients receive one `chunk` event with the complete response, not a token-by-token stream. The CLI emits a single `assistant` message once generation completes. This is by design — the gateway exists to leverage a Claude Max subscription (OAuth), which makes direct Anthropic API streaming calls irrelevant.
- No model aliasing — `haiku`, `sonnet`, `opus` are passed as-is to the CLI. If Anthropic renames a model tier, `validModels` in both `config.go` and `model.go` must be updated (duplication).
- Docker image is ~580MB due to the Node.js runtime required for Claude CLI.
- PrismJS is loaded from CDN — the frontend requires internet access for syntax highlighting in integration examples. API functionality works fully offline.

## Token Auto-Refresh — tmux Keepalive

**Verified 2026-03-01.** Claude OAuth tokens expire every ~8 hours. An interactive `claude` session running in a persistent tmux window automatically refreshes the token before expiry, keeping workers running indefinitely without manual re-authentication.

**Mechanism:** The Claude CLI auto-refreshes OAuth tokens while an interactive session is alive. Two refreshes were observed over 24h of monitoring, each extending the token by ~8h (triggered ~20 minutes before expiry).

**Implementation:** `cmd/claudegate/keepalive.go` — `startKeepalive(claudePath)` is called at startup from `main.go`. It checks for tmux, skips silently if the session already exists (idempotent across restarts), and logs the result. Requires `tmux` installed on the host or in the container. Disable with `CLAUDEGATE_DISABLE_KEEPALIVE=true`.

**Monitoring:** `/opt/claudegate/scripts/token-monitor.sh` logs to `/home/claudegate/token-monitor.log` every 30 minutes. Look for `TOKEN REFRESHED` entries.
