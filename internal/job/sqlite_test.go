package job

import (
	"context"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	store, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { store.db.Close() })
	return store
}

func makeJob(id, prompt, model string) *Job {
	return &Job{
		ID:        id,
		Prompt:    prompt,
		Model:     model,
		Status:    StatusQueued,
		CreatedAt: time.Now().UTC(),
	}
}

func TestCreateAndGet(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	j := makeJob("job-1", "Hello world", "haiku")
	if err := store.Create(ctx, j); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := store.Get(ctx, "job-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get returned nil, want job")
	}
	if got.ID != j.ID {
		t.Errorf("ID = %q, want %q", got.ID, j.ID)
	}
	if got.Prompt != j.Prompt {
		t.Errorf("Prompt = %q, want %q", got.Prompt, j.Prompt)
	}
	if got.Model != j.Model {
		t.Errorf("Model = %q, want %q", got.Model, j.Model)
	}
	if got.Status != StatusQueued {
		t.Errorf("Status = %q, want %q", got.Status, StatusQueued)
	}
}

func TestGet_NotFound(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	got, err := store.Get(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("Get returned %+v, want nil", got)
	}
}

func TestUpdateStatus_Completed(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	j := makeJob("job-2", "test prompt", "sonnet")
	if err := store.Create(ctx, j); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := store.UpdateStatus(ctx, "job-2", StatusCompleted, "the result", ""); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	got, err := store.Get(ctx, "job-2")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != StatusCompleted {
		t.Errorf("Status = %q, want %q", got.Status, StatusCompleted)
	}
	if got.Result != "the result" {
		t.Errorf("Result = %q, want %q", got.Result, "the result")
	}
	if got.CompletedAt == nil {
		t.Error("CompletedAt is nil, want non-nil")
	}
}

func TestUpdateStatus_Failed(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	j := makeJob("job-3", "fail prompt", "opus")
	if err := store.Create(ctx, j); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := store.UpdateStatus(ctx, "job-3", StatusFailed, "", "something went wrong"); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	got, err := store.Get(ctx, "job-3")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != StatusFailed {
		t.Errorf("Status = %q, want %q", got.Status, StatusFailed)
	}
	if got.Error != "something went wrong" {
		t.Errorf("Error = %q, want %q", got.Error, "something went wrong")
	}
	if got.CompletedAt == nil {
		t.Error("CompletedAt is nil, want non-nil")
	}
}

func TestMarkProcessing(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	j := makeJob("job-4", "processing prompt", "haiku")
	if err := store.Create(ctx, j); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := store.MarkProcessing(ctx, "job-4"); err != nil {
		t.Fatalf("MarkProcessing: %v", err)
	}

	got, err := store.Get(ctx, "job-4")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != StatusProcessing {
		t.Errorf("Status = %q, want %q", got.Status, StatusProcessing)
	}
	if got.StartedAt == nil {
		t.Error("StartedAt is nil, want non-nil")
	}
}

func TestDelete(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	j := makeJob("job-5", "delete me", "haiku")
	if err := store.Create(ctx, j); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := store.Delete(ctx, "job-5"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	got, err := store.Get(ctx, "job-5")
	if err != nil {
		t.Fatalf("Get after delete: %v", err)
	}
	if got != nil {
		t.Errorf("Get after delete returned %+v, want nil", got)
	}
}

func TestResetProcessing(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	j1 := makeJob("job-a", "first job", "haiku")
	j2 := makeJob("job-b", "second job", "haiku")

	if err := store.Create(ctx, j1); err != nil {
		t.Fatalf("Create j1: %v", err)
	}
	if err := store.Create(ctx, j2); err != nil {
		t.Fatalf("Create j2: %v", err)
	}

	// Mark only j2 as processing.
	if err := store.MarkProcessing(ctx, "job-b"); err != nil {
		t.Fatalf("MarkProcessing j2: %v", err)
	}

	ids, err := store.ResetProcessing(ctx)
	if err != nil {
		t.Fatalf("ResetProcessing: %v", err)
	}
	if len(ids) != 1 || ids[0] != "job-b" {
		t.Errorf("ResetProcessing returned %v, want [job-b]", ids)
	}

	// j2 must now be queued again.
	got, err := store.Get(ctx, "job-b")
	if err != nil {
		t.Fatalf("Get j2: %v", err)
	}
	if got.Status != StatusQueued {
		t.Errorf("j2 Status = %q after reset, want %q", got.Status, StatusQueued)
	}
	if got.StartedAt != nil {
		t.Error("j2 StartedAt should be nil after reset")
	}

	// j1 must still be queued.
	got1, err := store.Get(ctx, "job-a")
	if err != nil {
		t.Fatalf("Get j1: %v", err)
	}
	if got1.Status != StatusQueued {
		t.Errorf("j1 Status = %q, want %q", got1.Status, StatusQueued)
	}
}
