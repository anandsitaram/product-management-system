// Package media implements product image upload, listing, primary selection and deletion.
package media

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"pms/internal/platform/database"
	"pms/internal/shared/apperrors"
)

type Image struct {
	ID               uuid.UUID  `json:"id"`
	ProductID        uuid.UUID  `json:"product_id"`
	OriginalFilename string     `json:"original_filename"`
	ContentType      string     `json:"content_type"`
	FileSize         int64      `json:"file_size"`
	IsPrimary        bool       `json:"is_primary"`
	CreatedAt        time.Time  `json:"created_at"`
	URL              string     `json:"url,omitempty"`
	URLExpiresAt     *time.Time `json:"url_expires_at,omitempty"`
	ObjectKey        string     `json:"-"`
}

type Repository struct{ pool *pgxpool.Pool }

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

const cols = `id, product_id, object_key, original_filename, content_type, file_size, is_primary, created_at`

func scan(row interface{ Scan(...any) error }, i *Image) error {
	return row.Scan(&i.ID, &i.ProductID, &i.ObjectKey, &i.OriginalFilename, &i.ContentType, &i.FileSize, &i.IsPrimary, &i.CreatedAt)
}

func (r *Repository) ProductExists(ctx context.Context, id uuid.UUID) (bool, error) {
	var ok bool
	err := r.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM products WHERE id=$1 AND deleted_at IS NULL)`, id).Scan(&ok)
	return ok, err
}

func (r *Repository) Count(ctx context.Context, productID uuid.UUID) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx, `SELECT count(*) FROM product_images WHERE product_id=$1`, productID).Scan(&n)
	return n, err
}

func (r *Repository) List(ctx context.Context, productID uuid.UUID) ([]Image, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+cols+` FROM product_images WHERE product_id=$1
		ORDER BY is_primary DESC, created_at, id`, productID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Image{}
	for rows.Next() {
		var i Image
		if err := scan(rows, &i); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

func (r *Repository) Get(ctx context.Context, productID, imageID uuid.UUID) (*Image, error) {
	var i Image
	err := scan(r.pool.QueryRow(ctx, `SELECT `+cols+` FROM product_images WHERE id=$1 AND product_id=$2`, imageID, productID), &i)
	if database.IsNoRows(err) {
		return nil, apperrors.NotFound("Image")
	}
	return &i, err
}

// lockProduct serialises image mutations for a product and verifies it is not deleted.
func lockProduct(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	var x uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM products WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, id).Scan(&x)
	if database.IsNoRows(err) {
		return apperrors.NotFound("Product")
	}
	return err
}

// Insert adds the image under a product row lock, enforcing the per-product maximum and primary uniqueness.
func (r *Repository) Insert(ctx context.Context, in Image, wantPrimary bool, max int) (*Image, error) {
	var out Image
	err := database.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := lockProduct(ctx, tx, in.ProductID); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM product_images WHERE product_id=$1`, in.ProductID).Scan(&n); err != nil {
			return err
		}
		if n >= max {
			return errTooMany
		}
		primary := wantPrimary || n == 0
		if primary {
			if _, err := tx.Exec(ctx, `UPDATE product_images SET is_primary=false WHERE product_id=$1 AND is_primary`, in.ProductID); err != nil {
				return err
			}
		}
		return scan(tx.QueryRow(ctx, `INSERT INTO product_images (product_id, object_key, original_filename, content_type, file_size, is_primary)
			VALUES ($1,$2,$3,$4,$5,$6) RETURNING `+cols,
			in.ProductID, in.ObjectKey, in.OriginalFilename, in.ContentType, in.FileSize, primary), &out)
	})
	return &out, database.MapError(err)
}

func (r *Repository) SetPrimary(ctx context.Context, productID, imageID uuid.UUID) (*Image, error) {
	var out Image
	err := database.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := lockProduct(ctx, tx, productID); err != nil {
			return err
		}
		var x uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM product_images WHERE id=$1 AND product_id=$2`, imageID, productID).Scan(&x); err != nil {
			if database.IsNoRows(err) {
				return apperrors.NotFound("Image")
			}
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE product_images SET is_primary=false WHERE product_id=$1 AND is_primary`, productID); err != nil {
			return err
		}
		return scan(tx.QueryRow(ctx, `UPDATE product_images SET is_primary=true WHERE id=$1 RETURNING `+cols, imageID), &out)
	})
	return &out, database.MapError(err)
}

// Delete removes the metadata row and promotes the oldest remaining image if the primary was removed.
// It returns the deleted image so the caller can remove the object.
func (r *Repository) Delete(ctx context.Context, productID, imageID uuid.UUID) (*Image, error) {
	var out Image
	err := database.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := lockProduct(ctx, tx, productID); err != nil {
			return err
		}
		err := scan(tx.QueryRow(ctx, `DELETE FROM product_images WHERE id=$1 AND product_id=$2 RETURNING `+cols, imageID, productID), &out)
		if database.IsNoRows(err) {
			return apperrors.NotFound("Image")
		}
		if err != nil {
			return err
		}
		if out.IsPrimary {
			_, err = tx.Exec(ctx, `UPDATE product_images SET is_primary=true WHERE id = (
				SELECT id FROM product_images WHERE product_id=$1 ORDER BY created_at, id LIMIT 1)`, productID)
		}
		return err
	})
	return &out, database.MapError(err)
}

// AllKeys is used by the reconciliation command.
func (r *Repository) AllKeys(ctx context.Context) (map[string]bool, error) {
	rows, err := r.pool.Query(ctx, `SELECT object_key FROM product_images`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		m[k] = true
	}
	return m, rows.Err()
}

var errTooMany = apperrors.Unprocessable("A product can have at most the configured maximum number of images")
