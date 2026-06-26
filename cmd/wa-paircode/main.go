// Command wa-paircode runs the WhatsApp multi-device pairing-by-code flow
// ("link with phone number") instead of QR scanning.
//
// It opens (or creates) a SQLite creds database, connects to the REAL WhatsApp,
// and prints an 8-character pairing code. On the phone, open WhatsApp >
// Linked devices > Link with phone number, and type the code. On a successful
// login it exits 0. This connects to the REAL WhatsApp and is meant to be run by
// a human with a sacrificial chip; it is NOT part of `go test`.
//
// Usage:
//
//	go run ./cmd/wa-paircode -phone 5511999998888 [-db ./wa-paircode.creds.db] [-timeout 180s] [-debug]
//
// -phone is the E.164 number WITHOUT a leading '+'.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jfelipesjc/wa-go/internal/client"
	"github.com/jfelipesjc/wa-go/internal/store"
)

func main() {
	dbPath := flag.String("db", "./wa-paircode.creds.db", "path to the SQLite creds database")
	phone := flag.String("phone", "", "E.164 phone number WITHOUT '+' (e.g. 5511999998888) — REQUIRED")
	timeout := flag.Duration("timeout", 180*time.Second, "overall timeout for the pairing/login flow")
	debug := flag.Bool("debug", false, "verbose pairing diagnostics to stderr")
	listen := flag.Bool("listen", false, "after login, keep running and print incoming messages")
	flag.Parse()

	if *debug {
		client.EnableDebug(os.Stderr)
	}

	if err := run(runConfig{
		dbPath:  *dbPath,
		phone:   strings.TrimSpace(*phone),
		timeout: *timeout,
		listen:  *listen,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "wa-paircode: %v\n", err)
		os.Exit(1)
	}
}

type runConfig struct {
	dbPath  string
	phone   string
	timeout time.Duration
	listen  bool
}

// formatCode renders the 8-char code as XXXX-XXXX for readability.
func formatCode(code string) string {
	if len(code) == 8 {
		return code[:4] + "-" + code[4:]
	}
	return code
}

func run(cfg runConfig) error {
	if cfg.phone == "" {
		return fmt.Errorf("-phone is required (E.164 number without '+', e.g. 5511999998888)")
	}
	// Reject a leading '+' or non-digits early; the server expects bare digits.
	for _, r := range cfg.phone {
		if r < '0' || r > '9' {
			return fmt.Errorf("-phone must be digits only (no '+', spaces, or dashes); got %q", cfg.phone)
		}
	}

	st, err := store.OpenSQLite(cfg.dbPath)
	if err != nil {
		return fmt.Errorf("open store %q: %w", cfg.dbPath, err)
	}
	defer st.Close()

	fmt.Printf("wa-paircode: using creds DB %s (phone %s, timeout %s)\n", cfg.dbPath, cfg.phone, cfg.timeout)

	c := client.New(st)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.timeout)
	defer cancel()

	connErr := make(chan error, 1)
	go func() { connErr <- c.ConnectWithPairingCode(ctx, cfg.phone) }()

	for {
		select {
		case e, ok := <-c.Events():
			if !ok {
				if err := <-connErr; err != nil {
					return err
				}
				return nil
			}
			switch ev := e.(type) {
			case client.PairingCodeEvent:
				fmt.Println()
				fmt.Println("============================================")
				fmt.Println(" WhatsApp > Linked devices > Link with phone number")
				fmt.Printf("   DIGITE NO CELULAR:   %s\n", formatCode(ev.Code))
				fmt.Println("============================================")
				fmt.Println()
			case client.PairSuccessEvent:
				fmt.Printf("\nPaired! JID: %s\n", ev.JID)
				fmt.Println("Reconnecting to log in...")
			case client.LoggedInEvent:
				fmt.Println("\nLogged in successfully. Credentials saved.")
				if cfg.listen {
					fmt.Println("Keeping connection open (incoming messages); Ctrl-C to stop...")
					continue
				}
				cancel()
				return nil
			case client.MessageEvent:
				who := ev.From
				if ev.IsGroup && ev.Sender != "" {
					who = fmt.Sprintf("%s in %s", ev.Sender, ev.From)
				}
				fmt.Printf("\n[%s] Message from %s (id=%s):\n   %q\n", ev.Type, who, ev.ID, ev.Text)
			case client.DisconnectedEvent:
				fmt.Printf("Disconnected: %s\n", ev.Reason)
			}
		case <-ctx.Done():
			return fmt.Errorf("timed out after %s", cfg.timeout)
		}
	}
}
