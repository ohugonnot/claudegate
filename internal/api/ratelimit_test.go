package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRateLimit_Disabled(t *testing.T) {
	t.Parallel()
	mw := RateLimit(0)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
}

func TestRateLimit_AllowsUnderLimit(t *testing.T) {
	t.Parallel()
	mw := RateLimit(10)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	// First request should always be allowed (burst=rps=10).
	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", nil)
	req.RemoteAddr = "1.2.3.4:5678"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
}

func TestRateLimit_BlocksOverLimit(t *testing.T) {
	t.Parallel()
	// rps=1, burst=1 — second request from same IP should be blocked.
	mw := RateLimit(1)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	send := func() int {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", nil)
		req.RemoteAddr = "5.6.7.8:1234"
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		return rr.Code
	}

	// First request: allowed (consumes the burst token).
	if code := send(); code != http.StatusOK {
		t.Errorf("first request: status = %d, want 200", code)
	}
	// Second request immediately after: blocked.
	if code := send(); code != http.StatusTooManyRequests {
		t.Errorf("second request: status = %d, want 429", code)
	}
}

func TestRateLimit_OnlyAppliesTo_PostJobs(t *testing.T) {
	t.Parallel()
	// rps=1 — but GET requests should never be rate limited.
	mw := RateLimit(1)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil)
		req.RemoteAddr = "9.9.9.9:9999"
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("GET request %d: status = %d, want 200", i+1, rr.Code)
		}
	}
}
