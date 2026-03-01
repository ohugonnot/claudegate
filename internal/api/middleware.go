package api

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
)

type contextKey string

const requestIDKey contextKey = "requestID"

// Middleware is a function that wraps an http.Handler.
type Middleware func(http.Handler) http.Handler

// Chain applies middlewares to h in order: first middleware is outermost (executes first).
func Chain(h http.Handler, middlewares ...Middleware) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		h = middlewares[i](h)
	}
	return h
}

// publicPaths are exempt from API key authentication.
var publicPaths = map[string]bool{
	"/api/v1/health": true,
	"/":              true,
}

// Auth returns a Middleware that verifies the X-API-Key header against the list of valid keys.
// The /api/v1/health endpoint is exempt from authentication.
func Auth(validKeys []string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if publicPaths[r.URL.Path] {
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
}

// RequestID is a Middleware that attaches a UUID request ID to the response header and request context.
var RequestID Middleware = func(next http.Handler) http.Handler {
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

// CORS returns a Middleware that sets CORS headers based on allowed origins.
// An empty slice disables CORS. A single "*" allows all origins.
func CORS(allowedOrigins []string) Middleware {
	if len(allowedOrigins) == 0 {
		return func(next http.Handler) http.Handler {
			return next
		}
	}

	allowAll := len(allowedOrigins) == 1 && allowedOrigins[0] == "*"
	originSet := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		originSet[o] = true
	}

	return func(next http.Handler) http.Handler {
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
}

// Logging is a Middleware that logs the method, path, status code, and duration of each request.
var Logging Middleware = func(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusResponseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		reqID, _ := r.Context().Value(requestIDKey).(string)
		slog.Info("request", "method", r.Method, "path", r.URL.Path, "status", sw.status, "duration", time.Since(start), "request_id", reqID)
	})
}
