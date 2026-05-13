package logger

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
)

const (
	// RequestIDHeader is the HTTP header that carries request correlation id.
	RequestIDHeader = "X-Request-ID"
)

type ctxKey string

const requestIDKey ctxKey = "request_id"

// RequestIDFromContext extracts request_id from context.
func RequestIDFromContext(ctx context.Context) string {
	raw, _ := ctx.Value(requestIDKey).(string)
	return raw
}

// RequestID ensures each request has X-Request-ID, stores it in context and mirrors in response.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get(RequestIDHeader)
		if requestID == "" {
			requestID = uuid.NewString()
		}

		ctx := context.WithValue(r.Context(), requestIDKey, requestID)
		r = r.WithContext(ctx)
		r.Header.Set(RequestIDHeader, requestID)
		w.Header().Set(RequestIDHeader, requestID)

		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(p)
}

// HTTPLogger logs one structured record per handled request.
func HTTPLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}

			next.ServeHTTP(rec, r)

			status := rec.status
			if status == 0 {
				status = http.StatusOK
			}

			log.Info(
				"http_request",
				"request_id", RequestIDFromContext(r.Context()),
				"method", r.Method,
				"path", r.URL.Path,
				"status", status,
				"duration_ms", time.Since(start).Milliseconds(),
			)
		})
	}
}

