# ClaudeGate

HTTP gateway that wraps Claude Code CLI as a REST API with an async job queue.

## Features

- Async job queue with configurable concurrency
- Three result delivery modes: polling, SSE (Server-Sent Events), and webhook callback
- Multi-model support: haiku, sonnet, opus
- SQLite-backed job persistence with crash recovery
- API key authentication
- Optional system prompt and metadata per job

## Quick Start

```bash
# Install toolchain (requires mise)
make setup

# Build
make build

# Configure
cp .env.example .env
# Edit .env and set CLAUDEGATE_API_KEYS

# Run
make run
```

## Configuration

All configuration is via environment variables.

| Variable | Default | Description |
|---|---|---|
| `CLAUDEGATE_LISTEN_ADDR` | `:8080` | Address and port to listen on |
| `CLAUDEGATE_API_KEYS` | *(required)* | Comma-separated list of valid API keys |
| `CLAUDEGATE_CLAUDE_PATH` | `/root/.local/bin/claude` | Path to the Claude CLI binary |
| `CLAUDEGATE_DEFAULT_MODEL` | `haiku` | Default model when none is specified (`haiku`, `sonnet`, `opus`) |
| `CLAUDEGATE_CONCURRENCY` | `1` | Number of parallel workers |
| `CLAUDEGATE_DB_PATH` | `claudegate.db` | SQLite database file path |
| `CLAUDEGATE_QUEUE_SIZE` | `1000` | In-memory queue capacity |

## API Reference

All endpoints (except `/api/v1/health`) require the `X-API-Key` header.

### POST /api/v1/jobs

Submit a new job. Returns `202 Accepted` with the created job object.

```bash
curl -X POST http://localhost:8080/api/v1/jobs \
  -H "X-API-Key: your-secret-key-here" \
  -H "Content-Type: application/json" \
  -d '{
    "prompt": "Explain what a mutex is in one sentence.",
    "model": "haiku",
    "system_prompt": "Be concise.",
    "callback_url": "https://example.com/webhook"
  }'
```

Response:
```json
{
  "job_id": "a1b2c3d4-...",
  "prompt": "Explain what a mutex is in one sentence.",
  "model": "haiku",
  "status": "queued",
  "created_at": "2024-01-01T00:00:00Z"
}
```

### GET /api/v1/jobs/{id}

Poll a job's status and result.

```bash
curl http://localhost:8080/api/v1/jobs/a1b2c3d4-... \
  -H "X-API-Key: your-secret-key-here"
```

Response:
```json
{
  "job_id": "a1b2c3d4-...",
  "prompt": "Explain what a mutex is in one sentence.",
  "model": "haiku",
  "status": "completed",
  "result": "A mutex is a synchronization primitive that ensures only one goroutine accesses a shared resource at a time.",
  "created_at": "2024-01-01T00:00:00Z",
  "started_at": "2024-01-01T00:00:00.1Z",
  "completed_at": "2024-01-01T00:00:02Z"
}
```

Job statuses: `queued`, `processing`, `completed`, `failed`.

### GET /api/v1/jobs/{id}/sse

Stream job progress via Server-Sent Events. The connection closes automatically when the job finishes.

```bash
curl -N http://localhost:8080/api/v1/jobs/a1b2c3d4-.../sse \
  -H "X-API-Key: your-secret-key-here"
```

Events emitted:
- `status` — job moved to `processing`
- `chunk` — incremental text from the model
- `result` — final status, result, and error (connection closes after this)

### DELETE /api/v1/jobs/{id}

Delete a job record. Returns `204 No Content`.

```bash
curl -X DELETE http://localhost:8080/api/v1/jobs/a1b2c3d4-... \
  -H "X-API-Key: your-secret-key-here"
```

### GET /api/v1/health

Health check. No authentication required.

```bash
curl http://localhost:8080/api/v1/health
```

Response:
```json
{"status": "ok"}
```

## Docker

```bash
# Build image
docker build -t claudegate .

# Run
docker run -d \
  -p 8080:8080 \
  -e CLAUDEGATE_API_KEYS=your-secret-key-here \
  -e CLAUDEGATE_CLAUDE_PATH=/usr/local/bin/claude \
  -v $(pwd)/data:/data \
  -e CLAUDEGATE_DB_PATH=/data/claudegate.db \
  claudegate
```

## Systemd

The included `claudegate.service` assumes the binary lives at `/opt/claudegate/`. Adjust `ExecStart`, `WorkingDirectory`, and `EnvironmentFile` paths if your setup differs.

```bash
# Copy binary
make build
cp bin/claudegate /opt/claudegate/bin/claudegate

# Copy and configure environment
cp .env.example /opt/claudegate/.env
# Edit /opt/claudegate/.env

# Install and start the service
cp claudegate.service /etc/systemd/system/claudegate.service
systemctl daemon-reload
systemctl enable --now claudegate

# Check logs
journalctl -u claudegate -f
```

## Security

### How it works

ClaudeGate uses `--dangerously-skip-permissions` to run Claude CLI without interactive confirmation prompts. This is required for API/daemon usage but means Claude can execute any action the system user has permissions for.

### Built-in protections

- **Security system prompt (default ON):** A server-side system prompt is prepended to every job, instructing Claude to only provide text responses and refuse filesystem, shell, or network operations. This is a soft guardrail — it relies on Claude following instructions, not a technical sandbox.
- **API key authentication:** All endpoints (except health) require a valid `X-API-Key` header. Keys are compared using constant-time comparison to prevent timing attacks.
- **Dedicated system user:** The service should run as a non-root user with minimal permissions. Never run as root.
- **Localhost binding:** By default, configure `CLAUDEGATE_LISTEN_ADDR=127.0.0.1:8080` and use a reverse proxy for external access.

### Disabling the security prompt

Set `CLAUDEGATE_UNSAFE_NO_SECURITY_PROMPT=true` to remove the security system prompt. This gives Claude full access to the system (within the service user's permissions). Only do this if:
- You fully trust all API key holders
- The service user has minimal filesystem access
- You have network-level access controls in place

### Recommendations for production

- Use strong, randomly generated API keys (32+ characters)
- Rotate API keys regularly
- Run behind a reverse proxy with TLS
- Monitor logs for suspicious prompts
- Consider network-level restrictions (firewall, VPN)
- Run the service user with the most restrictive permissions possible

## Production

In production, bind to localhost and put a reverse proxy (Nginx, Caddy, etc.) in front:

```bash
CLAUDEGATE_LISTEN_ADDR=127.0.0.1:8080
```

## Architecture & Code Documentation

### Overview

```
POST /api/v1/jobs → API Handler → SQLite Store → Queue (chan) → Worker (claude CLI) → SQLite
                                                                                      ↓
                                                            SSE stream ← notify ← Queue
                                                            Webhook POST → callback_url
```

A job is created synchronously in SQLite and enqueued in memory. Workers pick it up, call the Claude CLI, stream chunks back via SSE, and write the final result to SQLite. Webhooks fire-and-forget after completion.

### Project Structure

```
claudegate/
├── cmd/claudegate/
│   └── main.go              # Entry point: wiring, startup, graceful shutdown
├── internal/
│   ├── api/
│   │   ├── handler.go       # HTTP handlers for all REST endpoints
│   │   ├── middleware.go    # Auth, request ID, logging middleware
│   │   └── sse.go           # Server-Sent Events streaming handler
│   ├── config/
│   │   └── config.go        # Configuration loaded from environment variables
│   ├── job/
│   │   ├── model.go         # Job struct, Status type, CreateRequest + validation
│   │   ├── store.go         # Store interface (abstracts the storage backend)
│   │   └── sqlite.go        # SQLite implementation of Store
│   ├── queue/
│   │   └── queue.go         # Buffered channel queue, worker pool, SSE fan-out
│   ├── webhook/
│   │   └── webhook.go       # Async webhook delivery with exponential backoff
│   └── worker/
│       └── worker.go        # Claude CLI execution and stream-json parsing
├── testdata/
│   └── mock-claude.sh       # Shell mock of Claude CLI for tests
├── Dockerfile               # Multi-stage build producing a static binary
├── Makefile                 # Build, test, lint, run targets
└── claudegate.service       # systemd unit file for daemon mode
```

### Component Details

#### `cmd/claudegate/main.go` — Entry point

**Role:** Wires all dependencies together, starts the server, handles shutdown.

**Initialization order:**
1. `config.Load()` — fail-fast if any required env var is missing or invalid
2. `job.NewSQLiteStore()` — opens the DB, runs migrations, enables WAL mode
3. `queue.New()` — allocates the buffered channel and subscriber map
4. `q.Recovery()` — resets interrupted jobs before workers start (crash recovery)
5. `q.Start(ctx)` — launches N worker goroutines
6. `api.NewHandler()` + `RegisterRoutes()` + middleware chain — sets up HTTP
7. `srv.ListenAndServe()` — starts accepting connections

**Graceful shutdown:** A goroutine waits for `SIGINT` or `SIGTERM`. On signal it calls `cancel()` to stop all workers (they drain the context), then `srv.Shutdown()` with a 10-second timeout to let in-flight HTTP requests finish.

**Middleware chain order (outer to inner):**
```
LoggingMiddleware → RequestIDMiddleware → AuthMiddleware → mux
```
Logging wraps everything so it captures the final status code. RequestID is set before auth so every request — including rejections — gets an ID. Auth sits just before the mux so all business routes are protected.

---

#### `internal/config/config.go` — Configuration

**Role:** Loads and validates all runtime configuration from environment variables.

**Technical decisions:**
- **Env vars only, no config file:** Follows the 12-factor app principle. The deployment environment (systemd `EnvironmentFile`, Docker `-e`, k8s secrets) is responsible for injecting values — the app itself has no `.env` loader.
- **Fail-fast validation:** Every constraint (non-empty API keys, positive concurrency, valid model name) is checked at startup. The process exits immediately with a clear error rather than discovering a misconfiguration at runtime.
- `CLAUDEGATE_API_KEYS` accepts a comma-separated list so multiple clients can use different keys without a restart.

---

#### `internal/job/model.go` — Job model

**Role:** Defines the `Job` struct, status constants, and the `CreateRequest` input type with its validation logic.

**Technical decisions:**
- **`Status` as a `string` type:** Stored and serialized as human-readable strings (`"queued"`, `"processing"`, etc.). Easier to inspect directly in the DB or in API responses than integer codes.
- **`json.RawMessage` for `Metadata`:** The gateway passes metadata through without parsing or validating it. The caller owns the schema. Using `json.RawMessage` avoids double-encoding and preserves the exact original JSON.
- **Pointer timestamps (`*time.Time`) for `StartedAt` / `CompletedAt`:** These fields are absent until the job reaches the relevant state. Pointers serialize to `null` in JSON and map cleanly to nullable `DATETIME` columns in SQLite.

---

#### `internal/job/store.go` — Store interface

**Role:** Declares the `Store` interface that decouples all callers from the concrete storage implementation.

**Technical decisions:**
- **Interface over concrete type:** `Queue`, `Handler`, and `main` all depend on `job.Store`, not on `*SQLiteStore`. This makes it straightforward to swap the backend (e.g., in-memory for tests, Postgres for a future migration) without touching any caller.
- The interface surface is minimal — only the operations actually needed — which keeps both the interface and any future implementations simple.

---

#### `internal/job/sqlite.go` — SQLite implementation

**Role:** Implements `Store` using a local SQLite file. Handles schema migration and crash recovery.

**Technical decisions:**
- **SQLite over Postgres/Redis:** Zero external dependencies. A single file holds all state. More than sufficient for the workload where the bottleneck is the Claude CLI, not the DB.
- **`modernc.org/sqlite` (pure Go):** No CGO required. The binary cross-compiles to any target with `CGO_ENABLED=0` and works out of the box in Alpine/scratch containers. The standard `mattn/go-sqlite3` requires a C compiler.
- **WAL mode (`PRAGMA journal_mode=WAL`):** Write-Ahead Logging allows concurrent readers while a write is in progress. Without it, any write would lock the entire file, blocking polling requests while a job result is being written.
- **Auto-migration on startup:** `migrate()` uses `CREATE TABLE IF NOT EXISTS` so it is idempotent and safe to run every time. No external migration tool needed.
- **`ResetProcessing`:** Finds all jobs stuck in `"processing"` (left there by a previous crash) and moves them back to `"queued"`. Returns their IDs so the caller can re-enqueue them before starting workers, guaranteeing at-least-once execution.

---

#### `internal/worker/worker.go` — Claude CLI execution

**Role:** Spawns the Claude CLI as a subprocess, streams its output, and returns the final result.

**Technical decisions:**
- **`filteredEnv()` — critical:** The worker strips every environment variable whose name starts with `CLAUDE` before exec-ing the CLI. The Claude CLI detects an active session via `CLAUDE_*` variables and refuses to start a nested one (error: "nested session"). Since claudegate itself runs inside a Claude Code session during development, this filter is mandatory.
- **`stream-json` format:** The CLI is invoked with `--output-format stream-json`. Each line is a self-contained JSON object. Two message types are handled:
  - `"assistant"` — contains a `content` array of text blocks; extracted and forwarded as SSE chunks.
  - `"result"` — the final aggregated response; stored as the job result.
- **Stdout pipe + line scanner:** The output is consumed incrementally. Each line is parsed as it arrives so chunks are forwarded to SSE subscribers in real time, not buffered until the process exits.
- **Context cancellation:** `exec.CommandContext` is used, so cancelling the context (on SIGTERM or client disconnect) sends SIGKILL to the subprocess automatically.

---

#### `internal/queue/queue.go` — Job queue & worker pool

**Role:** Manages the in-memory job queue, the worker goroutines, and the SSE subscriber fan-out.

**Technical decisions:**
- **Buffered channel as queue:** `make(chan string, cfg.QueueSize)` is all the queueing infrastructure needed. Job IDs (not full job structs) are enqueued — the worker fetches details from SQLite when it picks up the job. No Redis, no RabbitMQ. The bottleneck is the Claude CLI (seconds per job), not channel throughput.
- **N worker goroutines:** `cfg.Concurrency` goroutines each run `runWorker`, which blocks on `<-q.jobs`. The pool size is static and set at startup.
- **SSE fan-out via `map[string][]chan SSEEvent`:** Each job can have multiple concurrent SSE subscribers (multiple browser tabs, monitoring tools). The map is protected by a `sync.RWMutex` — reads (notify) hold a read lock, writes (subscribe/unsubscribe) hold a write lock.
- **Non-blocking notify:** `notify()` uses a `select` with a `default` branch when sending to subscriber channels. A slow or disconnected client never blocks the worker.
- **`notifyAndClose`:** Sends the final `"result"` event then deletes the job's entry from the map and closes all channels. Closed channels cause the SSE handler's `range` to exit cleanly.

---

#### `internal/webhook/webhook.go` — Webhook delivery

**Role:** POSTs the job result to a caller-supplied `callback_url` after completion.

**Technical decisions:**
- **Fire-and-forget goroutine:** `Send()` launches `send()` in a new goroutine and returns immediately. The worker is never blocked waiting for the remote server.
- **3 retries with exponential backoff (1s → 2s → 4s):** Transient network failures or temporary server errors are retried automatically. After three failures the error is logged and delivery is abandoned — there is no dead-letter queue.
- **30-second per-request timeout:** Prevents the goroutine from hanging indefinitely on a slow or unresponsive callback server.

---

#### `internal/api/handler.go` — HTTP handlers

**Role:** Implements the five REST endpoints using Go's standard `net/http` package.

**Technical decisions:**
- **Go 1.22 native routing with method+path patterns:** `"POST /api/v1/jobs"` and `"GET /api/v1/jobs/{id}"` — no external router needed. `r.PathValue("id")` extracts path parameters.
- **`POST` returns `202 Accepted`:** The job is persisted and enqueued but not yet processed. `202` is the correct HTTP semantic for async operations.
- **Validation before creation:** `req.Validate()` runs before any DB write. Invalid requests never touch the store.
- **`store.Create` then `queue.Enqueue`:** The job is written to SQLite first. If the enqueue fails (queue full), the job record already exists and can be recovered at next startup via `ResetProcessing`. The reverse order would lose the job on queue failure.

---

#### `internal/api/middleware.go` — Middleware stack

**Role:** Provides authentication, request tracing, and structured access logging.

**Technical decisions:**
- **`subtle.ConstantTimeCompare` in `AuthMiddleware`:** A naive string comparison (`==`) short-circuits on the first mismatched byte, leaking timing information that an attacker could exploit to guess valid keys one byte at a time. Constant-time comparison always takes the same duration regardless of where the mismatch occurs.
- **`/api/v1/health` exempt from auth:** Health checks must be reachable by load balancers and monitoring systems that don't have (or shouldn't need) an API key. The exemption is checked by path string before any key lookup.
- **`RequestIDMiddleware`:** Generates a UUID per request, sets it on the response as `X-Request-ID`, and stores it in the request context. Downstream handlers and logs can correlate entries for the same request.
- **`statusResponseWriter`:** A thin wrapper around `http.ResponseWriter` that captures the status code written by the handler. Required because `http.ResponseWriter` does not expose the code after `WriteHeader` is called, but `LoggingMiddleware` needs it to log the final status.

---

#### `internal/api/sse.go` — Server-Sent Events

**Role:** Streams live job progress to clients over a persistent HTTP connection.

**Technical decisions:**
- **`http.Flusher` check before starting:** Not all `http.ResponseWriter` implementations support streaming (e.g., some test recorders). The check prevents a silent hang — if flushing is not available, the handler returns an error immediately.
- **Already-terminal shortcut:** If the job is `completed` or `failed` by the time the client connects, the handler writes a single `"result"` event and closes the connection. No subscription needed. This handles the common case where the client polls SSE after the job has already finished.
- **Subscribe → send initial status → stream:** After subscribing, the handler sends the current job status so the client has an initial state even if no new events arrive immediately. Then it loops over the channel until it is closed (job finished) or the client disconnects (`r.Context().Done()`).
- **`defer h.queue.Unsubscribe`:** Guarantees the channel is removed from the fan-out map when the handler exits, regardless of whether the client disconnected or the job completed. Prevents a channel leak.

---

### Infrastructure Files

#### `Makefile` — Build automation

Targets: `setup` (installs `mise` and Go toolchain), `build` (compiles binary to `bin/claudegate`), `test` (runs all tests with `-count=1` to bypass cache), `lint` (runs `golangci-lint`), `run` (build then execute), `clean` (removes `bin/`).

#### `Dockerfile` — Multi-stage build

**Stage 1 (`builder`):** Uses the official `golang` image. Copies `go.mod`/`go.sum` first to cache the module download layer, then copies source and builds with `CGO_ENABLED=0` for a fully static binary.

**Stage 2 (runtime):** `debian:bookworm-slim` with only `ca-certificates` added (needed for outbound HTTPS webhook calls). The static binary is copied in. Result: a small image with no Go toolchain.

#### `claudegate.service` — systemd unit

Runs the binary as a `simple` service. `EnvironmentFile=/opt/claudegate/.env` injects configuration. `Restart=on-failure` with a 5-second delay restarts the process on crashes (combined with `ResetProcessing`, jobs interrupted by a crash are retried automatically). Logs go to the systemd journal via `StandardOutput=journal`.

#### `testdata/mock-claude.sh` — CLI mock for tests

A shell script that accepts the same arguments as the real Claude CLI and emits two lines of `stream-json`: one `"assistant"` message and one `"result"` message. Used in integration tests by setting `CLAUDEGATE_CLAUDE_PATH` to this script. Includes a 0.1-second sleep to simulate realistic latency.

---

### Design Decisions

| Decision | Rationale |
|---|---|
| **No HTTP framework** | Go 1.22 stdlib routing covers method matching and path parameters. No dependency, no magic. |
| **SQLite over Postgres/Redis** | Zero config, embedded in the binary's working directory, one file to back up. The bottleneck is the Claude CLI (seconds/job), not DB throughput. |
| **Buffered channel over a message broker** | Same reasoning: the channel is orders of magnitude faster than any external queue. Simplicity wins. |
| **`modernc.org/sqlite` (pure Go)** | `CGO_ENABLED=0` enables cross-compilation and scratch/Alpine containers without a C toolchain. |
| **API key auth over OAuth/JWT** | This is a machine-to-machine API. API keys are simpler to issue, rotate, and validate. No token expiry, no refresh flow. |
| **`stream-json` parsing** | Native output format of the Claude CLI. Parsing it directly avoids wrapping the CLI in a PTY or scraping human-readable output. |
| **Job ID only in channel** | Enqueueing the ID rather than the full `Job` struct keeps the channel payload tiny and ensures workers always read the latest state from the DB. |
| **Constant-time key comparison** | Timing attacks on string equality are a real class of vulnerability for authentication secrets. `subtle.ConstantTimeCompare` costs nothing and closes the vector. |

## License

MIT — see [LICENSE](LICENSE).
