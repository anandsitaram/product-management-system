// Command migrate applies or reverts database migrations.
//
//	migrate up          apply all pending migrations
//	migrate down [N]    revert the last N migrations (default 1)
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"pms/db"
	"pms/internal/platform/database"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
}

func run() error {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	if len(os.Args) < 2 {
		return fmt.Errorf("usage: migrate up | migrate down [N]")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pool, err := database.Connect(ctx, url)
	if err != nil {
		return err
	}
	defer pool.Close()

	switch os.Args[1] {
	case "up":
		applied, err := database.MigrateUp(ctx, pool, db.Migrations, "migrations")
		fmt.Printf("applied: %v\n", applied)
		return err
	case "down":
		n := 1
		if len(os.Args) > 2 {
			if n, err = strconv.Atoi(os.Args[2]); err != nil || n < 1 {
				return fmt.Errorf("invalid step count %q", os.Args[2])
			}
		}
		reverted, err := database.MigrateDown(ctx, pool, db.Migrations, "migrations", n)
		fmt.Printf("reverted: %v\n", reverted)
		return err
	}
	return fmt.Errorf("unknown command %q", os.Args[1])
}
