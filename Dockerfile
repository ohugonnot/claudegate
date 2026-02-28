# ── Build stage ──
FROM golang:1.26-bookworm AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o claudegate ./cmd/claudegate

# ── Runtime stage ──
FROM node:22-bookworm-slim

RUN apt-get update && apt-get install -y ca-certificates gosu && rm -rf /var/lib/apt/lists/*

# Install Claude Code CLI
RUN npm install -g @anthropic-ai/claude-code

# Create non-root user
RUN useradd -r -m -s /bin/bash claudegate

COPY --from=builder /build/claudegate /usr/local/bin/claudegate
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

WORKDIR /app

# Claude credentials must be mounted read-only at /claude-credentials at runtime:
#   docker run -v ~/.claude:/claude-credentials:ro ...
# The entrypoint copies them into the container's writable ~/.claude so Claude CLI
# can create temporary files (session-env, debug, plugins dirs) alongside them.
# Job database is stored under /app/data — mount a named volume to persist it:
#   docker run -v claudegate-data:/app/data ...
VOLUME ["/app/data"]

ENV CLAUDEGATE_LISTEN_ADDR=:8080 \
    CLAUDEGATE_CLAUDE_PATH=/usr/local/bin/claude \
    CLAUDEGATE_DB_PATH=/app/data/claudegate.db

EXPOSE 8080

# Entrypoint runs as root to copy credentials, then drops to claudegate user
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
    CMD curl -sf http://localhost:8080/api/v1/health || exit 1

ENTRYPOINT ["docker-entrypoint.sh"]
