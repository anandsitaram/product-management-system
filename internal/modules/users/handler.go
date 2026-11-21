package users

import (
	"net/http"

	"pms/internal/middleware/rbac"
	"pms/internal/shared/pagination"
	"pms/internal/shared/request"
	"pms/internal/shared/response"
	"pms/internal/shared/validation"
)

type Handler struct{ svc *Service }

func NewHandler(s *Service) *Handler { return &Handler{svc: s} }

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v := validation.New()
	p := pagination.Parse(q, v)
	f := ListFilter{Role: q.Get("role"), Search: q.Get("search")}
	if f.Role != "" {
		v.OneOf("role", f.Role, rbac.Roles...)
	}
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
	us, total, err := h.svc.List(r.Context(), f, p)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	response.Paginated(w, r, us, response.NewPagination(p.Page, p.Limit, total))
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var in CreateInput
	if err := request.DecodeJSON(w, r, &in); err != nil {
		response.Error(w, r, err)
		return
	}
	u, err := h.svc.Create(r.Context(), in)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	response.JSON(w, r, http.StatusCreated, u)
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := request.PathUUID(r, "userId", "User")
	if err != nil {
		response.Error(w, r, err)
		return
	}
	u, err := h.svc.Get(r.Context(), id)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	response.JSON(w, r, http.StatusOK, u)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := request.PathUUID(r, "userId", "User")
	if err != nil {
		response.Error(w, r, err)
		return
	}
	var in UpdateInput
	if err := request.DecodeJSON(w, r, &in); err != nil {
		response.Error(w, r, err)
		return
	}
	u, err := h.svc.Update(r.Context(), id, in)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	response.JSON(w, r, http.StatusOK, u)
}
