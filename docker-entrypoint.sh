#!/bin/sh
set -e

CLAUDE_HOME="/home/claudegate/.claude"
CREDS_MOUNT="/claude-credentials"

# Copy credentials from read-only mount to writable home
if [ -f "$CREDS_MOUNT/.credentials.json" ]; then
  mkdir -p "$CLAUDE_HOME"
  cp "$CREDS_MOUNT/.credentials.json" "$CLAUDE_HOME/.credentials.json"
  [ -f "$CREDS_MOUNT/settings.json" ] && cp "$CREDS_MOUNT/settings.json" "$CLAUDE_HOME/settings.json"
  chown -R claudegate:claudegate "$CLAUDE_HOME"
  chmod 600 "$CLAUDE_HOME/.credentials.json"
fi

# Ensure data dir is writable
chown -R claudegate:claudegate /app/data 2>/dev/null || true

# Drop privileges and exec
exec gosu claudegate claudegate "$@"
