// Package database wraps pgx pool creation, transactions and error mapping.
package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"pms/internal/shared/apperrors"
)

// DBTX is satisfied by *pgxpool.Pool and pgx.Tx.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	cfg.MaxConns = 20
	cfg.MinConns = 1
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 15 * time.Minute
	cfg.HealthCheckPeriod = time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

// WithTx runs fn in a transaction, committing on nil and rolling back on error.
func WithTx(ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

var constraintFields = map[string]string{
	"users_username_key":            "username",
	"users_email_key":               "email",
	"products_sku_key":              "sku",
	"categories_name_key":           "name",
	"labels_name_key":               "name",
	"product_images_object_key_key": "object_key",
}

// MapError converts well-known PostgreSQL errors into API errors; others pass through.
func MapError(err error) error {
	if err == nil {
		return nil
	}
	var ae *apperrors.Error
	if errors.As(err, &ae) {
		return err
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			if pgErr.ConstraintName == "ux_product_one_primary_image" {
				return apperrors.Conflict("The product already has a primary image")
			}
			if f, ok := constraintFields[pgErr.ConstraintName]; ok {
				return apperrors.Conflict("A record with this " + f + " already exists")
			}
			return apperrors.Conflict("A conflicting record already exists")
		case "23503":
			return apperrors.Unprocessable("A referenced record does not exist or the record is still in use")
		case "23514", "22P02", "22003":
			return apperrors.Unprocessable("A value violates a data constraint")
		}
	}
	return err
}

func IsNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
