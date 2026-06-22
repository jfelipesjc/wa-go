package wire

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"testing"
)

// ──────────────────────────────────────────────────────────────────────────────
// loopbackRW — in-memory io.ReadWriteCloser for Conn tests.
//
// Reads come from a pre-built sequence of raw frames (hex-encoded bytes that
// include the 3-byte length prefix, exactly as they arrive on the wire).
// Writes are captured in writeBuf for inspection.
// ──────────────────────────────────────────────────────────────────────────────

type loopbackRW struct {
	r       io.Reader
	writeBuf *bytes.Buffer
	closed  bool
}

// newLoopbackRW constructs a loopbackRW that will serve rawFrames (already
// hex-decoded, including the 3-byte length prefix) as the read side.
func newLoopbackRW(rawFrames [][]byte) *loopbackRW {
	var buf bytes.Buffer
	for _, f := range rawFrames {
		buf.Write(f)
	}
	return &loopbackRW{r: &buf, writeBuf: &bytes.Buffer{}}
}

func (l *loopbackRW) Read(p []byte) (int, error) {
	return l.r.Read(p)
}

func (l *loopbackRW) Write(p []byte) (int, error) {
	return l.writeBuf.Write(p)
}

func (l *loopbackRW) Close() error {
	l.closed = true
	return nil
}

// ──────────────────────────────────────────────────────────────────────────────
// traceFrames returns the parsed frames from frames_raw.jsonl, loaded via the
// shared loadFramesRaw helper (already defined in noise_test.go).
//
// Index: 0=out(ClientHello) 1=in(ServerHello) 2=out(ClientFinish)
//        3=in(pair-device)  4=out(app-node)
// ──────────────────────────────────────────────────────────────────────────────

func loadFramesRawForConn(t *testing.T) []frameLine {
	t.Helper()
	fh, err := os.Open("../../testdata/traces/connect_pair/frames_raw.jsonl")
	if err != nil {
		t.Fatalf("open frames_raw: %v", err)
	}
	defer fh.Close()
	var out []frameLine
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var fl frameLine
		if err := json.Unmarshal(line, &fl); err != nil {
			t.Fatalf("unmarshal frame: %v", err)
		}
		out = append(out, fl)
	}
	return out
}

// ──────────────────────────────────────────────────────────────────────────────
// Test 1: Handshake + ReadNode (pair-device) via in-memory replay.
//
// The loopbackRW serves the inbound bytes that the server would send:
//   - The ServerHello frame (frames[1]) — consumed by Handshake.
//   - The pair-device encrypted frame (frames[3]) — consumed by ReadNode.
//
// The outbound bytes (ClientHello, ClientFinish) are discarded into writeBuf.
// ──────────────────────────────────────────────────────────────────────────────

func TestConnReadNodePairDevice(t *testing.T) {
	f := loadNoiseFixture(t)
	ephPriv := mustHex(t, f.EphemeralKeyPriv)
	ephPub := mustHex(t, f.EphemeralKeyPub)
	staticPriv := mustHex(t, f.AuthStaticKeyPriv)
	staticPub := mustHex(t, f.AuthStaticKeyPub)

	frames := loadFramesRawForConn(t)

	// frames[1] = ServerHello, frames[3] = pair-device
	if len(frames) < 4 {
		t.Fatalf("expected ≥4 frames, got %d", len(frames))
	}
	serverHelloRaw := mustHex(t, frames[1].Hex) // raw bytes WITH 3-byte length prefix
	pairDeviceRaw := mustHex(t, frames[3].Hex)  // encrypted pair-device WITH prefix

	// The loopback serves these two inbound frames in sequence.
	rw := newLoopbackRW([][]byte{serverHelloRaw, pairDeviceRaw})

	conn := NewConn(rw)

	// clientPayload: use a dummy byte (same as noise_test.go does for the
	// ClientHandshake test — we only care about the transport keys).
	_, err := conn.Handshake(ephPriv, ephPub, staticPriv, staticPub, []byte("x"))
	if err != nil {
		t.Fatalf("Handshake: %v", err)
	}

	// ReadNode should decode the pair-device node correctly.
	got, err := conn.ReadNode()
	if err != nil {
		t.Fatalf("ReadNode: %v", err)
	}

	if got.Tag != "iq" {
		t.Fatalf("top tag: got %q want \"iq\"", got.Tag)
	}

	// Compare against the captured 'in' node from nodes.jsonl.
	want := loadInNode(t)
	if msg := nodeEqual(got, want); msg != "" {
		t.Fatalf("pair-device node mismatch: %s", msg)
	}

	t.Logf("ReadNode: tag=%q attrs=%v OK", got.Tag, got.Attrs)
}

// ──────────────────────────────────────────────────────────────────────────────
// Test 2: SendNode / ReadNode round-trip with a symmetric in-memory pair.
//
// We manually derive transport keys from ClientHandshake and create two Conn
// objects with swapped keys (write on side A = read on side B), connected via
// an io.Pipe. Then we send a node on side A and read it on side B.
// ──────────────────────────────────────────────────────────────────────────────

// pipeRW wraps an io.PipeReader+PipeWriter as an io.ReadWriteCloser.
type pipeRW struct {
	r *io.PipeReader
	w *io.PipeWriter
}

func (p *pipeRW) Read(buf []byte) (int, error)  { return p.r.Read(buf) }
func (p *pipeRW) Write(buf []byte) (int, error) { return p.w.Write(buf) }
func (p *pipeRW) Close() error {
	p.w.Close()
	return p.r.Close()
}

// newConnWithTransport builds a Conn whose noise.transport is pre-set with the
// given encKey and decKey, bypassing the actual handshake. This lets us test
// SendNode/ReadNode in isolation with known keys.
func newConnWithTransport(rw io.ReadWriteCloser, encKey, decKey []byte) *Conn {
	n := &Noise{}
	n.transport = &transportState{
		encKey: encKey,
		decKey: decKey,
	}
	return &Conn{rw: rw, noise: n}
}

func TestConnSendNodeReadNodeRoundTrip(t *testing.T) {
	f := loadNoiseFixture(t)
	ephPriv := mustHex(t, f.EphemeralKeyPriv)
	ephPub := mustHex(t, f.EphemeralKeyPub)
	staticPriv := mustHex(t, f.AuthStaticKeyPriv)
	staticPub := mustHex(t, f.AuthStaticKeyPub)
	serverHelloMsg := mustHex(t, f.ServerHelloFrameHex)[3:]

	// Derive transport keys from the real handshake.
	n := newNoise(ephPub)
	res, err := n.ClientHandshake(ephPriv, ephPub, staticPriv, staticPub, serverHelloMsg, []byte("x"))
	if err != nil {
		t.Fatalf("ClientHandshake: %v", err)
	}
	writeKey := res.WriteKey
	readKey := res.ReadKey

	// Connect side A (writer) and side B (reader) via an io.Pipe.
	// Side A encKey=writeKey, decKey=readKey  (client perspective)
	// Side B encKey=readKey,  decKey=writeKey (server perspective, swapped)
	aRead, aWrite := io.Pipe()
	bRead, bWrite := io.Pipe()

	// sideA: reads from aRead, writes to bWrite
	sideA := &pipeRW{r: aRead, w: bWrite}
	// sideB: reads from bRead, writes to aWrite
	sideB := &pipeRW{r: bRead, w: aWrite}

	connA := newConnWithTransport(sideA, writeKey, readKey)
	connB := newConnWithTransport(sideB, readKey, writeKey)

	// Build a test node.
	want := Node{
		Tag: "iq",
		Attrs: map[string]string{
			"id":   "test-123",
			"type": "get",
			"to":   "s.whatsapp.net",
		},
		Content: []Node{
			{Tag: "pair-device", Attrs: map[string]string{}, Content: nil},
		},
	}

	// Send from A in a goroutine (SendNode blocks until Write completes).
	errCh := make(chan error, 1)
	go func() {
		errCh <- connA.SendNode(want)
	}()

	// Read on B.
	got, err := connB.ReadNode()
	if err != nil {
		t.Fatalf("ReadNode: %v", err)
	}

	if sendErr := <-errCh; sendErr != nil {
		t.Fatalf("SendNode: %v", sendErr)
	}

	if msg := nodeEqual(got, want); msg != "" {
		t.Fatalf("round-trip mismatch: %s", msg)
	}

	t.Logf("round-trip OK: tag=%q attrs=%v", got.Tag, got.Attrs)

	// Clean up pipes.
	aRead.Close()
	aWrite.Close()
	bRead.Close()
	bWrite.Close()
}

// ──────────────────────────────────────────────────────────────────────────────
// Test 3: SendNode before Handshake returns an error (guard).
// ──────────────────────────────────────────────────────────────────────────────

func TestConnSendNodeBeforeHandshakeErrors(t *testing.T) {
	rw := newLoopbackRW(nil)
	conn := NewConn(rw)
	err := conn.SendNode(Node{Tag: "test"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	t.Logf("got expected error: %v", err)
}

// mustHexForConn is the same as mustHex but doesn't need testing.T from
// noise_test.go — we reuse mustHex from the same package (defined in noise_test.go).
// No additional helper needed; the package test binary includes all _test.go files.

// Verify that written bytes (WA header + ClientHello frame + ClientFinish frame)
// include the WA routing header at the front.
func TestConnHandshakeWritesWAHeader(t *testing.T) {
	f := loadNoiseFixture(t)
	ephPriv := mustHex(t, f.EphemeralKeyPriv)
	ephPub := mustHex(t, f.EphemeralKeyPub)
	staticPriv := mustHex(t, f.AuthStaticKeyPriv)
	staticPub := mustHex(t, f.AuthStaticKeyPub)

	frames := loadFramesRawForConn(t)
	serverHelloRaw := mustHex(t, frames[1].Hex)
	pairDeviceRaw := mustHex(t, frames[3].Hex)

	rw := newLoopbackRW([][]byte{serverHelloRaw, pairDeviceRaw})
	conn := NewConn(rw)

	_, err := conn.Handshake(ephPriv, ephPub, staticPriv, staticPub, []byte("x"))
	if err != nil {
		t.Fatalf("Handshake: %v", err)
	}

	written := rw.writeBuf.Bytes()
	if len(written) < 4 {
		t.Fatalf("written too short: %d bytes", len(written))
	}

	// First 4 bytes must be the WA routing header.
	if !bytes.Equal(written[:4], noiseWAHeader) {
		t.Fatalf("WA header mismatch:\n got=%x\nwant=%x", written[:4], noiseWAHeader)
	}

	// The full output should match: WA header + clientHelloFrame + clientFinishFrame.
	wantClientHelloFrame := mustHex(t, f.ClientHelloFrameHex)[4:] // strip WA header prefix
	wantClientHelloPayload := wantClientHelloFrame[3:]             // strip 3-byte length prefix

	// Reconstruct what clientHello frame bytes look like (WA header + framed hello).
	var wantBuf bytes.Buffer
	wantBuf.Write(noiseWAHeader)
	if err := writeFrame(&wantBuf, wantClientHelloPayload); err != nil {
		t.Fatalf("writeFrame: %v", err)
	}
	// ClientFinish: not checking exact bytes (payload is dummy "x", differs from trace),
	// but it should be present.
	if len(written) <= wantBuf.Len() {
		t.Fatalf("expected ClientFinish bytes after ClientHello, got written=%d wantBuf=%d",
			len(written), wantBuf.Len())
	}

	// Verify the ClientHello portion matches byte-for-byte.
	if !bytes.Equal(written[:wantBuf.Len()], wantBuf.Bytes()) {
		t.Fatalf("WA header + ClientHello frame mismatch:\n got=%x\nwant=%x",
			written[:wantBuf.Len()], wantBuf.Bytes())
	}
	t.Logf("WA header + ClientHello frame: OK (%d bytes written total)", len(written))
}

// Ensure loadInNode resolves to the pair-device 'in' node (same helper used in
// noise_test.go, shared within the package test binary).
func TestConnLoadInNodeSanity(t *testing.T) {
	node := loadInNode(t)
	if node.Tag != "iq" {
		t.Fatalf("expected tag iq, got %q", node.Tag)
	}
	t.Logf("in-node tag=%q attrs=%v", node.Tag, node.Attrs)
}

// Helper: decode a hex string for use without testing.T.
func hexMust(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic("hexMust: " + err.Error())
	}
	return b
}
