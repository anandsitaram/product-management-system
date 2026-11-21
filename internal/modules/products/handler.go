package products

import (
	"net/http"
	"strconv"

	"pms/internal/shared/apperrors"
	"pms/internal/shared/pagination"
	"pms/internal/shared/request"
	"pms/internal/shared/response"
	"pms/internal/shared/validation"
)

type Handler struct{ svc *Service }

func NewHandler(s *Service) *Handler { return &Handler{svc: s} }

func parseFilter(r *http.Request, v *validation.Validator) (Filter, pagination.Params) {
	q := r.URL.Query()
	pg := pagination.Parse(q, v)
	f := Filter{Search: q.Get("search"), Status: q.Get("status")}
	v.MaxLen("search", f.Search, 100)
	if s := q.Get("category_id"); s != "" {
		if id, ok := v.UUID("category_id", s); ok {
			f.CategoryID = &id
		}
	}
	if s := q.Get("label_id"); s != "" {
		if id, ok := v.UUID("label_id", s); ok {
			f.LabelID = &id
		}
	}
	if f.Status != "" {
		v.OneOf("status", f.Status, Statuses...)
	}
	var minF, maxF float64
	var haveMin, haveMax bool
	if s := q.Get("min_price"); s != "" {
		if !priceRe.MatchString(s) {
			v.Add("min_price", "must be a non-negative decimal number")
		} else {
			f.MinPrice, haveMin = s, true
			minF, _ = strconv.ParseFloat(s, 64)
		}
	}
	if s := q.Get("max_price"); s != "" {
		if !priceRe.MatchString(s) {
			v.Add("max_price", "must be a non-negative decimal number")
		} else {
			f.MaxPrice, haveMax = s, true
			maxF, _ = strconv.ParseFloat(s, 64)
		}
	}
	if haveMin && haveMax && minF > maxF {
		v.Add("min_price", "must not exceed max_price")
	}
	f.SortBy = q.Get("sort_by")
	if f.SortBy == "" {
		f.SortBy = "created_at"
	}
	if _, ok := sortColumns[f.SortBy]; !ok {
		v.Add("sort_by", "must be one of: name, sku, price, created_at, updated_at")
	}
	f.Desc = f.SortBy == "created_at" || f.SortBy == "updated_at"
	switch q.Get("order") {
	case "":
	case "asc":
		f.Desc = false
	case "desc":
		f.Desc = true
	default:
		v.Add("order", "must be asc or desc")
	}
	return f, pg
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	v := validation.New()
	f, pg := parseFilter(r, v)
	if err := v.Err(); err != nil {
		response.Error(w, r, err)
		return
	}
	ps, total, err := h.svc.List(r.Context(), f, pg)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	response.Paginated(w, r, ps, response.NewPagination(pg.Page, pg.Limit, total))
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var in Input
	if err := request.DecodeJSON(w, r, &in); err != nil {
		response.Error(w, r, err)
		return
	}
	p, err := h.svc.Create(r.Context(), in)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/products/"+p.ID.String())
	response.JSON(w, r, http.StatusCreated, p)
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := request.PathUUID(r, "productId", "Product")
	if err != nil {
		response.Error(w, r, err)
		return
	}
	p, err := h.svc.Get(r.Context(), id)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	response.JSON(w, r, http.StatusOK, p)
}

func (h *Handler) Put(w http.ResponseWriter, r *http.Request) {
	id, err := request.PathUUID(r, "productId", "Product")
	if err != nil {
		response.Error(w, r, err)
		return
	}
	var in Input
	if err := request.DecodeJSON(w, r, &in); err != nil {
		response.Error(w, r, err)
		return
	}
	p, err := h.svc.Replace(r.Context(), id, in)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	response.JSON(w, r, http.StatusOK, p)
}

func (h *Handler) Patch(w http.ResponseWriter, r *http.Request) {
	id, err := request.PathUUID(r, "productId", "Product")
	if err != nil {
		response.Error(w, r, err)
		return
	}
	var pt Patch
	if err := request.DecodeJSON(w, r, &pt); err != nil {
		response.Error(w, r, err)
		return
	}
	p, err := h.svc.Update(r.Context(), id, pt)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	response.JSON(w, r, http.StatusOK, p)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := request.PathUUID(r, "productId", "Product")
	if err != nil {
		response.Error(w, r, err)
		return
	}
	if err := h.svc.Delete(r.Context(), id); err != nil {
		response.Error(w, r, err)
		return
	}
	response.NoContent(w)
}

var _ = apperrors.NotFound
