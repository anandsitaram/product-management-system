package database

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

const migrationLockID = 727274001

type migration struct {
	version string
	up      string
	down    string
}

func load(fsys fs.FS, dir string) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	byVer := map[string]*migration{}
	for _, e := range entries {
		name := e.Name()
		var ver, kind string
		switch {
		case strings.HasSuffix(name, ".up.sql"):
			ver, kind = strings.TrimSuffix(name, ".up.sql"), "up"
		case strings.HasSuffix(name, ".down.sql"):
			ver, kind = strings.TrimSuffix(name, ".down.sql"), "down"
		default:
			continue
		}
		b, err := fs.ReadFile(fsys, dir+"/"+name)
		if err != nil {
			return nil, err
		}
		m := byVer[ver]
		if m == nil {
			m = &migration{version: ver}
			byVer[ver] = m
		}
		if kind == "up" {
			m.up = string(b)
		} else {
			m.down = string(b)
		}
	}
	out := make([]migration, 0, len(byVer))
	for _, m := range byVer {
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

func prepare(ctx context.Context, pool *pgxpool.Pool) (func(), error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	release := func() {
		_, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", int64(migrationLockID))
		conn.Release()
	}
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", int64(migrationLockID)); err != nil {
		conn.Release()
		return nil, err
	}
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

func applied(ctx context.Context, pool *pgxpool.Pool) (map[string]bool, []string, error) {
	rows, err := pool.Query(ctx, "SELECT version FROM schema_migrations ORDER BY version")
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	set := map[string]bool{}
	var order []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, nil, err
		}
		set[v] = true
		order = append(order, v)
	}
	return set, order, rows.Err()
}

// MigrateUp applies all pending migrations and returns the versions applied.
func MigrateUp(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS, dir string) ([]string, error) {
	migs, err := load(fsys, dir)
	if err != nil {
		return nil, err
	}
	release, err := prepare(ctx, pool)
	if err != nil {
		return nil, err
	}
	defer release()
	done, _, err := applied(ctx, pool)
	if err != nil {
		return nil, err
	}
	var ran []string
	for _, m := range migs {
		if done[m.version] || m.up == "" {
			continue
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			return ran, err
		}
		if _, err := tx.Exec(ctx, m.up); err != nil {
			_ = tx.Rollback(ctx)
			return ran, fmt.Errorf("migration %s: %w", m.version, err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", m.version); err != nil {
			_ = tx.Rollback(ctx)
			return ran, err
		}
		if err := tx.Commit(ctx); err != nil {
			return ran, err
		}
		ran = append(ran, m.version)
	}
	return ran, nil
}

// MigrateDown reverts the last `steps` applied migrations (most recent first).
func MigrateDown(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS, dir string, steps int) ([]string, error) {
	migs, err := load(fsys, dir)
	if err != nil {
		return nil, err
	}
	byVer := map[string]migration{}
	for _, m := range migs {
		byVer[m.version] = m
	}
	release, err := prepare(ctx, pool)
	if err != nil {
		return nil, err
	}
	defer release()
	_, order, err := applied(ctx, pool)
	if err != nil {
		return nil, err
	}
	var reverted []string
	for i := len(order) - 1; i >= 0 && len(reverted) < steps; i-- {
		m, ok := byVer[order[i]]
		if !ok || m.down == "" {
			return reverted, fmt.Errorf("migration %s has no down script", order[i])
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			return reverted, err
		}
		if _, err := tx.Exec(ctx, m.down); err != nil {
			_ = tx.Rollback(ctx)
			return reverted, fmt.Errorf("migration %s down: %w", m.version, err)
		}
		if _, err := tx.Exec(ctx, "DELETE FROM schema_migrations WHERE version=$1", m.version); err != nil {
			_ = tx.Rollback(ctx)
			return reverted, err
		}
		if err := tx.Commit(ctx); err != nil {
			return reverted, err
		}
		reverted = append(reverted, m.version)
	}
	return reverted, nil
}
