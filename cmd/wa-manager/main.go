// Command wa-manager runs N WhatsApp sessions concurrently in one process using
// the instance Manager (#7). Each session is backed by its own SQLite creds DB.
//
// It supervises every instance (reconnecting with exponential backoff + jitter)
// and prints the aggregated, name-tagged event stream. Like wa-pair, this
// connects to the REAL WhatsApp and is meant to be run by a human; it is NOT
// part of `go test`.
//
// Usage:
//
//	go run ./cmd/wa-manager -dbs a.creds.db,b.creds.db
//	go run ./cmd/wa-manager -dir ./sessions      # opens every *.creds.db there
//	go run ./cmd/wa-manager -dir ./sessions -concurrency 4 -timeout 0
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/felipeleal/wa-go/internal/manager"
	"github.com/felipeleal/wa-go/internal/store"
)

func main() {
	dbs := flag.String("dbs", "", "comma-separated list of SQLite creds DB paths")
	dir := flag.String("dir", "", "directory to scan for *.creds.db files")
	concurrency := flag.Int("concurrency", 8, "max instances connecting at once")
	timeout := flag.Duration("timeout", 0, "overall run timeout (0 = run until Ctrl-C)")
	flag.Parse()

	if err := run(*dbs, *dir, *concurrency, *timeout); err != nil {
		fmt.Fprintf(os.Stderr, "wa-manager: %v\n", err)
		os.Exit(1)
	}
}

func run(dbs, dir string, concurrency int, timeout time.Duration) error {
	paths, err := collectDBs(dbs, dir)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return fmt.Errorf("no creds DBs given; use -dbs or -dir")
	}

	m := manager.New(manager.WithConcurrency(concurrency))

	var stores []store.Store
	defer func() {
		for _, s := range stores {
			_ = s.Close()
		}
	}()

	for _, p := range paths {
		st, err := store.OpenSQLite(p)
		if err != nil {
			return fmt.Errorf("open store %q: %w", p, err)
		}
		stores = append(stores, st)
		name := instanceName(p)
		if _, err := m.Add(name, st); err != nil {
			return fmt.Errorf("add instance %q: %w", name, err)
		}
		fmt.Printf("wa-manager: registered %q -> %s\n", name, p)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	// Ctrl-C / SIGTERM triggers a clean stop.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		fmt.Println("\nwa-manager: shutting down...")
		cancel()
	}()

	m.Start(ctx)
	fmt.Printf("wa-manager: started %d instances (concurrency %d)\n", len(paths), concurrency)

	for {
		select {
		case ev, ok := <-m.Events():
			if !ok {
				return nil
			}
			fmt.Printf("[%s] %s\n", ev.Name, describe(ev.Event))
		case <-ctx.Done():
			m.Stop()
			fmt.Println("wa-manager: stopped.")
			return nil
		}
	}
}

// collectDBs resolves the -dbs CSV and/or the -dir scan into a path list.
func collectDBs(dbs, dir string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, p := range strings.Split(dbs, ",") {
		add(strings.TrimSpace(p))
	}
	if dir != "" {
		matches, err := filepath.Glob(filepath.Join(dir, "*.creds.db"))
		if err != nil {
			return nil, fmt.Errorf("scan %q: %w", dir, err)
		}
		for _, p := range matches {
			add(p)
		}
	}
	return out, nil
}

// instanceName derives a short label from a DB path: the base name minus the
// ".creds.db" / ".db" suffix.
func instanceName(p string) string {
	b := filepath.Base(p)
	b = strings.TrimSuffix(b, ".creds.db")
	b = strings.TrimSuffix(b, ".db")
	if b == "" {
		return p
	}
	return b
}

// describe renders a client.Event for the console.
func describe(e interface{}) string {
	return fmt.Sprintf("%T %+v", e, e)
}
