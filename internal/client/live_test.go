//go:build live

// Live integration test (no number, no pairing).
//
// Run with:
//
//	go test -tags live ./internal/client/ -run TestLive_EmitsQR -v
//
// It connects to the REAL WhatsApp WebSocket, runs the Noise handshake with a
// fresh throwaway identity, and asserts that the server delivers a pair-device
// node and the client emits a QREvent within 30s. It does NOT scan the QR and
// does NOT pair any number, so it cannot burn a chip. The temporary creds DB is
// created in t.TempDir() and discarded.
package client

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jfelipesjc/wa-go/internal/store"
)

func TestLive_EmitsQR(t *testing.T) {
	dbPath := t.TempDir() + "/live.creds.db"
	st, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	c := New(st)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Run Connect in the background; we only care about the first QR event.
	connErr := make(chan error, 1)
	go func() { connErr <- c.Connect(ctx) }()

	deadline := time.After(30 * time.Second)
	for {
		select {
		case e, ok := <-c.Events():
			if !ok {
				t.Fatalf("event channel closed before a QR was emitted (connect err: %v)", <-connErr)
			}
			switch ev := e.(type) {
			case QREvent:
				if !strings.HasPrefix(ev.Code, "https://wa.me/settings/linked_devices#") {
					t.Fatalf("QR has unexpected format: %q", ev.Code)
				}
				if strings.Count(ev.Code, ",") < 4 {
					t.Fatalf("QR does not have the expected comma-separated parts: %q", ev.Code)
				}
				t.Logf("live QR received OK: %.60s...", ev.Code)
				cancel() // stop the flow; we are done, do NOT pair
				return
			case DisconnectedEvent:
				t.Fatalf("disconnected before QR: %s", ev.Reason)
			}
		case <-deadline:
			t.Fatal("timed out: no QREvent within 30s")
		}
	}
}
