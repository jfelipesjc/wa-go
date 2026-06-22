// Package ws provides the WhatsApp WebSocket transport adapter.
//
// It dials wss://web.whatsapp.com/ws/chat (per Baileys Defaults/index.js)
// with the required Origin header and adapts the WebSocket message stream to
// the io.ReadWriteCloser that wire.Conn consumes.
//
// Transport contract:
//
//   - wire.Conn sends the 4-byte WA routing header (0x57 0x41 0x06 0x03)
//     followed by length-prefixed frames via Write.  Each Write call is one
//     atomic message (the header and each frame are separate Write calls).
//   - wire.Conn reads a stream of bytes via Read.  Messages from the server
//     arrive as WebSocket binary messages and may contain one or more
//     length-prefixed frames concatenated, or a single message may be split
//     across multiple Read calls (small buffer).
//
// The adapter therefore:
//
//   - Write(p): sends p as a single WebSocket binary message.
//   - Read(p): drains an internal byte-buffer; when empty, reads the next WS
//     message and appends it to the buffer, then serves p.
package ws

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/coder/websocket"
)

const (
	// waWSURL is the WhatsApp WebSocket endpoint (from Baileys DEFAULT_CONNECTION_CONFIG).
	waWSURL = "wss://web.whatsapp.com/ws/chat"

	// waOrigin is the required Origin header (from Baileys DEFAULT_ORIGIN).
	waOrigin = "https://web.whatsapp.com"
)

// Dial opens a WebSocket connection to the WhatsApp server and returns an
// io.ReadWriteCloser that wire.Conn can use directly.
//
// The returned ReadWriteCloser must be closed by the caller when done.
func Dial(ctx context.Context) (io.ReadWriteCloser, error) {
	hdrs := http.Header{}
	hdrs.Set("Origin", waOrigin)

	c, _, err := websocket.Dial(ctx, waWSURL, &websocket.DialOptions{
		HTTPHeader: hdrs,
	})
	if err != nil {
		return nil, fmt.Errorf("ws: dial: %w", err)
	}

	// Disable the read-size limit; WA messages can be large.
	c.SetReadLimit(-1)

	return newAdapter(ctx, c), nil
}

// adapter wraps a *websocket.Conn and implements io.ReadWriteCloser.
//
// Read semantics: callers (wire.Conn) may call Read with any buffer size.
// WebSocket messages are binary blobs; we buffer them internally so that
// multiple small Read calls are served from a single WS message, and a
// single Read call that spans multiple WS messages works too.
//
// Write semantics: each Write call becomes one WebSocket binary message.
// wire.Conn calls Write once per logical unit (routing header, or one frame),
// so each such unit arrives at the server as its own WS message, which is
// what Baileys/WA expects.
type adapter struct {
	conn *websocket.Conn

	// ctx bounds all WebSocket operations.  Cancelled by Close.
	ctx    context.Context
	cancel context.CancelFunc

	mu  sync.Mutex // protects buf
	buf bytes.Buffer
}

func newAdapter(ctx context.Context, c *websocket.Conn) *adapter {
	derived, cancel := context.WithCancel(ctx)
	return &adapter{
		conn:   c,
		ctx:    derived,
		cancel: cancel,
	}
}

// Read fills p from the internal buffer, fetching new WS messages as needed.
func (a *adapter) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	// Drain the buffer first.
	if a.buf.Len() > 0 {
		return a.buf.Read(p)
	}

	// Buffer empty — read the next WebSocket message.
	typ, data, err := a.conn.Read(a.ctx)
	if err != nil {
		return 0, err
	}
	if typ != websocket.MessageBinary {
		return 0, fmt.Errorf("ws: unexpected message type %v (want Binary)", typ)
	}

	// Append message bytes to buffer, then serve p.
	a.buf.Write(data)
	return a.buf.Read(p)
}

// Write sends p as a single WebSocket binary message.
func (a *adapter) Write(p []byte) (int, error) {
	err := a.conn.Write(a.ctx, websocket.MessageBinary, p)
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

// Close cancels in-flight reads/writes and closes the WebSocket connection.
func (a *adapter) Close() error {
	a.cancel()
	// CloseNow does not wait for a close handshake, which is fine here;
	// WhatsApp does not follow the normal WS close handshake.
	return a.conn.CloseNow()
}
