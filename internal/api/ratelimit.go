package api

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// ipLimiter holds a rate limiter and the last time it was seen.
type ipLimiter struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// RateLimiter manages per-IP rate limiters for job submission.
type RateLimiter struct {
	mu    sync.Mutex
	ips   map[string]*ipLimiter
	rps   rate.Limit
	burst int
}

// NewRateLimiter creates a RateLimiter allowing rps requests/second per IP.
// Burst is set to rps (allows a short burst equal to the per-second rate).
// Starts a background goroutine that evicts IPs not seen for 5 minutes.
func NewRateLimiter(rps int) *RateLimiter {
	rl := &RateLimiter{
		ips:   make(map[string]*ipLimiter),
		rps:   rate.Limit(rps),
		burst: rps,
	}
	go rl.cleanup()
	return rl
}

func (rl *RateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	l, ok := rl.ips[ip]
	if !ok {
		l = &ipLimiter{limiter: rate.NewLimiter(rl.rps, rl.burst)}
		rl.ips[ip] = l
	}
	l.lastSeen = time.Now()
	return l.limiter.Allow()
}

// cleanup removes limiters for IPs not seen in the last 5 minutes.
func (rl *RateLimiter) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		rl.mu.Lock()
		cutoff := time.Now().Add(-5 * time.Minute)
		for ip, l := range rl.ips {
			if l.lastSeen.Before(cutoff) {
				delete(rl.ips, ip)
			}
		}
		rl.mu.Unlock()
	}
}

// RateLimit returns a Middleware that limits POST /api/v1/jobs to rps req/s per IP.
// If rps is 0 the middleware is a no-op.
func RateLimit(rps int) Middleware {
	if rps <= 0 {
		return func(next http.Handler) http.Handler { return next }
	}
	rl := NewRateLimiter(rps)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && r.URL.Path == "/api/v1/jobs" {
				ip := clientIP(r)
				if !rl.allow(ip) {
					writeError(w, http.StatusTooManyRequests, "rate limit exceeded, slow down")
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// clientIP extracts the real client IP, respecting X-Forwarded-For when behind a proxy.
func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		// X-Forwarded-For may be "client, proxy1, proxy2" — take the first.
		if idx := strings.Index(fwd, ","); idx != -1 {
			return strings.TrimSpace(fwd[:idx])
		}
		return strings.TrimSpace(fwd)
	}
	// Strip port from RemoteAddr.
	addr := r.RemoteAddr
	if idx := strings.LastIndex(addr, ":"); idx != -1 {
		return addr[:idx]
	}
	return addr
}
