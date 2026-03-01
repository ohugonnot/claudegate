package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/claudegate/claudegate/internal/config"
	"github.com/claudegate/claudegate/internal/job"
	"github.com/claudegate/claudegate/internal/webhook"
	"github.com/claudegate/claudegate/internal/worker"
)

// ErrQueueFull is returned by Enqueue when the job channel is at capacity.
// Callers should map this to HTTP 503 Service Unavailable.
var ErrQueueFull = errors.New("queue full")

// SSEEvent represents a Server-Sent Events event.
type SSEEvent struct {
	Event string // "status", "chunk", "result"
	Data  string // JSON string
}

// Queue manages the job queue and workers.
type Queue struct {
	jobs    chan string
	store   job.Store
	subs    map[string][]chan SSEEvent
	cancels map[string]context.CancelFunc
	mu      sync.RWMutex
	cfg     *config.Config
}

// New creates a new Queue.
func New(cfg *config.Config, store job.Store) *Queue {
	return &Queue{
		jobs:    make(chan string, cfg.QueueSize),
		store:   store,
		subs:    make(map[string][]chan SSEEvent),
		cancels: make(map[string]context.CancelFunc),
		cfg:     cfg,
	}
}

// Cancel cancels a running job by its ID. Returns true if the job was found and cancelled.
func (q *Queue) Cancel(jobID string) bool {
	q.mu.Lock()
	cancel, ok := q.cancels[jobID]
	q.mu.Unlock()
	if ok {
		cancel()
		return true
	}
	return false
}

// Enqueue adds a job ID to the queue. Returns an error if the queue is full.
func (q *Queue) Enqueue(jobID string) error {
	select {
	case q.jobs <- jobID:
		return nil
	default:
		return fmt.Errorf("%w: job %s", ErrQueueFull, jobID)
	}
}

// Start launches N workers (cfg.Concurrency) as goroutines.
func (q *Queue) Start(ctx context.Context) {
	for range q.cfg.Concurrency {
		go q.runWorker(ctx)
	}
}

// Subscribe creates a buffered SSE channel for a job and returns it.
func (q *Queue) Subscribe(jobID string) chan SSEEvent {
	ch := make(chan SSEEvent, 64)
	q.mu.Lock()
	q.subs[jobID] = append(q.subs[jobID], ch)
	q.mu.Unlock()
	return ch
}

// Unsubscribe removes an SSE channel from the map.
func (q *Queue) Unsubscribe(jobID string, ch chan SSEEvent) {
	q.mu.Lock()
	defer q.mu.Unlock()

	chans := q.subs[jobID]
	for i, c := range chans {
		if c == ch {
			q.subs[jobID] = append(chans[:i], chans[i+1:]...)
			break
		}
	}
	if len(q.subs[jobID]) == 0 {
		delete(q.subs, jobID)
	}
}

// Recovery resets "processing" jobs and re-enqueues them.
func (q *Queue) Recovery(ctx context.Context) error {
	ids, err := q.store.ResetProcessing(ctx)
	if err != nil {
		return fmt.Errorf("reset processing: %w", err)
	}
	for _, id := range ids {
		if err := q.Enqueue(id); err != nil {
			slog.Error("recovery: failed to enqueue job", "job_id", id, "error", err)
		}
	}
	return nil
}

// StartCleanup launches a background goroutine that periodically deletes old terminal jobs.
func (q *Queue) StartCleanup(ctx context.Context, ttlHours, intervalMinutes int) {
	if ttlHours <= 0 {
		return
	}

	ticker := time.NewTicker(time.Duration(intervalMinutes) * time.Minute)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				before := time.Now().Add(-time.Duration(ttlHours) * time.Hour)
				deleted, err := q.store.DeleteTerminalBefore(ctx, before)
				if err != nil {
					slog.Error("cleanup", "error", err)
				} else if deleted > 0 {
					slog.Info("cleanup: deleted old jobs", "count", deleted)
				}
			}
		}
	}()
}

// runWorker is a worker loop: dequeues jobs and processes them.
func (q *Queue) runWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case jobID := <-q.jobs:
			q.processJob(ctx, jobID)
		}
	}
}

// chunkWriter implements worker.ChunkWriter, forwarding chunks to SSE subscribers.
type chunkWriter struct {
	q     *Queue
	jobID string
}

func (cw *chunkWriter) WriteChunk(text string) {
	data, _ := json.Marshal(map[string]string{"text": text})
	cw.q.notify(cw.jobID, SSEEvent{Event: "chunk", Data: string(data)})
}

func (q *Queue) processJob(ctx context.Context, jobID string) {
	// Check if job was cancelled while waiting in the queue channel.
	j, err := q.store.Get(ctx, jobID)
	if errors.Is(err, job.ErrJobNotFound) {
		slog.Warn("worker: job not found", "job_id", jobID)
		return
	}
	if err != nil {
		slog.Error("worker: get job", "job_id", jobID, "error", err)
		return
	}
	if j.Status == job.StatusCancelled {
		slog.Info("worker: job already cancelled, skipping", "job_id", jobID)
		return
	}

	if err := q.store.MarkProcessing(ctx, jobID); err != nil {
		slog.Error("worker: mark processing", "job_id", jobID, "error", err)
		return
	}

	q.notify(jobID, SSEEvent{Event: "status", Data: `{"status":"processing"}`})

	// Create cancellable context for this job.
	jobCtx, jobCancel := context.WithCancel(ctx)
	defer jobCancel()

	// Apply per-job timeout if configured.
	if q.cfg.JobTimeoutMinutes > 0 {
		var timeoutCancel context.CancelFunc
		jobCtx, timeoutCancel = context.WithTimeout(jobCtx, time.Duration(q.cfg.JobTimeoutMinutes)*time.Minute)
		defer timeoutCancel()
	}

	// Register cancel func so Cancel() can stop this job while it is running.
	q.mu.Lock()
	q.cancels[jobID] = jobCancel
	q.mu.Unlock()
	defer func() {
		q.mu.Lock()
		delete(q.cancels, jobID)
		q.mu.Unlock()
	}()

	cw := &chunkWriter{q: q, jobID: jobID}

	systemPrompt := q.cfg.SecurityPrompt
	if j.ResponseFormat == "json" {
		systemPrompt = systemPrompt + "\n\nCRITICAL: Your response must be RAW JSON only. Do NOT wrap it in ```json code fences. Do NOT add any text before or after the JSON. Do NOT use markdown formatting. Start directly with { or [ and end with } or ]. The raw output must be directly parseable by JSON.parse(). Be concise and fast."
	}
	if j.SystemPrompt != "" {
		systemPrompt = systemPrompt + "\n\n" + j.SystemPrompt
	}

	result, runErr := worker.Run(jobCtx, q.cfg.ClaudePath, j.Model, j.Prompt, systemPrompt, cw)

	// Strip markdown code fences if JSON mode (LLMs sometimes ignore instructions)
	if j.ResponseFormat == "json" && runErr == nil {
		result = stripCodeFences(result)
	}

	var status job.Status
	var errMsg string
	if runErr != nil {
		switch {
		case errors.Is(runErr, context.Canceled):
			status = job.StatusCancelled
			errMsg = "job cancelled by user"
		case errors.Is(runErr, context.DeadlineExceeded):
			status = job.StatusFailed
			errMsg = fmt.Sprintf("job timed out after %dm", q.cfg.JobTimeoutMinutes)
		default:
			status = job.StatusFailed
			errMsg = runErr.Error()
		}
	} else {
		status = job.StatusCompleted
	}

	q.finalizeJob(ctx, jobID, status, result, errMsg, j.CallbackURL)
}

func (q *Queue) finalizeJob(ctx context.Context, jobID string, status job.Status, result, errMsg, callbackURL string) {
	if err := q.store.UpdateStatus(ctx, jobID, status, result, errMsg); err != nil {
		slog.Error("worker: update status", "job_id", jobID, "error", err)
	}

	data, _ := json.Marshal(map[string]string{
		"status": string(status),
		"result": result,
		"error":  errMsg,
	})
	q.notifyAndClose(jobID, SSEEvent{Event: "result", Data: string(data)})

	if callbackURL != "" {
		payload, _ := json.Marshal(map[string]string{
			"job_id": jobID,
			"status": string(status),
			"result": result,
			"error":  errMsg,
		})
		webhook.Send(context.WithoutCancel(ctx), callbackURL, payload)
	}
}

// stripCodeFences removes markdown code fences that LLMs sometimes add despite instructions.
func stripCodeFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		// Remove opening fence (```json, ```, etc.)
		if idx := strings.Index(s, "\n"); idx != -1 {
			s = s[idx+1:]
		}
		// Remove closing fence
		s = strings.TrimSuffix(s, "```")
		s = strings.TrimSpace(s)
	}
	return s
}

// notify sends an event to all subscribers of a job without blocking.
// The RLock is held for the entire iteration to prevent notifyAndClose from
// closing channels between the slice copy and the send (send on closed channel panic).
func (q *Queue) notify(jobID string, event SSEEvent) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	for _, ch := range q.subs[jobID] {
		select {
		case ch <- event:
		default:
		}
	}
}

// notifyAndClose sends the final event and closes all channels for the job.
func (q *Queue) notifyAndClose(jobID string, event SSEEvent) {
	q.mu.Lock()
	chans := q.subs[jobID]
	delete(q.subs, jobID)
	q.mu.Unlock()

	for _, ch := range chans {
		select {
		case ch <- event:
		default:
		}
		close(ch)
	}
}
