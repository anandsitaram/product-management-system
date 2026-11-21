// Package auth implements login, refresh-token rotation and logout.
package auth

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	mw "pms/internal/middleware/auth"
	"pms/internal/modules/audit"
	"pms/internal/modules/users"
	"pms/internal/platform/database"
	"pms/internal/security"
	"pms/internal/shared/apperrors"
	"pms/internal/shared/request"
	"pms/internal/shared/response"
	"pms/internal/shared/validation"
)

type Repository struct{ db database.DBTX }

func NewRepository(db database.DBTX) *Repository { return &Repository{db: db} }

func (r *Repository) Insert(ctx context.Context, userID uuid.UUID, hash string, exp time.Time) error {
	_, err := r.db.Exec(ctx, `INSERT INTO refresh_tokens (user_id, token_hash, expires_at) VALUES ($1,$2,$3)`, userID, hash, exp)
	return err
}

// Claim atomically revokes a live token and returns its owner. (uuid.Nil, false) means the token
// was unknown, expired or already used.
func (r *Repository) Claim(ctx context.Context, hash string) (uuid.UUID, bool, error) {
	var id uuid.UUID
	err := r.db.QueryRow(ctx, `UPDATE refresh_tokens SET revoked_at = now()
		WHERE token_hash=$1 AND revoked_at IS NULL AND expires_at > now() RETURNING user_id`, hash).Scan(&id)
	if database.IsNoRows(err) {
		return uuid.Nil, false, nil
	}
	return id, err == nil, err
}

// OwnerOfRevoked returns the owner of a token that exists but was already revoked (reuse detection).
func (r *Repository) OwnerOfRevoked(ctx context.Context, hash string) (uuid.UUID, bool, error) {
	var id uuid.UUID
	err := r.db.QueryRow(ctx, `SELECT user_id FROM refresh_tokens WHERE token_hash=$1 AND revoked_at IS NOT NULL`, hash).Scan(&id)
	if database.IsNoRows(err) {
		return uuid.Nil, false, nil
	}
	return id, err == nil, err
}

func (r *Repository) RevokeOwned(ctx context.Context, hash string, userID uuid.UUID) error {
	_, err := r.db.Exec(ctx, `UPDATE refresh_tokens SET revoked_at = now()
		WHERE token_hash=$1 AND user_id=$2 AND revoked_at IS NULL`, hash, userID)
	return err
}

func (r *Repository) RevokeAll(ctx context.Context, userID uuid.UUID) error {
	_, err := r.db.Exec(ctx, `UPDATE refresh_tokens SET revoked_at = now() WHERE user_id=$1 AND revoked_at IS NULL`, userID)
	return err
}

type Service struct {
	repo       *Repository
	users      *users.Repository
	tokens     *security.TokenManager
	audit      *audit.Recorder
	refreshTTL time.Duration
	dummyHash  string
}

func NewService(repo *Repository, u *users.Repository, tm *security.TokenManager, rec *audit.Recorder,
	refreshTTL time.Duration, p security.Argon2Params) (*Service, error) {
	// Verified when the user does not exist so response time does not reveal valid usernames.
	dummy, err := security.HashPassword("dummy-password-for-timing", p)
	if err != nil {
		return nil, err
	}
	return &Service{repo: repo, users: u, tokens: tm, audit: rec, refreshTTL: refreshTTL, dummyHash: dummy}, nil
}

type TokenPair struct {
	AccessToken  string      `json:"access_token"`
	TokenType    string      `json:"token_type"`
	ExpiresIn    int         `json:"expires_in"`
	RefreshToken string      `json:"refresh_token"`
	User         *users.User `json:"user,omitempty"`
}

var errInvalidCreds = apperrors.Unauthorized("Invalid credentials")

func (s *Service) issue(ctx context.Context, u *users.User) (*TokenPair, error) {
	access, _, err := s.tokens.IssueAccess(u.ID, u.Role)
	if err != nil {
		return nil, apperrors.Internal(err)
	}
	refresh, hash, err := security.NewRefreshToken()
	if err != nil {
		return nil, apperrors.Internal(err)
	}
	if err := s.repo.Insert(ctx, u.ID, hash, time.Now().Add(s.refreshTTL)); err != nil {
		return nil, apperrors.Internal(err)
	}
	return &TokenPair{AccessToken: access, TokenType: "Bearer", ExpiresIn: int(s.tokens.TTL().Seconds()), RefreshToken: refresh, User: u}, nil
}

func (s *Service) Login(ctx context.Context, ident, password string) (*TokenPair, error) {
	c, err := s.users.FindByLogin(ctx, strings.TrimSpace(ident))
	if err != nil {
		return nil, apperrors.Internal(err)
	}
	if c == nil {
		_, _ = security.VerifyPassword(password, s.dummyHash)
		return nil, errInvalidCreds
	}
	ok, err := security.VerifyPassword(password, c.PasswordHash)
	if err != nil {
		return nil, apperrors.Internal(err)
	}
	if !ok || !c.IsActive { // identical response for wrong password and inactive account
		return nil, errInvalidCreds
	}
	pair, err := s.issue(ctx, &c.User)
	if err != nil {
		return nil, err
	}
	s.audit.Record(ctx, audit.Entry{ActorID: &c.ID, Action: "auth.login", EntityType: "user", EntityID: &c.ID})
	return pair, nil
}

func (s *Service) Refresh(ctx context.Context, token string) (*TokenPair, error) {
	hash := security.HashToken(token)
	uid, ok, err := s.repo.Claim(ctx, hash)
	if err != nil {
		return nil, apperrors.Internal(err)
	}
	if !ok {
		// A revoked token being replayed suggests theft: end every session of that user.
		if owner, reused, err := s.repo.OwnerOfRevoked(ctx, hash); err == nil && reused {
			_ = s.repo.RevokeAll(ctx, owner)
			s.audit.Record(ctx, audit.Entry{ActorID: &owner, Action: "auth.refresh_reuse_detected", EntityType: "user", EntityID: &owner})
		}
		return nil, apperrors.Unauthorized("Invalid or expired refresh token")
	}
	u, err := s.users.Get(ctx, uid)
	if err != nil || !u.IsActive {
		return nil, apperrors.Unauthorized("Invalid or expired refresh token")
	}
	return s.issue(ctx, u)
}

func (s *Service) Logout(ctx context.Context, userID uuid.UUID, token string) error {
	if err := s.repo.RevokeOwned(ctx, security.HashToken(token), userID); err != nil {
		return apperrors.Internal(err)
	}
	s.audit.Record(ctx, audit.Entry{Action: "auth.logout", EntityType: "user", EntityID: &userID})
	return nil
}

type Handler struct{ svc *Service }

func NewHandler(s *Service) *Handler { return &Handler{svc: s} }

func (h *Handler) Token(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"` // username or email
		Password string `json:"password"`
	}
	if err := request.DecodeJSON(w, r, &in); err != nil {
		response.Error(w, r, err)
		return
	}
	v := validation.New()
	v.Required("username", in.Username)
	v.Required("password", in.Password)
	v.MaxLen("username", in.Username, 255)
	v.MaxLen("password", in.Password, 128)
	if err := v.Err(); err != nil {
		response.Error(w, r, err)
		return
	}
	pair, err := h.svc.Login(r.Context(), in.Username, in.Password)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	response.JSON(w, r, http.StatusOK, pair)
}

func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := request.DecodeJSON(w, r, &in); err != nil {
		response.Error(w, r, err)
		return
	}
	v := validation.New()
	v.Required("refresh_token", in.RefreshToken)
	v.MaxLen("refresh_token", in.RefreshToken, 256)
	if err := v.Err(); err != nil {
		response.Error(w, r, err)
		return
	}
	pair, err := h.svc.Refresh(r.Context(), in.RefreshToken)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	response.JSON(w, r, http.StatusOK, pair)
}

func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	p, _ := mw.From(r.Context())
	var in struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := request.DecodeJSON(w, r, &in); err != nil {
		response.Error(w, r, err)
		return
	}
	v := validation.New()
	v.Required("refresh_token", in.RefreshToken)
	if err := v.Err(); err != nil {
		response.Error(w, r, err)
		return
	}
	if err := h.svc.Logout(r.Context(), p.UserID, in.RefreshToken); err != nil {
		response.Error(w, r, err)
		return
	}
	response.NoContent(w)
}
