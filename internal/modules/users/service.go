package users

import (
	"context"
	"regexp"
	"strings"
	"unicode"

	"github.com/google/uuid"

	"pms/internal/middleware/auth"
	"pms/internal/middleware/rbac"
	"pms/internal/modules/audit"
	"pms/internal/security"
	"pms/internal/shared/apperrors"
	"pms/internal/shared/pagination"
	"pms/internal/shared/validation"
)

type Service struct {
	repo   *Repository
	audit  *audit.Recorder
	params security.Argon2Params
}

func NewService(repo *Repository, rec *audit.Recorder, p security.Argon2Params) *Service {
	return &Service{repo: repo, audit: rec, params: p}
}

var usernameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ValidatePassword enforces the password policy: 10-128 characters with a letter and a digit.
func ValidatePassword(v *validation.Validator, field, pw string) {
	if len(pw) < 10 || len(pw) > 128 {
		v.Add(field, "must be between 10 and 128 characters")
		return
	}
	var letter, digit bool
	for _, r := range pw {
		letter = letter || unicode.IsLetter(r)
		digit = digit || unicode.IsDigit(r)
	}
	if !letter || !digit {
		v.Add(field, "must contain at least one letter and one digit")
	}
}

type CreateInput struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
	Role     string `json:"role"`
	IsActive *bool  `json:"is_active"`
}

func (s *Service) Create(ctx context.Context, in CreateInput) (*User, error) {
	in.Username, in.Email = strings.ToLower(strings.TrimSpace(in.Username)), strings.ToLower(strings.TrimSpace(in.Email))
	v := validation.New()
	if v.Required("username", in.Username) {
		v.MinLen("username", in.Username, 3)
		v.MaxLen("username", in.Username, 80)
		if !usernameRe.MatchString(in.Username) {
			v.Add("username", "may contain only letters, digits, '.', '_' and '-'")
		}
	}
	if v.Required("email", in.Email) {
		v.Email("email", in.Email)
	}
	if v.Required("password", in.Password) {
		ValidatePassword(v, "password", in.Password)
	}
	if v.Required("role", in.Role) {
		v.OneOf("role", in.Role, rbac.Roles...)
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	active := true
	if in.IsActive != nil {
		active = *in.IsActive
	}
	hash, err := security.HashPassword(in.Password, s.params)
	if err != nil {
		return nil, apperrors.Internal(err)
	}
	u, err := s.repo.Create(ctx, in.Username, in.Email, hash, in.Role, active)
	if err != nil {
		return nil, err
	}
	s.audit.Record(ctx, audit.Entry{Action: "user.create", EntityType: "user", EntityID: &u.ID,
		Changes: map[string]any{"username": u.Username, "email": u.Email, "role": u.Role, "is_active": u.IsActive}})
	return u, nil
}

type UpdateInput struct {
	Email    *string `json:"email"`
	Password *string `json:"password"`
	Role     *string `json:"role"`
	IsActive *bool   `json:"is_active"`
}

func (s *Service) Update(ctx context.Context, id uuid.UUID, in UpdateInput) (*User, error) {
	v := validation.New()
	if in.Email != nil {
		e := strings.ToLower(strings.TrimSpace(*in.Email))
		in.Email = &e
		v.Email("email", e)
	}
	if in.Password != nil {
		ValidatePassword(v, "password", *in.Password)
	}
	if in.Role != nil {
		v.OneOf("role", *in.Role, rbac.Roles...)
	}
	if in.Email == nil && in.Password == nil && in.Role == nil && in.IsActive == nil {
		v.Add("body", "at least one of email, password, role, is_active is required")
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	before, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	// Lock-out protection: an admin may not demote/deactivate themselves, and the last
	// active admin may never be demoted or deactivated.
	losesAdmin := before.Role == rbac.RoleAdmin && before.IsActive &&
		((in.Role != nil && *in.Role != rbac.RoleAdmin) || (in.IsActive != nil && !*in.IsActive))
	if losesAdmin {
		if p, ok := auth.From(ctx); ok && p.UserID == id {
			return nil, apperrors.Unprocessable("You cannot demote or deactivate your own administrator account")
		}
		n, err := s.repo.CountOtherActiveAdmins(ctx, id)
		if err != nil {
			return nil, apperrors.Internal(err)
		}
		if n == 0 {
			return nil, apperrors.Unprocessable("The last active administrator cannot be demoted or deactivated")
		}
	}

	up := UpdateParams{Email: in.Email, Role: in.Role, IsActive: in.IsActive}
	if in.Password != nil {
		h, err := security.HashPassword(*in.Password, s.params)
		if err != nil {
			return nil, apperrors.Internal(err)
		}
		up.PasswordHash = &h
	}
	after, err := s.repo.Update(ctx, id, up)
	if err != nil {
		return nil, err
	}
	// Deactivation, password reset and role changes end existing sessions.
	if in.Password != nil || (in.IsActive != nil && !*in.IsActive) || (in.Role != nil && *in.Role != before.Role) {
		if err := s.repo.RevokeRefreshTokens(ctx, id); err != nil {
			return nil, apperrors.Internal(err)
		}
	}
	changes := map[string]any{}
	if before.Email != after.Email {
		changes["email"] = map[string]any{"from": before.Email, "to": after.Email}
	}
	if before.Role != after.Role {
		changes["role"] = map[string]any{"from": before.Role, "to": after.Role}
	}
	if before.IsActive != after.IsActive {
		changes["is_active"] = map[string]any{"from": before.IsActive, "to": after.IsActive}
	}
	if in.Password != nil {
		changes["credentials_rotated"] = true
	}
	s.audit.Record(ctx, audit.Entry{Action: "user.update", EntityType: "user", EntityID: &id, Changes: changes})
	return after, nil
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (*User, error) { return s.repo.Get(ctx, id) }

func (s *Service) List(ctx context.Context, f ListFilter, p pagination.Params) ([]User, int64, error) {
	return s.repo.List(ctx, f, p)
}

// Bootstrap creates the first administrator when no admin exists. It is a no-op otherwise.
func (s *Service) Bootstrap(ctx context.Context, username, email, password string) (bool, error) {
	n, err := s.repo.CountAdmins(ctx)
	if err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil
	}
	t := true
	_, err = s.Create(ctx, CreateInput{Username: username, Email: email, Password: password, Role: rbac.RoleAdmin, IsActive: &t})
	return err == nil, err
}
