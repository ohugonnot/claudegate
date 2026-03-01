package main

import (
	"log/slog"
	"os/exec"
)

const keepaliveSession = "claude-keepalive"

// startKeepalive launches a background tmux session running an interactive
// Claude CLI session. The interactive session auto-refreshes OAuth tokens
// (~8h expiry) while alive, preventing worker failures in long-running deployments.
//
// Fails silently — if tmux is unavailable or the session already exists, the
// service continues normally. Disable with CLAUDEGATE_DISABLE_KEEPALIVE=true.
func startKeepalive(claudePath string) {
	if _, err := exec.LookPath("tmux"); err != nil {
		slog.Warn("keepalive: tmux not found, token auto-refresh disabled")
		return
	}

	// Session already exists (e.g. service restart) — nothing to do.
	if err := exec.Command("tmux", "has-session", "-t", keepaliveSession).Run(); err == nil {
		slog.Info("keepalive: session already running")
		return
	}

	if err := exec.Command("tmux", "new-session", "-d", "-s", keepaliveSession, claudePath).Run(); err != nil {
		slog.Warn("keepalive: failed to start session", "error", err)
		return
	}

	slog.Info("keepalive: started tmux session", "session", keepaliveSession)
}
