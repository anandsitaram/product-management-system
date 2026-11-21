// Package recovery converts panics into 500 responses without leaking details.
package recovery

import (
	"log/slog"
	"net/http"
	"runtime/debug"

	"pms/internal/middleware/requestid"
	"pms/internal/shared/apperrors"
	"pms/internal/shared/response"
)

func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				slog.Error("panic recovered", "request_id", requestid.From(r.Context()), "panic", rec, "stack", string(debug.Stack()))
				response.Error(w, r, apperrors.Internal(nil))
			}
		}()
		next.ServeHTTP(w, r)
	})
}
