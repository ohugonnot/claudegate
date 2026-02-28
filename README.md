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

## Production

In production, bind to localhost and put a reverse proxy (Nginx, Caddy, etc.) in front:

```bash
CLAUDEGATE_LISTEN_ADDR=127.0.0.1:8080
```

## License

MIT — see [LICENSE](LICENSE).
