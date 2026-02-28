# ── Build stage ──
FROM golang:1.26-bookworm AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o claudegate ./cmd/claudegate

# ── Runtime stage ──
FROM node:22-bookworm-slim

RUN apt-get update && apt-get install -y ca-certificates && rm -rf /var/lib/apt/lists/*

# Install Claude Code CLI
RUN npm install -g @anthropic-ai/claude-code

# Create non-root user
RUN useradd -r -m -s /bin/bash claudegate

COPY --from=builder /build/claudegate /usr/local/bin/claudegate

WORKDIR /app

# Claude credentials must be mounted at runtime:
#   docker run -v ~/.claude:/home/claudegate/.claude:ro ...
# Job database is stored under /app/data — mount a named volume to persist it:
#   docker run -v claudegate-data:/app/data ...
VOLUME ["/home/claudegate/.claude", "/app/data"]

ENV CLAUDEGATE_LISTEN_ADDR=:8080 \
    CLAUDEGATE_CLAUDE_PATH=/usr/local/bin/claude \
    CLAUDEGATE_DB_PATH=/app/data/claudegate.db

EXPOSE 8080

USER claudegate

ENTRYPOINT ["claudegate"]
