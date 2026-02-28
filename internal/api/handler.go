package api

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/claudegate/claudegate/internal/config"
	"github.com/claudegate/claudegate/internal/job"
	"github.com/claudegate/claudegate/internal/queue"
	"github.com/google/uuid"
)

//go:embed static/index.html
var frontendHTML []byte

// Handler holds the dependencies for all HTTP handlers.
type Handler struct {
	store job.Store
	queue *queue.Queue
	cfg   *config.Config
}

// NewHandler constructs a Handler with the given dependencies.
func NewHandler(store job.Store, q *queue.Queue, cfg *config.Config) *Handler {
	return &Handler{store: store, queue: q, cfg: cfg}
}

// RegisterRoutes registers all API routes on mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /", h.ServeFrontend)
	mux.HandleFunc("POST /api/v1/jobs", h.CreateJob)
	mux.HandleFunc("GET /api/v1/jobs", h.ListJobs)
	mux.HandleFunc("GET /api/v1/jobs/{id}", h.GetJob)
	mux.HandleFunc("DELETE /api/v1/jobs/{id}", h.DeleteJob)
	mux.HandleFunc("GET /api/v1/jobs/{id}/sse", h.StreamSSE)
	mux.HandleFunc("POST /api/v1/jobs/{id}/cancel", h.CancelJob)
	mux.HandleFunc("GET /api/v1/health", h.Health)
}

// ServeFrontend serves the embedded playground HTML.
func (h *Handler) ServeFrontend(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(frontendHTML) //nolint:errcheck
}

// CreateJob handles POST /api/v1/jobs and responds 202 with the created job.
func (h *Handler) CreateJob(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB max
	var req job.CreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if req.Model == "" {
		req.Model = h.cfg.DefaultModel
	}

	if err := req.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	now := time.Now().UTC()
	j := &job.Job{
		ID:             uuid.New().String(),
		Prompt:         req.Prompt,
		Model:          req.Model,
		CallbackURL:    req.CallbackURL,
		SystemPrompt:   req.SystemPrompt,
		Metadata:       req.Metadata,
		ResponseFormat: req.ResponseFormat,
		Status:         job.StatusQueued,
		CreatedAt:      now,
	}

	if err := h.store.Create(r.Context(), j); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create job")
		return
	}

	if err := h.queue.Enqueue(j.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to enqueue job")
		return
	}

	writeJSON(w, http.StatusAccepted, j)
}

// ListJobs handles GET /api/v1/jobs and responds 200 with a paginated list of jobs.
func (h *Handler) ListJobs(w http.ResponseWriter, r *http.Request) {
	limit := parseIntParam(r.URL.Query().Get("limit"), 20)
	offset := parseIntParam(r.URL.Query().Get("offset"), 0)

	jobs, total, err := h.store.List(r.Context(), limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list jobs")
		return
	}

	// Return an empty array instead of null when there are no jobs.
	if jobs == nil {
		jobs = []*job.Job{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"jobs":   jobs,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

// parseIntParam parses a query string integer, returning the fallback on empty or invalid input.
func parseIntParam(s string, fallback int) int {
	if s == "" {
		return fallback
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return fallback
	}
	return v
}

// GetJob handles GET /api/v1/jobs/{id} and responds 200 with the job.
func (h *Handler) GetJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	j, err := h.store.Get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get job")
		return
	}
	if j == nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}

	writeJSON(w, http.StatusOK, j)
}

// DeleteJob handles DELETE /api/v1/jobs/{id} and responds 204.
func (h *Handler) DeleteJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	j, err := h.store.Get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get job")
		return
	}
	if j == nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}

	if err := h.store.Delete(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete job")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// CancelJob handles POST /api/v1/jobs/{id}/cancel.
// If the job is queued it is marked cancelled in the DB; if it is processing its context is cancelled.
func (h *Handler) CancelJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	j, err := h.store.Get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get job")
		return
	}
	if j == nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}

	if j.Status.IsTerminal() {
		writeError(w, http.StatusConflict, "job already in terminal state")
		return
	}

	if err := h.store.UpdateStatus(r.Context(), id, job.StatusCancelled, "", "job cancelled by user"); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to cancel job")
		return
	}

	// If the job is currently processing, cancel its running context.
	h.queue.Cancel(id)

	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

// Health handles GET /api/v1/health and responds 200.
// It also reports Claude OAuth token validity from ~/.claude/.credentials.json.
func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	resp := map[string]string{"status": "ok", "claude_auth": "unknown"}

	homeDir, err := os.UserHomeDir()
	if err == nil {
		data, err := os.ReadFile(filepath.Join(homeDir, ".claude", ".credentials.json"))
		if err == nil {
			var creds struct {
				ClaudeAiOauth struct {
					ExpiresAt int64 `json:"expiresAt"`
				} `json:"claudeAiOauth"`
			}
			if json.Unmarshal(data, &creds) == nil && creds.ClaudeAiOauth.ExpiresAt > 0 {
				expiresAt := time.UnixMilli(creds.ClaudeAiOauth.ExpiresAt).UTC()
				remaining := time.Until(expiresAt)
				if remaining > 0 {
					resp["claude_auth"] = "valid"
				} else {
					resp["claude_auth"] = "expired"
					remaining = -remaining
				}
				resp["token_expires_at"] = expiresAt.Format(time.RFC3339)
				resp["token_expires_in"] = remaining.Truncate(time.Second).String()
			}
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data) //nolint:errcheck
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
