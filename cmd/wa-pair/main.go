// Command wa-pair runs the interactive WhatsApp multi-device pairing flow.
//
// It opens (or creates) a SQLite creds database, runs the client, renders the
// pairing QR in the terminal for scanning, and prints lifecycle events. On a
// successful login it exits 0. This connects to the REAL WhatsApp and is meant
// to be run by a human with a sacrificial chip; it is NOT part of `go test`.
//
// Usage:
//
//	go run ./cmd/wa-pair [-db ./wa-pair.creds.db] [-timeout 120s]
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/felipeleal/wa-go/internal/client"
	"github.com/felipeleal/wa-go/internal/store"
	"github.com/mdp/qrterminal/v3"
)

func main() {
	dbPath := flag.String("db", "./wa-pair.creds.db", "path to the SQLite creds database")
	timeout := flag.Duration("timeout", 120*time.Second, "overall timeout for the pairing/login flow")
	flag.Parse()

	if err := run(*dbPath, *timeout); err != nil {
		fmt.Fprintf(os.Stderr, "wa-pair: %v\n", err)
		os.Exit(1)
	}
}

func run(dbPath string, timeout time.Duration) error {
	st, err := store.OpenSQLite(dbPath)
	if err != nil {
		return fmt.Errorf("open store %q: %w", dbPath, err)
	}
	defer st.Close()

	fmt.Printf("wa-pair: using creds DB %s (timeout %s)\n", dbPath, timeout)

	c := client.New(st)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	connErr := make(chan error, 1)
	go func() { connErr <- c.Connect(ctx) }()

	for {
		select {
		case e, ok := <-c.Events():
			if !ok {
				// Channel closed: Connect returned.
				if err := <-connErr; err != nil {
					return err
				}
				return nil
			}
			switch ev := e.(type) {
			case client.QREvent:
				fmt.Println("\nScan this QR with WhatsApp > Linked devices:")
				qrterminal.GenerateWithConfig(ev.Code, qrterminal.Config{
					Level:     qrterminal.L,
					Writer:    os.Stdout,
					BlackChar: qrterminal.BLACK,
					WhiteChar: qrterminal.WHITE,
					QuietZone: 1,
				})
			case client.PairSuccessEvent:
				fmt.Printf("\nPaired! JID: %s\n", ev.JID)
				fmt.Println("Reconnecting to log in...")
			case client.LoggedInEvent:
				fmt.Println("\nLogged in successfully. Credentials saved.")
				cancel()
				return nil
			case client.DisconnectedEvent:
				fmt.Printf("Disconnected: %s\n", ev.Reason)
			}
		case <-ctx.Done():
			return fmt.Errorf("timed out after %s", timeout)
		}
	}
}
