// Package audit records and queries append-only audit events.
package audit

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"pms/internal/middleware/auth"
	"pms/internal/middleware/requestid"
	"pms/internal/platform/database"
	"pms/internal/shared/pagination"
	"pms/internal/shared/response"
	"pms/internal/shared/validation"
)

type Entry struct {
	ActorID    *uuid.UUID // optional override; defaults to the authenticated principal
	Action     string
	EntityType string
	EntityID   *uuid.UUID
	Changes    any
}

type Log struct {
	ID         uuid.UUID       `json:"id"`
	UserID     *uuid.UUID      `json:"user_id"`
	Action     string          `json:"action"`
	EntityType string          `json:"entity_type"`
	EntityID   *uuid.UUID      `json:"entity_id"`
	Changes    json.RawMessage `json:"changes"`
	RequestID  *string         `json:"request_id"`
	CreatedAt  time.Time       `json:"created_at"`
}

type Recorder struct{ db database.DBTX }

func NewRecorder(db database.DBTX) *Recorder { return &Recorder{db: db} }

var sensitive = []string{"password", "token", "secret", "hash", "authorization", "signed_url"}

func redact(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			lk := strings.ToLower(k)
			hit := false
			for _, s := range sensitive {
				if strings.Contains(lk, s) {
					hit = true
					break
				}
			}
			if hit {
				t[k] = "[REDACTED]"
			} else {
				t[k] = redact(val)
			}
		}
		return t
	case []any:
		for i := range t {
			t[i] = redact(t[i])
		}
		return t
	}
	return v
}

// Record appends an event. Failures are logged and never fail the user's request.
func (r *Recorder) Record(ctx context.Context, e Entry) {
	var changes []byte
	if e.Changes != nil {
		raw, err := json.Marshal(e.Changes)
		if err == nil {
			var generic any
			if json.Unmarshal(raw, &generic) == nil {
				changes, _ = json.Marshal(redact(generic))
			}
		}
	}
	actor := e.ActorID
	if actor == nil {
		if p, ok := auth.From(ctx); ok {
			id := p.UserID
			actor = &id
		}
	}
	var reqID *string
	if id := requestid.From(ctx); id != "" {
		reqID = &id
	}
	_, err := r.db.Exec(context.WithoutCancel(ctx),
		`INSERT INTO audit_logs (user_id, action, entity_type, entity_id, changes, request_id)
		 VALUES ($1,$2,$3,$4,$5,$6)`, actor, e.Action, e.EntityType, e.EntityID, changes, reqID)
	if err != nil {
		slog.Error("audit write failed", "action", e.Action, "entity_type", e.EntityType, "error", err)
	}
}

type Filter struct {
	UserID     *uuid.UUID
	Action     string
	EntityType string
	EntityID   *uuid.UUID
	From, To   *time.Time
}

func (r *Recorder) List(ctx context.Context, f Filter, p pagination.Params) ([]Log, int64, error) {
	where := []string{"TRUE"}
	args := []any{}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.Replace(cond, "?", "$"+itoa(len(args)), 1))
	}
	if f.UserID != nil {
		add("user_id = ?", *f.UserID)
	}
	if f.Action != "" {
		add("action = ?", f.Action)
	}
	if f.EntityType != "" {
		add("entity_type = ?", f.EntityType)
	}
	if f.EntityID != nil {
		add("entity_id = ?", *f.EntityID)
	}
	if f.From != nil {
		add("created_at >= ?", *f.From)
	}
	if f.To != nil {
		add("created_at <= ?", *f.To)
	}
	w := strings.Join(where, " AND ")
	var total int64
	if err := r.db.QueryRow(ctx, "SELECT count(*) FROM audit_logs WHERE "+w, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, p.Limit, p.Offset())
	rows, err := r.db.Query(ctx, `SELECT id, user_id, action, entity_type, entity_id, changes, request_id, created_at
		FROM audit_logs WHERE `+w+` ORDER BY created_at DESC, id DESC LIMIT $`+itoa(len(args)-1)+` OFFSET $`+itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Log{}
	for rows.Next() {
		var l Log
		var ch []byte
		if err := rows.Scan(&l.ID, &l.UserID, &l.Action, &l.EntityType, &l.EntityID, &ch, &l.RequestID, &l.CreatedAt); err != nil {
			return nil, 0, err
		}
		if len(ch) > 0 {
			l.Changes = ch
		} else {
			l.Changes = json.RawMessage("null")
		}
		out = append(out, l)
	}
	return out, total, rows.Err()
}

func itoa(n int) string { return strconv.Itoa(n) }

type Handler struct{ rec *Recorder }

func NewHandler(rec *Recorder) *Handler { return &Handler{rec: rec} }

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v := validation.New()
	p := pagination.Parse(q, v)
	var f Filter
	if s := q.Get("user_id"); s != "" {
		if id, ok := v.UUID("user_id", s); ok {
			f.UserID = &id
		}
	}
	if s := q.Get("entity_id"); s != "" {
		if id, ok := v.UUID("entity_id", s); ok {
			f.EntityID = &id
		}
	}
	f.Action, f.EntityType = q.Get("action"), q.Get("entity_type")
	parseTime := func(name string) *time.Time {
		s := q.Get(name)
		if s == "" {
			return nil
		}
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			v.Add(name, "must be an RFC 3339 timestamp")
			return nil
		}
		return &t
	}
	f.From, f.To = parseTime("from"), parseTime("to")
	if err := v.Err(); err != nil {
		response.Error(w, r, err)
		return
	}
	logs, total, err := h.rec.List(r.Context(), f, p)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	response.Paginated(w, r, logs, response.NewPagination(p.Page, p.Limit, total))
}
