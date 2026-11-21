// Package response renders the consistent JSON envelope used by every endpoint.
package response

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"pms/internal/middleware/requestid"
	"pms/internal/shared/apperrors"
)

type Meta struct {
	RequestID string `json:"request_id"`
}

type Pagination struct {
	Page       int   `json:"page"`
	Limit      int   `json:"limit"`
	Total      int64 `json:"total"`
	TotalPages int   `json:"total_pages"`
}

func NewPagination(page, limit int, total int64) Pagination {
	tp := 0
	if limit > 0 {
		tp = int((total + int64(limit) - 1) / int64(limit))
	}
	return Pagination{Page: page, Limit: limit, Total: total, TotalPages: tp}
}

func write(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("encode response", "error", err)
	}
}

func JSON(w http.ResponseWriter, r *http.Request, status int, data any) {
	write(w, status, map[string]any{"data": data, "meta": Meta{RequestID: requestid.From(r.Context())}})
}

func Paginated(w http.ResponseWriter, r *http.Request, data any, p Pagination) {
	write(w, http.StatusOK, map[string]any{
		"data": data, "pagination": p, "meta": Meta{RequestID: requestid.From(r.Context())},
	})
}

func NoContent(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }

// Error renders err as the standard error envelope. Internal details are logged, never returned.
func Error(w http.ResponseWriter, r *http.Request, err error) {
	ae := apperrors.As(err)
	if ae.Status >= 500 {
		slog.Error("request failed", "request_id", requestid.From(r.Context()), "code", ae.Code, "error", err)
	}
	inner := map[string]any{"code": ae.Code, "message": ae.Message}
	if len(ae.Details) > 0 {
		inner["details"] = ae.Details
	}
	write(w, ae.Status, map[string]any{"error": inner, "meta": Meta{RequestID: requestid.From(r.Context())}})
}
