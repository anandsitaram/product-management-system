package products

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"pms/internal/middleware/auth"
	"pms/internal/modules/audit"
	"pms/internal/shared/apperrors"
	"pms/internal/shared/pagination"
	"pms/internal/shared/validation"
)

var (
	priceRe  = regexp.MustCompile(`^\d{1,10}(\.\d{1,2})?$`)
	Statuses = []string{"DRAFT", "ACTIVE", "INACTIVE"}
)

// Input is the body of POST (create) and PUT (replace).
type Input struct {
	SKU           string      `json:"sku"`
	Name          string      `json:"name"`
	Description   *string     `json:"description"`
	Price         json.Number `json:"price"`
	StockQuantity *int        `json:"stock_quantity"`
	CategoryID    *string     `json:"category_id"`
	Status        string      `json:"status"`
	LabelIDs      []string    `json:"label_ids"`
}

// Patch is the body of PATCH; absent fields are unchanged.
type Patch struct {
	SKU           *string                     `json:"sku"`
	Name          *string                     `json:"name"`
	Description   validation.Optional[string] `json:"description"`
	Price         *json.Number                `json:"price"`
	StockQuantity *int                        `json:"stock_quantity"`
	CategoryID    validation.Optional[string] `json:"category_id"`
	Status        *string                     `json:"status"`
	LabelIDs      *[]string                   `json:"label_ids"`
}

type Service struct {
	repo  *Repository
	audit *audit.Recorder
}

func NewService(r *Repository, a *audit.Recorder) *Service { return &Service{repo: r, audit: a} }

// validatePrice returns the canonical price text.
func validatePrice(v *validation.Validator, field string, n json.Number) string {
	s := strings.TrimSpace(n.String())
	switch {
	case s == "":
		v.Add(field, "is required")
	case strings.HasPrefix(s, "-"):
		v.Add(field, "must be greater than or equal to zero")
	case !priceRe.MatchString(s):
		v.Add(field, "must be a decimal number with at most 10 integer digits and 2 decimal places")
	default:
		return s
	}
	return ""
}

// resolved is a fully-populated, validated product write.
type resolved struct {
	data     Data
	labelIDs []uuid.UUID
	hasLabel bool
}

func (s *Service) validateFull(v *validation.Validator, in Input) (resolved, []uuid.UUID) {
	var out resolved
	in.SKU, in.Name = strings.TrimSpace(in.SKU), strings.TrimSpace(in.Name)
	if v.Required("sku", in.SKU) {
		v.MaxLen("sku", in.SKU, 80)
	}
	if v.Required("name", in.Name) {
		v.MaxLen("name", in.Name, 200)
	}
	if in.Description != nil {
		v.MaxLen("description", *in.Description, 10000)
	}
	out.data.Price = validatePrice(v, "price", in.Price)
	if in.StockQuantity != nil {
		if *in.StockQuantity < 0 {
			v.Add("stock_quantity", "must be greater than or equal to zero")
		} else if *in.StockQuantity > 1_000_000_000 {
			v.Add("stock_quantity", "is too large")
		}
		out.data.StockQuantity = *in.StockQuantity
	}
	if in.Status == "" {
		in.Status = "DRAFT"
	}
	v.OneOf("status", in.Status, Statuses...)
	if in.CategoryID != nil {
		if id, ok := v.UUID("category_id", *in.CategoryID); ok {
			out.data.CategoryID = &id
		}
	}
	seen := map[uuid.UUID]bool{}
	for i, s := range in.LabelIDs {
		if id, ok := v.UUID("label_ids["+strconv.Itoa(i)+"]", s); ok && !seen[id] {
			seen[id] = true
			out.labelIDs = append(out.labelIDs, id)
		}
	}
	if len(in.LabelIDs) > 50 {
		v.Add("label_ids", "must contain at most 50 entries")
	}
	out.hasLabel = true
	out.data.SKU, out.data.Name, out.data.Description, out.data.Status = in.SKU, in.Name, in.Description, in.Status
	return out, out.labelIDs
}

// checkRefs verifies the category and any newly-added labels exist and are active (422 otherwise).
func (s *Service) checkRefs(ctx context.Context, cat *uuid.UUID, checkCat bool, labels []uuid.UUID) error {
	var errs []apperrors.FieldError
	if cat != nil && checkCat {
		active, err := s.repo.CheckCategory(ctx, *cat)
		if err != nil {
			return apperrors.Internal(err)
		}
		if !active {
			errs = append(errs, apperrors.FieldError{Field: "category_id", Message: "category does not exist or is inactive"})
		}
	}
	bad, err := s.repo.InvalidLabels(ctx, labels)
	if err != nil {
		return apperrors.Internal(err)
	}
	for _, id := range bad {
		errs = append(errs, apperrors.FieldError{Field: "label_ids", Message: "label " + id.String() + " does not exist or is inactive"})
	}
	if len(errs) > 0 {
		return apperrors.ValidationUnprocessable(errs)
	}
	return nil
}

func (s *Service) Create(ctx context.Context, in Input) (*Product, error) {
	v := validation.New()
	r, labels := s.validateFull(v, in)
	if err := v.Err(); err != nil {
		return nil, err
	}
	if err := s.checkRefs(ctx, r.data.CategoryID, true, labels); err != nil {
		return nil, err
	}
	var by *uuid.UUID
	if p, ok := auth.From(ctx); ok {
		by = &p.UserID
	}
	p, err := s.repo.Create(ctx, r.data, labels, by)
	if err != nil {
		return nil, err
	}
	s.audit.Record(ctx, audit.Entry{Action: "product.create", EntityType: "product", EntityID: &p.ID, Changes: p})
	return p, nil
}

func labelSet(p *Product) map[uuid.UUID]bool {
	m := map[uuid.UUID]bool{}
	for _, l := range p.Labels {
		m[l.ID] = true
	}
	return m
}

func (s *Service) write(ctx context.Context, id uuid.UUID, before *Product, r resolved, labelIDs *[]uuid.UUID) (*Product, error) {
	catChanged := r.data.CategoryID != nil && (before.CategoryID == nil || *before.CategoryID != *r.data.CategoryID)
	var added []uuid.UUID
	if labelIDs != nil {
		have := labelSet(before)
		for _, l := range *labelIDs {
			if !have[l] {
				added = append(added, l)
			}
		}
	}
	if err := s.checkRefs(ctx, r.data.CategoryID, catChanged, added); err != nil {
		return nil, err
	}
	after, err := s.repo.Update(ctx, id, r.data, labelIDs)
	if err != nil {
		return nil, err
	}
	s.audit.Record(ctx, audit.Entry{Action: "product.update", EntityType: "product", EntityID: &id,
		Changes: map[string]any{"before": before, "after": after}})
	return after, nil
}

// Replace implements PUT: every writable field is replaced (omitted optional fields reset to defaults).
func (s *Service) Replace(ctx context.Context, id uuid.UUID, in Input) (*Product, error) {
	v := validation.New()
	r, labels := s.validateFull(v, in)
	if err := v.Err(); err != nil {
		return nil, err
	}
	before, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if labels == nil {
		labels = []uuid.UUID{}
	}
	return s.write(ctx, id, before, r, &labels)
}

// Update implements PATCH.
func (s *Service) Update(ctx context.Context, id uuid.UUID, pt Patch) (*Product, error) {
	if pt.SKU == nil && pt.Name == nil && !pt.Description.Set && pt.Price == nil && pt.StockQuantity == nil &&
		!pt.CategoryID.Set && pt.Status == nil && pt.LabelIDs == nil {
		return nil, apperrors.Validation([]apperrors.FieldError{{Field: "body", Message: "at least one field is required"}})
	}
	before, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	in := Input{SKU: before.SKU, Name: before.Name, Description: before.Description, Price: before.Price,
		StockQuantity: &before.StockQuantity, Status: before.Status}
	if before.CategoryID != nil {
		c := before.CategoryID.String()
		in.CategoryID = &c
	}
	if pt.SKU != nil {
		in.SKU = *pt.SKU
	}
	if pt.Name != nil {
		in.Name = *pt.Name
	}
	if pt.Description.Set {
		if pt.Description.Null {
			in.Description = nil
		} else {
			d := pt.Description.Value
			in.Description = &d
		}
	}
	if pt.Price != nil {
		in.Price = *pt.Price
	}
	if pt.StockQuantity != nil {
		in.StockQuantity = pt.StockQuantity
	}
	if pt.CategoryID.Set {
		if pt.CategoryID.Null {
			in.CategoryID = nil
		} else {
			c := pt.CategoryID.Value
			in.CategoryID = &c
		}
	}
	if pt.Status != nil {
		in.Status = *pt.Status
	}
	var labelIDs *[]uuid.UUID
	if pt.LabelIDs != nil {
		in.LabelIDs = *pt.LabelIDs
	}
	v := validation.New()
	r, labels := s.validateFull(v, in)
	if err := v.Err(); err != nil {
		return nil, err
	}
	if pt.LabelIDs != nil {
		if labels == nil {
			labels = []uuid.UUID{}
		}
		labelIDs = &labels
	}
	return s.write(ctx, id, before, r, labelIDs)
}

func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	if err := s.repo.SoftDelete(ctx, id); err != nil {
		return err
	}
	s.audit.Record(ctx, audit.Entry{Action: "product.delete", EntityType: "product", EntityID: &id})
	return nil
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (*Product, error) {
	return s.repo.Get(ctx, id)
}

func (s *Service) List(ctx context.Context, f Filter, p pagination.Params) ([]*Product, int64, error) {
	return s.repo.List(ctx, f, p)
}
