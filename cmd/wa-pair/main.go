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
	"rsc.io/qr"
)

func main() {
	dbPath := flag.String("db", "./wa-pair.creds.db", "path to the SQLite creds database")
	timeout := flag.Duration("timeout", 120*time.Second, "overall timeout for the pairing/login flow")
	pngPath := flag.String("png", "", "if set, also write each QR to this PNG file for scanning")
	debug := flag.Bool("debug", false, "verbose pairing diagnostics to stderr")
	listen := flag.Bool("listen", false, "after login, keep running and print incoming messages")
	sendTo := flag.String("send-to", "", "after login, send a 1:1 text to this JID (e.g. 5512...@s.whatsapp.net)")
	sendText := flag.String("send-text", "", "the text to send with -send-to")
	flag.Parse()

	if *debug {
		client.EnableDebug(os.Stderr)
	}

	if err := run(runConfig{
		dbPath:   *dbPath,
		timeout:  *timeout,
		pngPath:  *pngPath,
		listen:   *listen,
		sendTo:   *sendTo,
		sendText: *sendText,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "wa-pair: %v\n", err)
		os.Exit(1)
	}
}

func writeQRPNG(path, code string) {
	c, err := qr.Encode(code, qr.M)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wa-pair: qr encode: %v\n", err)
		return
	}
	if err := os.WriteFile(path, c.PNG(), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "wa-pair: write png: %v\n", err)
		return
	}
	fmt.Printf("[QR written to %s]\n", path)
}

// runConfig groups the CLI options for run.
type runConfig struct {
	dbPath   string
	timeout  time.Duration
	pngPath  string
	listen   bool
	sendTo   string
	sendText string
}

func run(cfg runConfig) error {
	st, err := store.OpenSQLite(cfg.dbPath)
	if err != nil {
		return fmt.Errorf("open store %q: %w", cfg.dbPath, err)
	}
	defer st.Close()

	fmt.Printf("wa-pair: using creds DB %s (timeout %s)\n", cfg.dbPath, cfg.timeout)

	c := client.New(st)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.timeout)
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
				if cfg.pngPath != "" {
					writeQRPNG(cfg.pngPath, ev.Code)
				}
			case client.PairSuccessEvent:
				fmt.Printf("\nPaired! JID: %s\n", ev.JID)
				fmt.Println("Reconnecting to log in...")
			case client.LoggedInEvent:
				fmt.Println("\nLogged in successfully. Credentials saved.")
				if cfg.sendTo != "" && cfg.sendText != "" {
					// Give the server a moment to settle the session, then send once.
					go func() {
						time.Sleep(2 * time.Second)
						msgID, err := c.SendText(ctx, cfg.sendTo, cfg.sendText)
						if err != nil {
							fmt.Fprintf(os.Stderr, "wa-pair: send failed: %v\n", err)
							return
						}
						fmt.Printf("\n✉️  Sent to %s (id=%s): %q\n", cfg.sendTo, msgID, cfg.sendText)
					}()
				}
				if cfg.listen || (cfg.sendTo != "" && cfg.sendText != "") {
					fmt.Println("Keeping connection open (incoming messages / send ack); Ctrl-C to stop...")
					continue
				}
				cancel()
				return nil
			case client.MessageEvent:
				fmt.Printf("\n📩 Message from %s (id=%s):\n   %q\n", ev.From, ev.ID, ev.Text)
			case client.DisconnectedEvent:
				fmt.Printf("Disconnected: %s\n", ev.Reason)
			}
		case <-ctx.Done():
			return fmt.Errorf("timed out after %s", cfg.timeout)
		}
	}
}
