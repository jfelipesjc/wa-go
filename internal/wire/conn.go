package wire

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"
)

// Conn wraps an io.ReadWriteCloser with the WhatsApp Noise transport cipher and
// binary-node framing. It is transport-agnostic: callers inject any
// io.ReadWriteCloser (WebSocket, TCP, in-memory pipe, …).
//
// Life cycle:
//
//  1. Create with NewConn.
//  2. Call Handshake to run the Noise XX client handshake and derive transport
//     keys. After Handshake returns, SendNode/ReadNode are available.
//  3. Use SendNode / ReadNode for application-layer traffic.
//  4. Call Close when done.
type Conn struct {
	rw    io.ReadWriteCloser
	noise *Noise // nil until Handshake completes
}

// NewConn constructs a Conn around the given transport. The transport is
// responsible for the raw byte stream (no framing or encryption yet).
func NewConn(rw io.ReadWriteCloser) *Conn {
	return &Conn{rw: rw}
}

// Close closes the underlying transport.
func (c *Conn) Close() error {
	return c.rw.Close()
}

// Handshake runs the Noise_XX_25519_AESGCM_SHA256 client handshake.
//
// Parameters mirror ClientHandshake:
//   - ephemeralPriv/ephemeralPub: client ephemeral key pair (generated fresh per
//     connection, or injected for deterministic replay in tests).
//   - staticPriv/staticPub: client long-term static (auth/noise) key pair.
//   - clientPayload: the plaintext ClientPayload protobuf to send in ClientFinish.
//
// Handshake writes the WA routing header + ClientHello frame, reads the
// ServerHello frame, writes the ClientFinish frame, and leaves the Conn ready
// for SendNode / ReadNode calls.
func (c *Conn) Handshake(
	ephemeralPriv, ephemeralPub,
	staticPriv, staticPub,
	clientPayload []byte,
) (*HandshakeResult, error) {
	// 1. Write the WA routing header once (not framed).
	if _, err := c.rw.Write(noiseWAHeader); err != nil {
		return nil, fmt.Errorf("conn: write WA header: %w", err)
	}

	// 2. Build handshake state (mixes in WA header + ephemeral pub).
	n := newNoise(ephemeralPub)
	c.noise = n

	// 3. Build ClientHello and send it as a framed payload.
	clientHello := encodeClientHello(ephemeralPub)
	if err := writeFrame(c.rw, clientHello); err != nil {
		return nil, fmt.Errorf("conn: write ClientHello: %w", err)
	}

	// 4. Read the ServerHello frame.
	serverHelloFrame, err := readFrame(c.rw)
	if err != nil {
		return nil, fmt.Errorf("conn: read ServerHello: %w", err)
	}

	// 5. Run the handshake (processes ServerHello, emits ClientFinish).
	res, err := n.ClientHandshake(
		ephemeralPriv, ephemeralPub,
		staticPriv, staticPub,
		serverHelloFrame, clientPayload,
	)
	if err != nil {
		return nil, fmt.Errorf("conn: ClientHandshake: %w", err)
	}

	// 6. Send ClientFinish as a framed payload.
	if err := writeFrame(c.rw, res.ClientFinish); err != nil {
		return nil, fmt.Errorf("conn: write ClientFinish: %w", err)
	}

	return res, nil
}

// SendNode encodes n as a binary node, prefixes it with 0x00 (no compression),
// encrypts it with the transport write key, and writes it as a length-prefixed
// frame.
//
// SendNode must not be called before Handshake completes.
func (c *Conn) SendNode(n Node) error {
	if c.noise == nil || c.noise.transport == nil {
		return fmt.Errorf("conn: SendNode called before Handshake")
	}

	// Encode node to binary.
	nodeBytes, err := EncodeNode(n)
	if err != nil {
		return fmt.Errorf("conn: EncodeNode: %w", err)
	}

	// Prepend compression prefix byte 0x00 (no compression).
	plaintext := make([]byte, 1+len(nodeBytes))
	plaintext[0] = 0x00
	copy(plaintext[1:], nodeBytes)

	// Encrypt with transport write key.
	ciphertext, err := c.noise.transport.encrypt(plaintext)
	if err != nil {
		return fmt.Errorf("conn: transport encrypt: %w", err)
	}

	// Write as a length-prefixed frame.
	if err := writeFrame(c.rw, ciphertext); err != nil {
		return fmt.Errorf("conn: writeFrame: %w", err)
	}
	return nil
}

// ReadNode reads one length-prefixed frame, decrypts it with the transport read
// key, handles the compression prefix byte (0x00 = raw, 0x02 = zlib), and
// decodes the resulting bytes as a binary node.
//
// ReadNode must not be called before Handshake completes.
func (c *Conn) ReadNode() (Node, error) {
	if c.noise == nil || c.noise.transport == nil {
		return Node{}, fmt.Errorf("conn: ReadNode called before Handshake")
	}

	// Read one frame.
	ciphertext, err := readFrame(c.rw)
	if err != nil {
		return Node{}, fmt.Errorf("conn: readFrame: %w", err)
	}

	// Decrypt.
	plaintext, err := c.noise.transport.decrypt(ciphertext)
	if err != nil {
		return Node{}, fmt.Errorf("conn: transport decrypt: %w", err)
	}

	if len(plaintext) == 0 {
		return Node{}, fmt.Errorf("conn: empty plaintext after decrypt")
	}

	// Handle compression prefix.
	compByte := plaintext[0]
	rest := plaintext[1:]

	var nodeBytes []byte
	switch compByte {
	case 0x00:
		nodeBytes = rest
	case 0x02:
		nodeBytes, err = zlibInflate(rest)
		if err != nil {
			return Node{}, fmt.Errorf("conn: zlib inflate: %w", err)
		}
	default:
		return Node{}, fmt.Errorf("conn: unknown compression prefix 0x%02x", compByte)
	}

	node, err := DecodeNode(nodeBytes)
	if err != nil {
		return Node{}, fmt.Errorf("conn: DecodeNode: %w", err)
	}
	return node, nil
}

// zlibDeflate compresses data with zlib (for completeness; not used in SendNode
// which always sends uncompressed).
func zlibDeflate(b []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	if _, err := w.Write(b); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
