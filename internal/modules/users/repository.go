// Package users implements user lifecycle and role assignment (ADMIN only).
package users

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"pms/internal/middleware/auth"
	"pms/internal/platform/database"
	"pms/internal/shared/apperrors"
	"pms/internal/shared/pagination"
)

type User struct {
	ID        uuid.UUID `json:"id"`
	Username  string    `json:"username"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	IsActive  bool      `json:"is_active"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Credentials is used only by the auth module for login.
type Credentials struct {
	User
	PasswordHash string
}

type Repository struct{ db database.DBTX }

func NewRepository(db database.DBTX) *Repository { return &Repository{db: db} }

const userCols = `u.id, u.username, u.email, r.name, u.is_active, u.created_at, u.updated_at`
const userFrom = ` FROM users u JOIN roles r ON r.id = u.role_id`

func scanUser(row interface{ Scan(...any) error }, u *User) error {
	return row.Scan(&u.ID, &u.Username, &u.Email, &u.Role, &u.IsActive, &u.CreatedAt, &u.UpdatedAt)
}

func (r *Repository) Get(ctx context.Context, id uuid.UUID) (*User, error) {
	var u User
	err := scanUser(r.db.QueryRow(ctx, `SELECT `+userCols+userFrom+` WHERE u.id=$1`, id), &u)
	if database.IsNoRows(err) {
		return nil, apperrors.NotFound("User")
	}
	return &u, err
}

// FindByLogin matches a lower-cased username or email. Returns (nil, nil) when absent.
func (r *Repository) FindByLogin(ctx context.Context, ident string) (*Credentials, error) {
	var c Credentials
	err := r.db.QueryRow(ctx, `SELECT `+userCols+`, u.password_hash`+userFrom+
		` WHERE u.username=$1 OR u.email=$1`, strings.ToLower(ident)).
		Scan(&c.ID, &c.Username, &c.Email, &c.Role, &c.IsActive, &c.CreatedAt, &c.UpdatedAt, &c.PasswordHash)
	if database.IsNoRows(err) {
		return nil, nil
	}
	return &c, err
}

// LoadPrincipal implements auth.Loader; inactive or missing users yield (nil, nil).
func (r *Repository) LoadPrincipal(ctx context.Context, id uuid.UUID) (*auth.Principal, error) {
	var p auth.Principal
	var active bool
	err := r.db.QueryRow(ctx, `SELECT u.id, u.username, r.name, u.is_active`+userFrom+` WHERE u.id=$1`, id).
		Scan(&p.UserID, &p.Username, &p.Role, &active)
	if database.IsNoRows(err) || (err == nil && !active) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

type ListFilter struct {
	Role     string
	IsActive *bool
	Search   string
}

func EscapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func (r *Repository) List(ctx context.Context, f ListFilter, p pagination.Params) ([]User, int64, error) {
	where, args := []string{"TRUE"}, []any{}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", "$"+strconv.Itoa(len(args))))
	}
	if f.Role != "" {
		add("r.name = ?", f.Role)
	}
	if f.IsActive != nil {
		add("u.is_active = ?", *f.IsActive)
	}
	if f.Search != "" {
		add(`(u.username ILIKE ? ESCAPE '\' OR u.email ILIKE ? ESCAPE '\')`, "%"+EscapeLike(f.Search)+"%")
	}
	w := strings.Join(where, " AND ")
	var total int64
	if err := r.db.QueryRow(ctx, `SELECT count(*)`+userFrom+` WHERE `+w, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, p.Limit, p.Offset())
	rows, err := r.db.Query(ctx, `SELECT `+userCols+userFrom+` WHERE `+w+
		` ORDER BY u.created_at DESC, u.id LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		var u User
		if err := scanUser(rows, &u); err != nil {
			return nil, 0, err
		}
		out = append(out, u)
	}
	return out, total, rows.Err()
}

func (r *Repository) Create(ctx context.Context, username, email, hash, role string, active bool) (*User, error) {
	var id uuid.UUID
	err := r.db.QueryRow(ctx, `INSERT INTO users (role_id, username, email, password_hash, is_active)
		VALUES ((SELECT id FROM roles WHERE name=$1), $2, $3, $4, $5) RETURNING id`, role, username, email, hash, active).Scan(&id)
	if err != nil {
		return nil, database.MapError(err)
	}
	return r.Get(ctx, id)
}

type UpdateParams struct {
	Email        *string
	PasswordHash *string
	Role         *string
	IsActive     *bool
}

func (r *Repository) Update(ctx context.Context, id uuid.UUID, u UpdateParams) (*User, error) {
	sets, args := []string{"updated_at = now()"}, []any{}
	add := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, col+" = $"+strconv.Itoa(len(args)))
	}
	if u.Email != nil {
		add("email", *u.Email)
	}
	if u.PasswordHash != nil {
		add("password_hash", *u.PasswordHash)
	}
	if u.Role != nil {
		args = append(args, *u.Role)
		sets = append(sets, "role_id = (SELECT id FROM roles WHERE name = $"+strconv.Itoa(len(args))+")")
	}
	if u.IsActive != nil {
		add("is_active", *u.IsActive)
	}
	args = append(args, id)
	tag, err := r.db.Exec(ctx, `UPDATE users SET `+strings.Join(sets, ", ")+` WHERE id = $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, database.MapError(err)
	}
	if tag.RowsAffected() == 0 {
		return nil, apperrors.NotFound("User")
	}
	return r.Get(ctx, id)
}

// CountOtherActiveAdmins counts active ADMIN users excluding id.
func (r *Repository) CountOtherActiveAdmins(ctx context.Context, id uuid.UUID) (int, error) {
	var n int
	err := r.db.QueryRow(ctx, `SELECT count(*)`+userFrom+` WHERE r.name='ADMIN' AND u.is_active AND u.id <> $1`, id).Scan(&n)
	return n, err
}

func (r *Repository) CountAdmins(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRow(ctx, `SELECT count(*)`+userFrom+` WHERE r.name='ADMIN'`).Scan(&n)
	return n, err
}

// RevokeRefreshTokens revokes every live refresh token of a user.
func (r *Repository) RevokeRefreshTokens(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.Exec(ctx, `UPDATE refresh_tokens SET revoked_at = now() WHERE user_id=$1 AND revoked_at IS NULL`, id)
	return err
}
