// Package products implements product CRUD, search/filtering and soft deletion.
package products

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"pms/internal/platform/database"
	"pms/internal/shared/apperrors"
	"pms/internal/shared/pagination"
)

type CategoryRef struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

type LabelRef struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

type Product struct {
	ID            uuid.UUID    `json:"id"`
	SKU           string       `json:"sku"`
	Name          string       `json:"name"`
	Description   *string      `json:"description"`
	Price         json.Number  `json:"price"`
	StockQuantity int          `json:"stock_quantity"`
	CategoryID    *uuid.UUID   `json:"category_id"`
	Category      *CategoryRef `json:"category"`
	Status        string       `json:"status"`
	Labels        []LabelRef   `json:"labels"`
	CreatedBy     *uuid.UUID   `json:"created_by"`
	CreatedAt     time.Time    `json:"created_at"`
	UpdatedAt     time.Time    `json:"updated_at"`
}

// Data is the writable part of a product.
type Data struct {
	SKU           string
	Name          string
	Description   *string
	Price         string
	StockQuantity int
	CategoryID    *uuid.UUID
	Status        string
}

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

const selectCols = `p.id, p.sku, p.name, p.description, p.price::text, p.stock_quantity, p.category_id,
	c.id, c.name, p.status, p.created_by, p.created_at, p.updated_at`

const fromJoin = ` FROM products p LEFT JOIN categories c ON c.id = p.category_id`

func scanProduct(row interface{ Scan(...any) error }) (*Product, error) {
	var p Product
	var price string
	var cid *uuid.UUID
	var cname *string
	if err := row.Scan(&p.ID, &p.SKU, &p.Name, &p.Description, &price, &p.StockQuantity, &p.CategoryID,
		&cid, &cname, &p.Status, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	p.Price = json.Number(price)
	if cid != nil && cname != nil {
		p.Category = &CategoryRef{ID: *cid, Name: *cname}
	}
	p.Labels = []LabelRef{}
	return &p, nil
}

func (r *Repository) attachLabels(ctx context.Context, db database.DBTX, ps []*Product) error {
	if len(ps) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(ps))
	idx := make(map[uuid.UUID]*Product, len(ps))
	for i, p := range ps {
		ids[i] = p.ID
		idx[p.ID] = p
	}
	rows, err := db.Query(ctx, `SELECT pl.product_id, l.id, l.name FROM product_labels pl
		JOIN labels l ON l.id = pl.label_id WHERE pl.product_id = ANY($1) ORDER BY l.name`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var pid uuid.UUID
		var l LabelRef
		if err := rows.Scan(&pid, &l.ID, &l.Name); err != nil {
			return err
		}
		idx[pid].Labels = append(idx[pid].Labels, l)
	}
	return rows.Err()
}

func (r *Repository) get(ctx context.Context, db database.DBTX, id uuid.UUID) (*Product, error) {
	p, err := scanProduct(db.QueryRow(ctx, `SELECT `+selectCols+fromJoin+` WHERE p.id=$1 AND p.deleted_at IS NULL`, id))
	if database.IsNoRows(err) {
		return nil, apperrors.NotFound("Product")
	}
	if err != nil {
		return nil, err
	}
	if err := r.attachLabels(ctx, db, []*Product{p}); err != nil {
		return nil, err
	}
	return p, nil
}

func (r *Repository) Get(ctx context.Context, id uuid.UUID) (*Product, error) {
	return r.get(ctx, r.pool, id)
}

type Filter struct {
	Search     string
	CategoryID *uuid.UUID
	LabelID    *uuid.UUID
	Status     string
	MinPrice   string
	MaxPrice   string
	SortBy     string
	Desc       bool
}

var sortColumns = map[string]string{
	"name": "p.name", "sku": "p.sku", "price": "p.price", "created_at": "p.created_at", "updated_at": "p.updated_at",
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func (r *Repository) List(ctx context.Context, f Filter, pg pagination.Params) ([]*Product, int64, error) {
	where, args := []string{"p.deleted_at IS NULL"}, []any{}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", "$"+strconv.Itoa(len(args))))
	}
	if f.Search != "" {
		add(`(p.sku ILIKE ? ESCAPE '\' OR p.name ILIKE ? ESCAPE '\' OR p.description ILIKE ? ESCAPE '\')`, "%"+escapeLike(f.Search)+"%")
	}
	if f.CategoryID != nil {
		add("p.category_id = ?", *f.CategoryID)
	}
	if f.LabelID != nil {
		add("EXISTS (SELECT 1 FROM product_labels pl WHERE pl.product_id = p.id AND pl.label_id = ?)", *f.LabelID)
	}
	if f.Status != "" {
		add("p.status = ?", f.Status)
	}
	if f.MinPrice != "" {
		add("p.price >= ?::numeric", f.MinPrice)
	}
	if f.MaxPrice != "" {
		add("p.price <= ?::numeric", f.MaxPrice)
	}
	w := strings.Join(where, " AND ")

	var total int64
	if err := r.pool.QueryRow(ctx, `SELECT count(*)`+fromJoin+` WHERE `+w, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	col, ok := sortColumns[f.SortBy]
	if !ok {
		col = "p.created_at"
	}
	dir := "ASC"
	if f.Desc {
		dir = "DESC"
	}
	args = append(args, pg.Limit, pg.Offset())
	q := fmt.Sprintf(`SELECT %s%s WHERE %s ORDER BY %s %s, p.id %s LIMIT $%d OFFSET $%d`,
		selectCols, fromJoin, w, col, dir, dir, len(args)-1, len(args))
	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*Product{}
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	rows.Close()
	return out, total, r.attachLabels(ctx, r.pool, out)
}

// CheckCategory ensures the category exists and is active.
func (r *Repository) CheckCategory(ctx context.Context, id uuid.UUID) (bool, error) {
	var active bool
	err := r.pool.QueryRow(ctx, `SELECT is_active FROM categories WHERE id=$1`, id).Scan(&active)
	if database.IsNoRows(err) {
		return false, nil
	}
	return active, err
}

// InvalidLabels returns the subset of ids that do not exist or are inactive.
func (r *Repository) InvalidLabels(ctx context.Context, ids []uuid.UUID) ([]uuid.UUID, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT id FROM labels WHERE id = ANY($1) AND is_active`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ok := map[uuid.UUID]bool{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ok[id] = true
	}
	var bad []uuid.UUID
	for _, id := range ids {
		if !ok[id] {
			bad = append(bad, id)
		}
	}
	return bad, rows.Err()
}

func replaceLabels(ctx context.Context, tx pgx.Tx, productID uuid.UUID, labelIDs []uuid.UUID) error {
	if _, err := tx.Exec(ctx, `DELETE FROM product_labels WHERE product_id=$1`, productID); err != nil {
		return err
	}
	if len(labelIDs) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO product_labels (product_id, label_id)
		SELECT $1, unnest($2::uuid[]) ON CONFLICT DO NOTHING`, productID, labelIDs)
	return err
}

func (r *Repository) Create(ctx context.Context, d Data, labelIDs []uuid.UUID, createdBy *uuid.UUID) (*Product, error) {
	var out *Product
	err := database.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		var id uuid.UUID
		if err := tx.QueryRow(ctx, `INSERT INTO products (sku, name, description, price, stock_quantity, category_id, status, created_by)
			VALUES ($1,$2,$3,$4::numeric,$5,$6,$7,$8) RETURNING id`,
			d.SKU, d.Name, d.Description, d.Price, d.StockQuantity, d.CategoryID, d.Status, createdBy).Scan(&id); err != nil {
			return err
		}
		if err := replaceLabels(ctx, tx, id, labelIDs); err != nil {
			return err
		}
		p, err := r.get(ctx, tx, id)
		out = p
		return err
	})
	return out, database.MapError(err)
}

// Update replaces the writable fields; labelIDs == nil leaves labels untouched.
func (r *Repository) Update(ctx context.Context, id uuid.UUID, d Data, labelIDs *[]uuid.UUID) (*Product, error) {
	var out *Product
	err := database.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE products SET sku=$2, name=$3, description=$4, price=$5::numeric,
			stock_quantity=$6, category_id=$7, status=$8, updated_at=now()
			WHERE id=$1 AND deleted_at IS NULL`,
			id, d.SKU, d.Name, d.Description, d.Price, d.StockQuantity, d.CategoryID, d.Status)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return apperrors.NotFound("Product")
		}
		if labelIDs != nil {
			if err := replaceLabels(ctx, tx, id, *labelIDs); err != nil {
				return err
			}
		}
		p, err := r.get(ctx, tx, id)
		out = p
		return err
	})
	return out, database.MapError(err)
}

func (r *Repository) SoftDelete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `UPDATE products SET deleted_at=now(), updated_at=now() WHERE id=$1 AND deleted_at IS NULL`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperrors.NotFound("Product")
	}
	return nil
}
