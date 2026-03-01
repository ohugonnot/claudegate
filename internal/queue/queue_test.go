package queue

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/claudegate/claudegate/internal/config"
	"github.com/claudegate/claudegate/internal/job"
)

func TestStripCodeFences(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "standard JSON fence",
			input: "```json\n{\"key\":\"value\"}\n```",
			want:  "{\"key\":\"value\"}",
		},
		{
			name:  "plain fence",
			input: "```\n{\"key\":\"value\"}\n```",
			want:  "{\"key\":\"value\"}",
		},
		{
			name:  "no fence unchanged",
			input: "{\"key\":\"value\"}",
			want:  "{\"key\":\"value\"}",
		},
		{
			name:  "only whitespace trimmed",
			input: "  {\"key\":\"value\"}  ",
			want:  "{\"key\":\"value\"}",
		},
		{
			name:  "trailing newline after closing fence",
			input: "```json\n{\"a\":1}\n```\n",
			want:  "{\"a\":1}",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "only opening fence no newline",
			input: "```",
			// HasPrefix matches, no newline so opening fence is kept, but HasSuffix
			// also matches the same "```" so the closing fence removal strips it,
			// leaving an empty string after TrimSpace.
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := stripCodeFences(tt.input)
			if got != tt.want {
				t.Errorf("stripCodeFences(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// mockStore implements job.Store for testing.
type mockStore struct {
	mu   sync.Mutex
	jobs map[string]*job.Job
}

func newMockStore() *mockStore {
	return &mockStore{jobs: make(map[string]*job.Job)}
}

func (m *mockStore) Create(ctx context.Context, j *job.Job) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jobs[j.ID] = j
	return nil
}

func (m *mockStore) Get(ctx context.Context, id string) (*job.Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return nil, job.ErrJobNotFound
	}
	return j, nil
}

func (m *mockStore) UpdateStatus(ctx context.Context, id string, status job.Status, result, errMsg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if j, ok := m.jobs[id]; ok {
		j.Status = status
		j.Result = result
		j.Error = errMsg
	}
	return nil
}

func (m *mockStore) MarkProcessing(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if j, ok := m.jobs[id]; ok {
		j.Status = job.StatusProcessing
	}
	return nil
}

func (m *mockStore) Delete(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.jobs, id)
	return nil
}

func (m *mockStore) List(ctx context.Context, limit, offset int) ([]*job.Job, int, error) {
	return nil, 0, nil
}

func (m *mockStore) ResetProcessing(ctx context.Context) ([]string, error) {
	return nil, nil
}

func (m *mockStore) DeleteTerminalBefore(ctx context.Context, before time.Time) (int64, error) {
	return 0, nil
}

func testConfig(claudePath string) *config.Config {
	return &config.Config{
		ClaudePath:  claudePath,
		QueueSize:   10,
		Concurrency: 1,
	}
}

func TestNotify_NoRaceWithNotifyAndClose(t *testing.T) {
	t.Parallel()
	// Verify that concurrent notify + notifyAndClose do not panic.
	q := &Queue{
		subs: make(map[string][]chan SSEEvent),
		jobs: make(chan string, 10),
	}

	jobID := "race-test"
	ch := make(chan SSEEvent, 64)
	q.mu.Lock()
	q.subs[jobID] = []chan SSEEvent{ch}
	q.mu.Unlock()

	var wg sync.WaitGroup
	// Goroutine 1: spam notify
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			q.notify(jobID, SSEEvent{Event: "chunk", Data: `{"text":"hi"}`})
		}
	}()
	// Goroutine 2: call notifyAndClose once after a short delay
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(1 * time.Millisecond)
		q.notifyAndClose(jobID, SSEEvent{Event: "result", Data: `{"status":"completed"}`})
	}()

	wg.Wait()
	// If we get here without panic, the race is handled correctly.
}

func TestSubscribeUnsubscribe(t *testing.T) {
	t.Parallel()
	store := newMockStore()
	cfg := testConfig("")
	q := New(cfg, store)

	ch := q.Subscribe("job-1")
	if ch == nil {
		t.Fatal("Subscribe returned nil channel")
	}

	q.Unsubscribe("job-1", ch)

	q.mu.RLock()
	_, ok := q.subs["job-1"]
	q.mu.RUnlock()
	if ok {
		t.Error("expected subs[job-1] to be cleaned up after unsubscribe")
	}
}
