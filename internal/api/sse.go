package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/claudegate/claudegate/internal/job"
)

// StreamSSE handles GET /api/v1/jobs/{id}/sse.
// It streams server-sent events for the job until it completes or the client disconnects.
func (h *Handler) StreamSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

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

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// If already terminal, send the result event and close immediately.
	if j.Status == job.StatusCompleted || j.Status == job.StatusFailed {
		writeSSEEvent(w, flusher, "result", j)
		return
	}

	ch := h.queue.Subscribe(id)
	defer h.queue.Unsubscribe(id, ch)

	// Send the current status so the client has an initial state.
	writeSSEEvent(w, flusher, "status", j)

	for {
		select {
		case event, open := <-ch:
			if !open {
				return
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Event, event.Data)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// writeSSEEvent serialises data as JSON and writes a single SSE event frame.
func writeSSEEvent(w http.ResponseWriter, flusher http.Flusher, event string, data any) {
	payload, err := json.Marshal(data)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, payload)
	flusher.Flush()
}
