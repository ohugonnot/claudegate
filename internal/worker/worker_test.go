package worker

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// mockClaudePath returns the absolute path to the mock-claude.sh script
// in the testdata directory at the root of the repository.
func mockClaudePath(t *testing.T) string {
	t.Helper()
	// __file__ of this test is internal/worker/worker_test.go
	// testdata/ is two levels up (../../testdata).
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "testdata", "mock-claude.sh")
}

type testChunkWriter struct {
	chunks []string
}

func (w *testChunkWriter) WriteChunk(text string) {
	w.chunks = append(w.chunks, text)
}

func TestRun_MockClaude_ReturnsResult(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	claudePath := mockClaudePath(t)

	cw := &testChunkWriter{}

	result, err := Run(ctx, claudePath, "haiku", "say hello", "", cw)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	const want = "Hello from mock Claude!"
	if result != want {
		t.Errorf("result = %q, want %q", result, want)
	}

	// The mock emits one assistant chunk.
	if len(cw.chunks) == 0 {
		t.Error("expected at least one chunk, got none")
	}
}

func TestRun_ContextCancelled_ReturnsError(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately before Run starts.
	cancel()

	claudePath := mockClaudePath(t)

	_, err := Run(ctx, claudePath, "haiku", "say hello", "", nil)
	if err == nil {
		t.Fatal("expected error when context is cancelled, got nil")
	}
}

func TestRun_CLIError_ReturnsError(t *testing.T) {
	t.Parallel()
	// Use a script that exits non-zero to simulate CLI failure.
	tmpDir := t.TempDir()
	script := filepath.Join(tmpDir, "fail-claude.sh")
	content := "#!/bin/bash\necho '{\"type\":\"result\",\"result\":\"auth failed\",\"model\":\"haiku\",\"stop_reason\":\"end_turn\"}'\nexit 1\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ctx := context.Background()
	_, err := Run(ctx, script, "haiku", "hello", "", nil)
	if err == nil {
		t.Fatal("expected error from non-zero exit, got nil")
	}
}

func TestRun_LargeOutput_HandledGracefully(t *testing.T) {
	t.Parallel()
	// Script that emits many chunks — verifies we handle large output without panicking.
	tmpDir := t.TempDir()
	script := filepath.Join(tmpDir, "large-claude.sh")
	// Write 100 assistant lines + result.
	var sb strings.Builder
	sb.WriteString("#!/bin/bash\n")
	for i := 0; i < 100; i++ {
		sb.WriteString(`echo '{"type":"assistant","content":[{"type":"text","text":"chunk"}]}'` + "\n")
	}
	sb.WriteString(`echo '{"type":"result","result":"done","model":"haiku","stop_reason":"end_turn"}'` + "\n")
	if err := os.WriteFile(script, []byte(sb.String()), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ctx := context.Background()
	cw := &testChunkWriter{}
	result, err := Run(ctx, script, "haiku", "hello", "", cw)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result != "done" {
		t.Errorf("result = %q, want %q", result, "done")
	}
	if len(cw.chunks) != 100 {
		t.Errorf("chunks = %d, want 100", len(cw.chunks))
	}
}
