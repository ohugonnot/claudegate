package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"

	"github.com/claudegate/claudegate/internal/config"
	"github.com/claudegate/claudegate/internal/job"
	"github.com/claudegate/claudegate/internal/webhook"
	"github.com/claudegate/claudegate/internal/worker"
)

// SSEEvent représente un événement Server-Sent Events.
type SSEEvent struct {
	Event string // "status", "chunk", "result"
	Data  string // JSON string
}

// Queue gère la file d'attente des jobs et les workers.
type Queue struct {
	jobs  chan string
	store job.Store
	subs  map[string][]chan SSEEvent
	mu    sync.RWMutex
	cfg   *config.Config
}

// New crée une nouvelle Queue.
func New(cfg *config.Config, store job.Store) *Queue {
	return &Queue{
		jobs:  make(chan string, cfg.QueueSize),
		store: store,
		subs:  make(map[string][]chan SSEEvent),
		cfg:   cfg,
	}
}

// Enqueue ajoute un job ID dans la file. Retourne une erreur si la file est pleine.
func (q *Queue) Enqueue(jobID string) error {
	select {
	case q.jobs <- jobID:
		return nil
	default:
		return fmt.Errorf("queue full: cannot enqueue job %s", jobID)
	}
}

// Start lance N workers (cfg.Concurrency) en goroutines.
func (q *Queue) Start(ctx context.Context) {
	for range q.cfg.Concurrency {
		go q.runWorker(ctx)
	}
}

// Subscribe crée un canal SSE bufferisé pour un job et le retourne.
func (q *Queue) Subscribe(jobID string) chan SSEEvent {
	ch := make(chan SSEEvent, 64)
	q.mu.Lock()
	q.subs[jobID] = append(q.subs[jobID], ch)
	q.mu.Unlock()
	return ch
}

// Unsubscribe retire un canal SSE de la map.
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

// Recovery réinitialise les jobs "processing" et les ré-enqueue.
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

// runWorker est la boucle d'un worker : prend des jobs et les traite.
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

	onChunk := func(text string) {
		data, _ := json.Marshal(map[string]string{"text": text})
		q.notify(jobID, SSEEvent{Event: "chunk", Data: string(data)})
	}

	systemPrompt := q.cfg.SecurityPrompt
	if j.SystemPrompt != "" {
		systemPrompt = systemPrompt + "\n\n" + j.SystemPrompt
	}

	result, runErr := worker.Run(ctx, q.cfg.ClaudePath, j.Model, j.Prompt, systemPrompt, onChunk)

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

// notify envoie un événement à tous les abonnés d'un job sans bloquer.
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

// notifyAndClose envoie l'événement final et ferme tous les canaux du job.
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
