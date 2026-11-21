// Package request holds helpers for decoding JSON bodies and path parameters.
package request

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"pms/internal/shared/apperrors"
)

const MaxJSONBody = 1 << 20 // 1 MiB

// DecodeJSON strictly decodes a single JSON object (unknown fields are rejected).
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		return apperrors.UnsupportedMediaType("Content-Type must be application/json")
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxJSONBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var mbe *http.MaxBytesError
		var se *json.SyntaxError
		var ute *json.UnmarshalTypeError
		switch {
		case errors.As(err, &mbe):
			return apperrors.PayloadTooLarge("Request body is too large")
		case errors.As(err, &se), errors.Is(err, io.ErrUnexpectedEOF):
			return apperrors.BadRequest("Request body is not valid JSON")
		case errors.As(err, &ute):
			return apperrors.Validation([]apperrors.FieldError{{Field: ute.Field, Message: "has an invalid type; expected " + ute.Type.String()}})
		case errors.Is(err, io.EOF):
			return apperrors.BadRequest("Request body is required")
		default:
			return apperrors.BadRequest("Invalid request body: " + err.Error())
		}
	}
	if dec.More() {
		return apperrors.BadRequest("Request body must contain a single JSON object")
	}
	return nil
}

// PathUUID parses a UUID URL parameter; a malformed ID is reported as 404 (it cannot exist).
func PathUUID(r *http.Request, name, what string) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		return uuid.Nil, apperrors.NotFound(what)
	}
	return id, nil
}
