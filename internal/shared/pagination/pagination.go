// Package pagination parses and validates page/limit query parameters.
package pagination

import (
	"net/url"
	"strconv"

	"pms/internal/shared/validation"
)

const (
	DefaultLimit = 20
	MaxLimit     = 100
)

type Params struct{ Page, Limit int }

func (p Params) Offset() int { return (p.Page - 1) * p.Limit }

func Parse(q url.Values, v *validation.Validator) Params {
	p := Params{Page: 1, Limit: DefaultLimit}
	if s := q.Get("page"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 1_000_000 {
			v.Add("page", "must be an integer >= 1")
		} else {
			p.Page = n
		}
	}
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > MaxLimit {
			v.Add("limit", "must be an integer between 1 and 100")
		} else {
			p.Limit = n
		}
	}
	return p
}
