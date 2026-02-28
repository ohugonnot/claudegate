package job

import "context"

// Store persists and retrieves jobs.
type Store interface {
	Create(ctx context.Context, j *Job) error
	Get(ctx context.Context, id string) (*Job, error)
	UpdateStatus(ctx context.Context, id string, status Status, result, errMsg string) error
	MarkProcessing(ctx context.Context, id string) error
	Delete(ctx context.Context, id string) error
	// ResetProcessing moves all "processing" jobs back to "queued" and returns their IDs.
	// Called at startup to recover jobs that were interrupted by a crash.
	ResetProcessing(ctx context.Context) ([]string, error)
	// List returns a page of jobs ordered by created_at DESC, plus the total count.
	List(ctx context.Context, limit, offset int) ([]*Job, int, error)
}
