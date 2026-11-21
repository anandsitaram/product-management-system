// Command reconcile compares image objects in storage with product_images metadata.
//
//	reconcile            report orphaned objects and metadata rows whose object is missing
//	reconcile --delete   additionally delete orphaned objects
//
// Run it on a schedule (cron / platform scheduler). Objects younger than --min-age are skipped
// so in-flight uploads are never treated as orphans.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"pms/internal/config"
	"pms/internal/modules/media"
	"pms/internal/platform/database"
	"pms/internal/platform/storage"
	"pms/internal/server"
)

func main() {
	del := flag.Bool("delete", false, "delete orphaned objects")
	minAge := flag.Duration("min-age", 15*time.Minute, "ignore objects newer than this (in-flight uploads)")
	flag.Parse()
	if err := run(*del, *minAge); err != nil {
		fmt.Fprintln(os.Stderr, "reconcile:", err)
		os.Exit(1)
	}
}

func run(del bool, minAge time.Duration) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	pool, err := database.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	store, err := server.NewStorage(ctx, cfg)
	if err != nil {
		return err
	}
	known, err := media.NewRepository(pool).AllKeys(ctx)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	var orphans []string
	err = store.List(ctx, "products/", func(o storage.Object) error {
		seen[o.Key] = true
		if !known[o.Key] && time.Since(o.LastModified) > minAge {
			orphans = append(orphans, o.Key)
		}
		return nil
	})
	if err != nil {
		return err
	}
	var missing []string
	for k := range known {
		if !seen[k] {
			missing = append(missing, k)
		}
	}
	sort.Strings(orphans)
	sort.Strings(missing)
	fmt.Printf("objects in storage: %d, metadata rows: %d\n", len(seen), len(known))
	fmt.Printf("orphaned objects (no metadata): %d\n", len(orphans))
	for _, k := range orphans {
		fmt.Println("  ", k)
		if del {
			if err := store.Delete(ctx, k); err != nil {
				return fmt.Errorf("delete %s: %w", k, err)
			}
		}
	}
	fmt.Printf("metadata rows with missing object: %d\n", len(missing))
	for _, k := range missing {
		fmt.Println("  ", k)
	}
	if len(orphans) > 0 && !del {
		fmt.Println("re-run with --delete to remove orphaned objects")
	}
	return nil
}
