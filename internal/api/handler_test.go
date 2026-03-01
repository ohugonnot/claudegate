package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/claudegate/claudegate/internal/config"
	"github.com/claudegate/claudegate/internal/job"
	"github.com/claudegate/claudegate/internal/queue"
)

// testConfig returns a minimal config suitable for handler tests.
func testConfig() *config.Config {
	return &config.Config{
		APIKeys:      []string{"test-api-key"},
		DefaultModel: "haiku",
		QueueSize:    100,
		Concurrency:  1,
	}
}

// newTestServer builds an httptest.Server with a real SQLiteStore, Queue and Handler.
func newTestServer(t *testing.T) (*httptest.Server, *job.SQLiteStore) {
	t.Helper()

	store, err := job.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}

	cfg := testConfig()
	q := queue.New(cfg, store)
	h := NewHandler(store, q, cfg)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// Wrap with auth middleware (same as production).
	handler := Auth(cfg.APIKeys)(mux)

	srv := httptest.NewServer(handler)
	t.Cleanup(func() {
		srv.Close()
	})
	return srv, store
}

func apiKey() string { return "test-api-key" }

func doRequest(t *testing.T, srv *httptest.Server, method, path string, body []byte, withAuth bool) *http.Response {
	t.Helper()
	var req *http.Request
	var err error
	if body != nil {
		req, err = http.NewRequest(method, srv.URL+path, bytes.NewReader(body))
	} else {
		req, err = http.NewRequest(method, srv.URL+path, nil)
	}
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if withAuth {
		req.Header.Set("X-API-Key", apiKey())
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do request: %v", err)
	}
	return resp
}

func TestCreateJob_Returns202WithJobID(t *testing.T) {
	srv, _ := newTestServer(t)

	body, _ := json.Marshal(map[string]string{"prompt": "hello"})
	resp := doRequest(t, srv, http.MethodPost, "/api/v1/jobs", body, true)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result["job_id"] == "" {
		t.Error("response body missing job_id")
	}
}

func TestGetJob_Returns200(t *testing.T) {
	srv, _ := newTestServer(t)

	// Create a job first.
	body, _ := json.Marshal(map[string]string{"prompt": "test get"})
	createResp := doRequest(t, srv, http.MethodPost, "/api/v1/jobs", body, true)
	defer createResp.Body.Close()

	if createResp.StatusCode != http.StatusAccepted {
		t.Fatalf("create: status = %d, want 202", createResp.StatusCode)
	}

	var created map[string]interface{}
	err := json.NewDecoder(createResp.Body).Decode(&created)
	if err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	jobID := created["job_id"].(string)

	getResp := doRequest(t, srv, http.MethodGet, "/api/v1/jobs/"+jobID, nil, true)
	defer getResp.Body.Close()

	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("get: status = %d, want 200", getResp.StatusCode)
	}

	var got map[string]interface{}
	err = json.NewDecoder(getResp.Body).Decode(&got)
	if err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if got["job_id"] != jobID {
		t.Errorf("job_id = %v, want %q", got["job_id"], jobID)
	}
}

func TestGetJob_NotFound_Returns404(t *testing.T) {
	srv, _ := newTestServer(t)
	resp := doRequest(t, srv, http.MethodGet, "/api/v1/jobs/does-not-exist", nil, true)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestDeleteJob_NotFound_Returns404(t *testing.T) {
	srv, _ := newTestServer(t)
	resp := doRequest(t, srv, http.MethodDelete, "/api/v1/jobs/does-not-exist", nil, true)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestDeleteJob_Returns204(t *testing.T) {
	srv, _ := newTestServer(t)

	// Create a job to delete.
	body, _ := json.Marshal(map[string]string{"prompt": "delete me"})
	createResp := doRequest(t, srv, http.MethodPost, "/api/v1/jobs", body, true)
	defer createResp.Body.Close()

	var created map[string]interface{}
	err := json.NewDecoder(createResp.Body).Decode(&created)
	if err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	jobID := created["job_id"].(string)

	delResp := doRequest(t, srv, http.MethodDelete, "/api/v1/jobs/"+jobID, nil, true)
	defer delResp.Body.Close()

	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: status = %d, want 204", delResp.StatusCode)
	}
}

func TestHealth_Returns200(t *testing.T) {
	srv, _ := newTestServer(t)

	resp := doRequest(t, srv, http.MethodGet, "/api/v1/health", nil, false)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health: status = %d, want 200", resp.StatusCode)
	}

	var result map[string]string
	err := json.NewDecoder(resp.Body).Decode(&result)
	if err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if result["status"] != "ok" {
		t.Errorf("health status = %q, want %q", result["status"], "ok")
	}
}

func TestAuth_NoAPIKey_Returns401(t *testing.T) {
	srv, _ := newTestServer(t)

	body, _ := json.Marshal(map[string]string{"prompt": "unauthorized"})
	resp := doRequest(t, srv, http.MethodPost, "/api/v1/jobs", body, false)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestAuth_Health_ExemptFromAuth(t *testing.T) {
	srv, _ := newTestServer(t)

	// Health endpoint must be reachable without an API key.
	resp := doRequest(t, srv, http.MethodGet, "/api/v1/health", nil, false)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health without key: status = %d, want 200", resp.StatusCode)
	}
}

func createTestJob(t *testing.T, srv *httptest.Server, prompt string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"prompt": prompt})
	resp := doRequest(t, srv, http.MethodPost, "/api/v1/jobs", body, true)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("create job %q: status = %d, want 202", prompt, resp.StatusCode)
	}
}

func TestListJobs_Returns200(t *testing.T) {
	srv, _ := newTestServer(t)

	createTestJob(t, srv, "job one")
	createTestJob(t, srv, "job two")
	createTestJob(t, srv, "job three")

	resp := doRequest(t, srv, http.MethodGet, "/api/v1/jobs", nil, true)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: status = %d, want 200", resp.StatusCode)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	jobs, ok := result["jobs"].([]interface{})
	if !ok {
		t.Fatalf("jobs field missing or wrong type: %v", result["jobs"])
	}
	if len(jobs) != 3 {
		t.Errorf("len(jobs) = %d, want 3", len(jobs))
	}
	if int(result["total"].(float64)) != 3 {
		t.Errorf("total = %v, want 3", result["total"])
	}
}

func TestCancelJob_Queued_Returns200(t *testing.T) {
	srv, _ := newTestServer(t)

	body, _ := json.Marshal(map[string]string{"prompt": "cancel me"})
	createResp := doRequest(t, srv, http.MethodPost, "/api/v1/jobs", body, true)
	defer createResp.Body.Close()

	var created map[string]interface{}
	err := json.NewDecoder(createResp.Body).Decode(&created)
	if err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	jobID := created["job_id"].(string)

	cancelResp := doRequest(t, srv, http.MethodPost, "/api/v1/jobs/"+jobID+"/cancel", nil, true)
	defer cancelResp.Body.Close()
	if cancelResp.StatusCode != http.StatusOK {
		t.Fatalf("cancel: status = %d, want 200", cancelResp.StatusCode)
	}
}

func TestCancelJob_Terminal_Returns409(t *testing.T) {
	srv, store := newTestServer(t)

	body, _ := json.Marshal(map[string]string{"prompt": "already done"})
	createResp := doRequest(t, srv, http.MethodPost, "/api/v1/jobs", body, true)
	defer createResp.Body.Close()

	var created map[string]interface{}
	err := json.NewDecoder(createResp.Body).Decode(&created)
	if err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	jobID := created["job_id"].(string)

	// Manually mark as completed.
	store.UpdateStatus(context.Background(), jobID, job.StatusCompleted, "done", "") //nolint:errcheck

	cancelResp := doRequest(t, srv, http.MethodPost, "/api/v1/jobs/"+jobID+"/cancel", nil, true)
	defer cancelResp.Body.Close()
	if cancelResp.StatusCode != http.StatusConflict {
		t.Fatalf("cancel terminal: status = %d, want 409", cancelResp.StatusCode)
	}
}

func TestCancelJob_NotFound_Returns404(t *testing.T) {
	srv, _ := newTestServer(t)

	resp := doRequest(t, srv, http.MethodPost, "/api/v1/jobs/does-not-exist/cancel", nil, true)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cancel not found: status = %d, want 404", resp.StatusCode)
	}
}

func TestListJobs_Pagination(t *testing.T) {
	srv, _ := newTestServer(t)

	createTestJob(t, srv, "job one")
	createTestJob(t, srv, "job two")
	createTestJob(t, srv, "job three")

	// First page: limit=2, offset=0 → 2 jobs, total 3.
	resp := doRequest(t, srv, http.MethodGet, "/api/v1/jobs?limit=2&offset=0", nil, true)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("page1: status = %d, want 200", resp.StatusCode)
	}

	var page1 map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&page1); err != nil {
		t.Fatalf("decode page1 response: %v", err)
	}

	jobs1 := page1["jobs"].([]interface{})
	if len(jobs1) != 2 {
		t.Errorf("page1 len(jobs) = %d, want 2", len(jobs1))
	}
	if int(page1["total"].(float64)) != 3 {
		t.Errorf("page1 total = %v, want 3", page1["total"])
	}

	// Second page: limit=2, offset=2 → 1 job, total 3.
	resp2 := doRequest(t, srv, http.MethodGet, "/api/v1/jobs?limit=2&offset=2", nil, true)
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("page2: status = %d, want 200", resp2.StatusCode)
	}

	var page2 map[string]interface{}
	if err := json.NewDecoder(resp2.Body).Decode(&page2); err != nil {
		t.Fatalf("decode page2 response: %v", err)
	}

	jobs2 := page2["jobs"].([]interface{})
	if len(jobs2) != 1 {
		t.Errorf("page2 len(jobs) = %d, want 1", len(jobs2))
	}
	if int(page2["total"].(float64)) != 3 {
		t.Errorf("page2 total = %v, want 3", page2["total"])
	}
}
