// Package categories implements category CRUD (delete = deactivate).
package categories

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"pms/internal/modules/audit"
	"pms/internal/modules/users"
	"pms/internal/platform/database"
	"pms/internal/shared/apperrors"
	"pms/internal/shared/pagination"
	"pms/internal/shared/request"
	"pms/internal/shared/response"
	"pms/internal/shared/validation"
)

type Category struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Description *string   `json:"description"`
	IsActive    bool      `json:"is_active"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

const cols = `id, name, description, is_active, created_at, updated_at`

func scan(row interface{ Scan(...any) error }, c *Category) error {
	return row.Scan(&c.ID, &c.Name, &c.Description, &c.IsActive, &c.CreatedAt, &c.UpdatedAt)
}

type Repository struct{ db database.DBTX }

func NewRepository(db database.DBTX) *Repository { return &Repository{db: db} }

func (r *Repository) Get(ctx context.Context, id uuid.UUID) (*Category, error) {
	var c Category
	err := scan(r.db.QueryRow(ctx, `SELECT `+cols+` FROM categories WHERE id=$1`, id), &c)
	if database.IsNoRows(err) {
		return nil, apperrors.NotFound("Category")
	}
	return &c, err
}

type Filter struct {
	Search   string
	IsActive *bool
}

func (r *Repository) List(ctx context.Context, f Filter, p pagination.Params) ([]Category, int64, error) {
	where, args := []string{"TRUE"}, []any{}
	if f.Search != "" {
		args = append(args, "%"+users.EscapeLike(f.Search)+"%")
		where = append(where, `name ILIKE $`+strconv.Itoa(len(args))+` ESCAPE '\'`)
	}
	if f.IsActive != nil {
		args = append(args, *f.IsActive)
		where = append(where, `is_active = $`+strconv.Itoa(len(args)))
	}
	w := strings.Join(where, " AND ")
	var total int64
	if err := r.db.QueryRow(ctx, `SELECT count(*) FROM categories WHERE `+w, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, p.Limit, p.Offset())
	rows, err := r.db.Query(ctx, `SELECT `+cols+` FROM categories WHERE `+w+` ORDER BY name LIMIT $`+
		strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Category{}
	for rows.Next() {
		var c Category
		if err := scan(rows, &c); err != nil {
			return nil, 0, err
		}
		out = append(out, c)
	}
	return out, total, rows.Err()
}

func (r *Repository) Create(ctx context.Context, name string, desc *string) (*Category, error) {
	var c Category
	err := scan(r.db.QueryRow(ctx, `INSERT INTO categories (name, description) VALUES ($1,$2) RETURNING `+cols, name, desc), &c)
	if err != nil {
		return nil, database.MapError(err)
	}
	return &c, nil
}

func (r *Repository) Update(ctx context.Context, id uuid.UUID, in UpdateInput) (*Category, error) {
	sets, args := []string{"updated_at = now()"}, []any{}
	add := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, col+" = $"+strconv.Itoa(len(args)))
	}
	if in.Name != nil {
		add("name", *in.Name)
	}
	if in.Description.Set {
		if in.Description.Null {
			add("description", nil)
		} else {
			add("description", in.Description.Value)
		}
	}
	if in.IsActive != nil {
		add("is_active", *in.IsActive)
	}
	args = append(args, id)
	var c Category
	err := scan(r.db.QueryRow(ctx, `UPDATE categories SET `+strings.Join(sets, ", ")+` WHERE id=$`+
		strconv.Itoa(len(args))+` RETURNING `+cols, args...), &c)
	if database.IsNoRows(err) {
		return nil, apperrors.NotFound("Category")
	}
	if err != nil {
		return nil, database.MapError(err)
	}
	return &c, nil
}

type CreateInput struct {
	Name        string  `json:"name"`
	Description *string `json:"description"`
}

type UpdateInput struct {
	Name        *string                     `json:"name"`
	Description validation.Optional[string] `json:"description"`
	IsActive    *bool                       `json:"is_active"`
}

type Service struct {
	repo  *Repository
	audit *audit.Recorder
}

func NewService(r *Repository, a *audit.Recorder) *Service { return &Service{repo: r, audit: a} }

func validateName(v *validation.Validator, name string) {
	if v.Required("name", name) {
		v.MaxLen("name", name, 120)
	}
}

func (s *Service) Create(ctx context.Context, in CreateInput) (*Category, error) {
	in.Name = strings.TrimSpace(in.Name)
	v := validation.New()
	validateName(v, in.Name)
	if in.Description != nil {
		v.MaxLen("description", *in.Description, 2000)
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	c, err := s.repo.Create(ctx, in.Name, in.Description)
	if err != nil {
		return nil, err
	}
	s.audit.Record(ctx, audit.Entry{Action: "category.create", EntityType: "category", EntityID: &c.ID, Changes: c})
	return c, nil
}

func (s *Service) Update(ctx context.Context, id uuid.UUID, in UpdateInput) (*Category, error) {
	v := validation.New()
	if in.Name != nil {
		t := strings.TrimSpace(*in.Name)
		in.Name = &t
		validateName(v, t)
	}
	if in.Description.Set && !in.Description.Null {
		v.MaxLen("description", in.Description.Value, 2000)
	}
	if in.Name == nil && !in.Description.Set && in.IsActive == nil {
		v.Add("body", "at least one of name, description, is_active is required")
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	before, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	after, err := s.repo.Update(ctx, id, in)
	if err != nil {
		return nil, err
	}
	s.audit.Record(ctx, audit.Entry{Action: "category.update", EntityType: "category", EntityID: &id,
		Changes: map[string]any{"before": before, "after": after}})
	return after, nil
}

func (s *Service) Deactivate(ctx context.Context, id uuid.UUID) error {
	f := false
	if _, err := s.repo.Update(ctx, id, UpdateInput{IsActive: &f}); err != nil {
		return err
	}
	s.audit.Record(ctx, audit.Entry{Action: "category.deactivate", EntityType: "category", EntityID: &id})
	return nil
}

type Handler struct{ svc *Service }

func NewHandler(s *Service) *Handler { return &Handler{svc: s} }

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v := validation.New()
	p := pagination.Parse(q, v)
	f := Filter{Search: q.Get("search")}
	v.MaxLen("search", f.Search, 100)
	switch q.Get("is_active") {
	case "":
	case "true":
		t := true
		f.IsActive = &t
	case "false":
		b := false
		f.IsActive = &b
	default:
		v.Add("is_active", "must be true or false")
	}
	if err := v.Err(); err != nil {
		response.Error(w, r, err)
		return
	}
	cs, total, err := h.svc.repo.List(r.Context(), f, p)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	response.Paginated(w, r, cs, response.NewPagination(p.Page, p.Limit, total))
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var in CreateInput
	if err := request.DecodeJSON(w, r, &in); err != nil {
		response.Error(w, r, err)
		return
	}
	c, err := h.svc.Create(r.Context(), in)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	response.JSON(w, r, http.StatusCreated, c)
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := request.PathUUID(r, "categoryId", "Category")
	if err != nil {
		response.Error(w, r, err)
		return
	}
	c, err := h.svc.repo.Get(r.Context(), id)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	response.JSON(w, r, http.StatusOK, c)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := request.PathUUID(r, "categoryId", "Category")
	if err != nil {
		response.Error(w, r, err)
		return
	}
	var in UpdateInput
	if err := request.DecodeJSON(w, r, &in); err != nil {
		response.Error(w, r, err)
		return
	}
	c, err := h.svc.Update(r.Context(), id, in)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	response.JSON(w, r, http.StatusOK, c)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := request.PathUUID(r, "categoryId", "Category")
	if err != nil {
		response.Error(w, r, err)
		return
	}
	if err := h.svc.Deactivate(r.Context(), id); err != nil {
		response.Error(w, r, err)
		return
	}
	response.NoContent(w)
}
