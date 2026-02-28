package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/claudegate/claudegate/internal/config"
	"github.com/claudegate/claudegate/internal/job"
	"github.com/claudegate/claudegate/internal/webhook"
	"github.com/claudegate/claudegate/internal/worker"
)

// SSEEvent represents a Server-Sent Events event.
type SSEEvent struct {
	Event string // "status", "chunk", "result"
	Data  string // JSON string
}

// Queue manages the job queue and workers.
type Queue struct {
	jobs  chan string
	store job.Store
	subs  map[string][]chan SSEEvent
	mu    sync.RWMutex
	cfg   *config.Config
}

// New creates a new Queue.
func New(cfg *config.Config, store job.Store) *Queue {
	return &Queue{
		jobs:  make(chan string, cfg.QueueSize),
		store: store,
		subs:  make(map[string][]chan SSEEvent),
		cfg:   cfg,
	}
}

// Enqueue adds a job ID to the queue. Returns an error if the queue is full.
func (q *Queue) Enqueue(jobID string) error {
	select {
	case q.jobs <- jobID:
		return nil
	default:
		return fmt.Errorf("queue full: cannot enqueue job %s", jobID)
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
			log.Printf("recovery: failed to enqueue job %s: %v", id, err)
		}
	}
	return nil
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

func (q *Queue) processJob(ctx context.Context, jobID string) {
	if err := q.store.MarkProcessing(ctx, jobID); err != nil {
		log.Printf("worker: mark processing %s: %v", jobID, err)
		return
	}

	q.notify(jobID, SSEEvent{Event: "status", Data: `{"status":"processing"}`})

	j, err := q.store.Get(ctx, jobID)
	if err != nil {
		log.Printf("worker: get job %s: %v", jobID, err)
		q.finalizeJob(ctx, jobID, "", fmt.Sprintf("failed to load job: %v", err), "")
		return
	}
	if j == nil {
		log.Printf("worker: job %s not found (deleted?)", jobID)
		q.finalizeJob(ctx, jobID, "", "job not found", "")
		return
	}

	onChunk := func(text string) {
		data, _ := json.Marshal(map[string]string{"text": text})
		q.notify(jobID, SSEEvent{Event: "chunk", Data: string(data)})
	}

	systemPrompt := q.cfg.SecurityPrompt
	if j.ResponseFormat == "json" {
		systemPrompt = systemPrompt + "\n\nCRITICAL: Your response must be RAW JSON only. Do NOT wrap it in ```json code fences. Do NOT add any text before or after the JSON. Do NOT use markdown formatting. Start directly with { or [ and end with } or ]. The raw output must be directly parseable by JSON.parse(). Be concise and fast."
	}
	if j.SystemPrompt != "" {
		systemPrompt = systemPrompt + "\n\n" + j.SystemPrompt
	}

	result, runErr := worker.Run(ctx, q.cfg.ClaudePath, j.Model, j.Prompt, systemPrompt, onChunk)

	// Strip markdown code fences if JSON mode (LLMs sometimes ignore instructions)
	if j.ResponseFormat == "json" && runErr == nil {
		result = stripCodeFences(result)
	}

	errMsg := ""
	if runErr != nil {
		errMsg = runErr.Error()
	}

	q.finalizeJob(ctx, jobID, result, errMsg, j.CallbackURL)
}

func (q *Queue) finalizeJob(ctx context.Context, jobID, result, errMsg, callbackURL string) {
	status := job.StatusCompleted
	if errMsg != "" {
		status = job.StatusFailed
	}

	if err := q.store.UpdateStatus(ctx, jobID, status, result, errMsg); err != nil {
		log.Printf("worker: update status %s: %v", jobID, err)
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
		webhook.Send(callbackURL, payload)
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
		if strings.HasSuffix(s, "```") {
			s = s[:len(s)-3]
		}
		s = strings.TrimSpace(s)
	}
	return s
}

// notify sends an event to all subscribers of a job without blocking.
func (q *Queue) notify(jobID string, event SSEEvent) {
	q.mu.RLock()
	chans := q.subs[jobID]
	q.mu.RUnlock()

	for _, ch := range chans {
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
