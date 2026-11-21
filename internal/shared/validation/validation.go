// Package validation provides small explicit validators that collect field errors.
package validation

import (
	"net/mail"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"pms/internal/shared/apperrors"
)

type Validator struct{ errs []apperrors.FieldError }

func New() *Validator { return &Validator{} }

func (v *Validator) Add(field, msg string) {
	v.errs = append(v.errs, apperrors.FieldError{Field: field, Message: msg})
}

func (v *Validator) OK() bool { return len(v.errs) == 0 }

// Err returns a 400 VALIDATION_ERROR or nil.
func (v *Validator) Err() error {
	if v.OK() {
		return nil
	}
	return apperrors.Validation(v.errs)
}

// Err422 returns a 422 VALIDATION_ERROR or nil.
func (v *Validator) Err422() error {
	if v.OK() {
		return nil
	}
	return apperrors.ValidationUnprocessable(v.errs)
}

func (v *Validator) Required(field, val string) bool {
	if strings.TrimSpace(val) == "" {
		v.Add(field, "is required")
		return false
	}
	return true
}

func (v *Validator) MaxLen(field, val string, max int) {
	if utf8.RuneCountInString(val) > max {
		v.Add(field, "must be at most "+strconv.Itoa(max)+" characters")
	}
}

func (v *Validator) MinLen(field, val string, min int) {
	if utf8.RuneCountInString(val) < min {
		v.Add(field, "must be at least "+strconv.Itoa(min)+" characters")
	}
}

func (v *Validator) Email(field, val string) {
	a, err := mail.ParseAddress(val)
	if err != nil || a.Address != val || !strings.Contains(val[strings.LastIndex(val, "@"):], ".") {
		v.Add(field, "must be a valid email address")
		return
	}
	v.MaxLen(field, val, 255)
}

func (v *Validator) OneOf(field, val string, allowed ...string) {
	for _, a := range allowed {
		if val == a {
			return
		}
	}
	v.Add(field, "must be one of: "+strings.Join(allowed, ", "))
}

func (v *Validator) UUID(field, val string) (uuid.UUID, bool) {
	id, err := uuid.Parse(val)
	if err != nil {
		v.Add(field, "must be a valid UUID")
		return uuid.Nil, false
	}
	return id, true
}
