// Package logging provides structured access logs and metrics observation.
package logging

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"pms/internal/middleware/requestid"
)

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (s *statusWriter) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += int64(n)
	return n, err
}

func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// Observer receives request outcomes (used for metrics).
type Observer func(method, route string, status int, d time.Duration)

// Middleware logs method, path (never the query string, which may carry signed URLs) and outcome.
func Middleware(log *slog.Logger, observe Observer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w}
			next.ServeHTTP(sw, r)
			if sw.status == 0 {
				sw.status = http.StatusOK
			}
			d := time.Since(start)
			route := "unmatched"
			if rc := chi.RouteContext(r.Context()); rc != nil && rc.RoutePattern() != "" {
				route = rc.RoutePattern()
			}
			if observe != nil {
				observe(r.Method, route, sw.status, d)
			}
			lvl := slog.LevelInfo
			if sw.status >= 500 {
				lvl = slog.LevelError
			} else if sw.status >= 400 {
				lvl = slog.LevelWarn
			}
			log.Log(r.Context(), lvl, "http_request",
				"request_id", requestid.From(r.Context()), "method", r.Method, "path", r.URL.Path,
				"route", route, "status", sw.status, "bytes", sw.bytes, "duration_ms", d.Milliseconds())
		})
	}
}
