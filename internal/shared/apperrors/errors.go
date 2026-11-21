// Package apperrors defines the API error type and its constructors.
package apperrors

import (
	"errors"
	"net/http"
)

type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

type Error struct {
	Status  int
	Code    string
	Message string
	Details []FieldError
	Cause   error
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return e.Code + ": " + e.Message + ": " + e.Cause.Error()
	}
	return e.Code + ": " + e.Message
}
func (e *Error) Unwrap() error { return e.Cause }

func New(status int, code, msg string) *Error {
	return &Error{Status: status, Code: code, Message: msg}
}

func BadRequest(msg string) *Error   { return New(http.StatusBadRequest, "BAD_REQUEST", msg) }
func Unauthorized(msg string) *Error { return New(http.StatusUnauthorized, "UNAUTHORIZED", msg) }
func Forbidden(msg string) *Error    { return New(http.StatusForbidden, "FORBIDDEN", msg) }
func NotFound(what string) *Error    { return New(http.StatusNotFound, "NOT_FOUND", what+" not found") }
func Conflict(msg string) *Error     { return New(http.StatusConflict, "CONFLICT", msg) }
func TooManyRequests() *Error {
	return New(http.StatusTooManyRequests, "RATE_LIMITED", "Too many requests; try again later")
}
func PayloadTooLarge(msg string) *Error {
	return New(http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", msg)
}
func UnsupportedMediaType(msg string) *Error {
	return New(http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", msg)
}
func Unprocessable(msg string) *Error {
	return New(http.StatusUnprocessableEntity, "UNPROCESSABLE", msg)
}
func Internal(cause error) *Error {
	return &Error{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "An unexpected error occurred", Cause: cause}
}
func Unavailable(msg string, cause error) *Error {
	return &Error{Status: http.StatusServiceUnavailable, Code: "UNAVAILABLE", Message: msg, Cause: cause}
}

// Validation builds a 400 VALIDATION_ERROR carrying field details.
func Validation(details []FieldError) *Error {
	return &Error{Status: http.StatusBadRequest, Code: "VALIDATION_ERROR", Message: "Request validation failed", Details: details}
}

// ValidationUnprocessable builds a 422 for well-formed requests that violate business rules.
func ValidationUnprocessable(details []FieldError) *Error {
	return &Error{Status: http.StatusUnprocessableEntity, Code: "VALIDATION_ERROR", Message: "Request validation failed", Details: details}
}

// As extracts an *Error from err, wrapping unknown errors as 500.
func As(err error) *Error {
	var ae *Error
	if errors.As(err, &ae) {
		return ae
	}
	return Internal(err)
}
