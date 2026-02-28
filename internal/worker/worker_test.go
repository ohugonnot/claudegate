package worker

import (
	"context"
	"path/filepath"
	"runtime"
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

func TestRun_MockClaude_ReturnsResult(t *testing.T) {
	ctx := context.Background()
	claudePath := mockClaudePath(t)

	var chunks []string
	onChunk := func(text string) {
		chunks = append(chunks, text)
	}

	result, err := Run(ctx, claudePath, "haiku", "say hello", "", onChunk)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	const want = "Hello from mock Claude!"
	if result != want {
		t.Errorf("result = %q, want %q", result, want)
	}

	// The mock emits one assistant chunk.
	if len(chunks) == 0 {
		t.Error("expected at least one chunk, got none")
	}
}

func TestRun_ContextCancelled_ReturnsError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately before Run starts.
	cancel()

	claudePath := mockClaudePath(t)

	_, err := Run(ctx, claudePath, "haiku", "say hello", "", nil)
	if err == nil {
		t.Fatal("expected error when context is cancelled, got nil")
	}
}
