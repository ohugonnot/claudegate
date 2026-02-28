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

func TestList(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	j1 := makeJob("list-1", "first", "haiku")
	j2 := makeJob("list-2", "second", "haiku")
	j3 := makeJob("list-3", "third", "haiku")

	for _, j := range []*Job{j1, j2, j3} {
		if err := store.Create(ctx, j); err != nil {
			t.Fatalf("Create %s: %v", j.ID, err)
		}
	}

	// All jobs.
	jobs, total, err := store.List(ctx, 20, 0)
	if err != nil {
		t.Fatalf("List(20,0): %v", err)
	}
	if len(jobs) != 3 {
		t.Errorf("List(20,0) len = %d, want 3", len(jobs))
	}
	if total != 3 {
		t.Errorf("List(20,0) total = %d, want 3", total)
	}

	// First page.
	jobs, total, err = store.List(ctx, 2, 0)
	if err != nil {
		t.Fatalf("List(2,0): %v", err)
	}
	if len(jobs) != 2 {
		t.Errorf("List(2,0) len = %d, want 2", len(jobs))
	}
	if total != 3 {
		t.Errorf("List(2,0) total = %d, want 3", total)
	}

	// Second page.
	jobs, total, err = store.List(ctx, 2, 2)
	if err != nil {
		t.Fatalf("List(2,2): %v", err)
	}
	if len(jobs) != 1 {
		t.Errorf("List(2,2) len = %d, want 1", len(jobs))
	}
	if total != 3 {
		t.Errorf("List(2,2) total = %d, want 3", total)
	}
}

func TestDeleteTerminalBefore(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	// Create jobs with different statuses
	j1 := makeJob("ttl-1", "old completed", "haiku")
	j2 := makeJob("ttl-2", "old failed", "haiku")
	j3 := makeJob("ttl-3", "recent completed", "haiku")
	j4 := makeJob("ttl-4", "still queued", "haiku")

	for _, j := range []*Job{j1, j2, j3, j4} {
		if err := store.Create(ctx, j); err != nil {
			t.Fatalf("Create %s: %v", j.ID, err)
		}
	}

	// Mark j1, j2, j3 as completed/failed with different times
	store.UpdateStatus(ctx, "ttl-1", StatusCompleted, "result1", "")
	store.UpdateStatus(ctx, "ttl-2", StatusFailed, "", "error2")
	store.UpdateStatus(ctx, "ttl-3", StatusCompleted, "result3", "")

	// Manually set old completed_at for j1 and j2
	oldTime := time.Now().Add(-48 * time.Hour)
	store.db.ExecContext(ctx, `UPDATE jobs SET completed_at = ? WHERE id IN (?, ?)`, oldTime, "ttl-1", "ttl-2")

	// Delete jobs completed before 24 hours ago
	cutoff := time.Now().Add(-24 * time.Hour)
	deleted, err := store.DeleteTerminalBefore(ctx, cutoff)
	if err != nil {
		t.Fatalf("DeleteTerminalBefore: %v", err)
	}
	if deleted != 2 {
		t.Errorf("deleted = %d, want 2", deleted)
	}

	// j1 and j2 should be gone
	got1, _ := store.Get(ctx, "ttl-1")
	if got1 != nil {
		t.Error("ttl-1 should be deleted")
	}
	got2, _ := store.Get(ctx, "ttl-2")
	if got2 != nil {
		t.Error("ttl-2 should be deleted")
	}

	// j3 (recent) and j4 (queued) should remain
	got3, _ := store.Get(ctx, "ttl-3")
	if got3 == nil {
		t.Error("ttl-3 should still exist")
	}
	got4, _ := store.Get(ctx, "ttl-4")
	if got4 == nil {
		t.Error("ttl-4 should still exist")
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
