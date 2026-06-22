package ws

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/coder/websocket"
)

// startEchoServer starts a local httptest WebSocket server.
// The serveFunc drives what the server does after the handshake.
func startEchoServer(t *testing.T, serveFunc func(ctx context.Context, c *websocket.Conn)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true, // allow any origin in tests
		})
		if err != nil {
			t.Logf("server: accept error: %v", err)
			return
		}
		defer c.CloseNow() //nolint:errcheck
		serveFunc(r.Context(), c)
	}))
	return srv
}

// dialTest dials the given httptest server and returns the adapter.
func dialTest(t *testing.T, srv *httptest.Server) *adapter {
	t.Helper()
	// Convert http:// → ws://.
	wsURL := "ws" + srv.URL[len("http"):]

	c, _, err := websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"https://web.whatsapp.com"}},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c.SetReadLimit(-1)

	return newAdapter(context.Background(), c)
}

// Test 1: one WS message containing two back-to-back frames (3-byte header + payload each).
// Confirm that all bytes arrive intact regardless of how the adapter slices them.
func TestRead_TwoFramesConcatenatedInOneMessage(t *testing.T) {
	// Build two fake frames: [0x00 0x00 0x03 A B C] [0x00 0x00 0x02 D E]
	frame1 := []byte{0x00, 0x00, 0x03, 'A', 'B', 'C'}
	frame2 := []byte{0x00, 0x00, 0x02, 'D', 'E'}
	payload := append(frame1, frame2...)

	srv := startEchoServer(t, func(ctx context.Context, c *websocket.Conn) {
		// Send both frames concatenated in ONE WS message.
		if err := c.Write(ctx, websocket.MessageBinary, payload); err != nil {
			t.Logf("server write: %v", err)
		}
		// Keep the connection open until the client closes.
		<-ctx.Done()
	})
	defer srv.Close()

	adp := dialTest(t, srv)
	defer adp.Close()

	// Read exactly len(payload) bytes — do not use io.ReadAll (blocks until close).
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(adp, got); err != nil {
		t.Fatalf("ReadFull: %v", err)
	}

	if string(got) != string(payload) {
		t.Errorf("got %q, want %q", got, payload)
	}
}

// Test 2: fragmentation — small Read buffer.
// Server sends one message; client reads it one byte at a time.
func TestRead_SmallBuffer(t *testing.T) {
	data := []byte("hello fragmentation world 1234567890")

	srv := startEchoServer(t, func(ctx context.Context, c *websocket.Conn) {
		if err := c.Write(ctx, websocket.MessageBinary, data); err != nil {
			t.Logf("server write: %v", err)
		}
		<-ctx.Done()
	})
	defer srv.Close()

	adp := dialTest(t, srv)
	defer adp.Close()

	var got []byte
	buf := make([]byte, 1) // 1 byte at a time
	for len(got) < len(data) {
		n, err := adp.Read(buf)
		if n > 0 {
			got = append(got, buf[:n]...)
		}
		if err != nil {
			break
		}
	}

	if string(got) != string(data) {
		t.Errorf("got %q, want %q", got, data)
	}
}

// Test 3: Write reaches server as a single binary WS message with the exact bytes.
func TestWrite_SingleMessageDelivery(t *testing.T) {
	payload := []byte{0x57, 0x41, 0x06, 0x03, 0xDE, 0xAD, 0xBE, 0xEF}
	received := make(chan []byte, 1)

	srv := startEchoServer(t, func(ctx context.Context, c *websocket.Conn) {
		typ, data, err := c.Read(ctx)
		if err != nil {
			t.Logf("server read: %v", err)
			return
		}
		if typ != websocket.MessageBinary {
			t.Errorf("server got type %v, want Binary", typ)
		}
		received <- data
	})
	defer srv.Close()

	adp := dialTest(t, srv)
	defer adp.Close()

	n, err := adp.Write(payload)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != len(payload) {
		t.Fatalf("Write returned n=%d, want %d", n, len(payload))
	}

	got := <-received
	if string(got) != string(payload) {
		t.Errorf("server got %q, want %q", got, payload)
	}
}

// Test 4: Close — subsequent Read returns an error (not a panic).
func TestClose_ReadAfterCloseReturnsError(t *testing.T) {
	srv := startEchoServer(t, func(ctx context.Context, c *websocket.Conn) {
		// Just wait; never send anything.
		<-ctx.Done()
	})
	defer srv.Close()

	adp := dialTest(t, srv)

	if err := adp.Close(); err != nil {
		// CloseNow may return errors; that's acceptable.
		t.Logf("Close returned (non-fatal): %v", err)
	}

	// Read after close must not panic and must return an error.
	buf := make([]byte, 4)
	_, err := adp.Read(buf)
	if err == nil {
		t.Error("Read after Close: expected error, got nil")
	}
}

// Test 5: multiple WS messages, each containing one "frame" — adapter
// concatenates across message boundaries as a transparent byte stream.
func TestRead_MultipleMessages(t *testing.T) {
	msg1 := []byte{0x00, 0x00, 0x03, 'X', 'Y', 'Z'}
	msg2 := []byte{0x00, 0x00, 0x01, 'W'}
	combined := append(append([]byte{}, msg1...), msg2...)

	srv := startEchoServer(t, func(ctx context.Context, c *websocket.Conn) {
		_ = c.Write(ctx, websocket.MessageBinary, msg1)
		_ = c.Write(ctx, websocket.MessageBinary, msg2)
		<-ctx.Done()
	})
	defer srv.Close()

	adp := dialTest(t, srv)
	defer adp.Close()

	got := make([]byte, len(combined))
	_, err := io.ReadFull(adp, got)
	if err != nil {
		t.Fatalf("ReadFull: %v", err)
	}

	if string(got) != string(combined) {
		t.Errorf("got %q, want %q", got, combined)
	}
}
