package api

import (
	"context"
	"crypto/subtle"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"
)

type contextKey string

const requestIDKey contextKey = "requestID"

// AuthMiddleware verifies the X-API-Key header against the list of valid keys.
// The /api/v1/health endpoint is exempt from authentication.
func AuthMiddleware(validKeys []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/health" || r.URL.Path == "/" {
			next.ServeHTTP(w, r)
			return
		}

		provided := r.Header.Get("X-API-Key")
		if provided == "" {
			writeError(w, http.StatusUnauthorized, "missing X-API-Key header")
			return
		}

		for _, key := range validKeys {
			if subtle.ConstantTimeCompare([]byte(provided), []byte(key)) == 1 {
				next.ServeHTTP(w, r)
				return
			}
		}

		writeError(w, http.StatusUnauthorized, "invalid API key")
	})
}

// RequestIDMiddleware attaches a UUID request ID to the response header and request context.
func RequestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := uuid.New().String()
		w.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), requestIDKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// statusResponseWriter wraps http.ResponseWriter to capture the written status code.
type statusResponseWriter struct {
	http.ResponseWriter
	status int
}

func (sw *statusResponseWriter) WriteHeader(code int) {
	sw.status = code
	sw.ResponseWriter.WriteHeader(code)
}

func (sw *statusResponseWriter) Flush() {
	if f, ok := sw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// CORSMiddleware sets CORS headers based on allowed origins.
// An empty slice disables CORS. A single "*" allows all origins.
func CORSMiddleware(allowedOrigins []string, next http.Handler) http.Handler {
	if len(allowedOrigins) == 0 {
		return next
	}

	allowAll := len(allowedOrigins) == 1 && allowedOrigins[0] == "*"
	originSet := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		originSet[o] = true
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}

		if allowAll || originSet[origin] {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-API-Key")
			w.Header().Set("Access-Control-Max-Age", "86400")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// LoggingMiddleware logs the method, path, status code, and duration of each request.
func LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusResponseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, sw.status, time.Since(start))
	})
}
