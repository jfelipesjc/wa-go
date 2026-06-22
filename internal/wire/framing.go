package wire

import (
	"encoding/binary"
	"fmt"
	"io"
)

// maxFramePayload is the maximum payload size supported by the 3-byte
// big-endian length prefix (2^24 - 1 = 16 777 215 bytes).
const maxFramePayload = (1 << 24) - 1

// writeFrame writes a length-prefixed frame to w.
// The format is: 3 bytes big-endian length followed by payload bytes.
// Returns an error if len(payload) > maxFramePayload.
func writeFrame(w io.Writer, payload []byte) error {
	if len(payload) > maxFramePayload {
		return fmt.Errorf("wire: frame payload too large: %d > %d", len(payload), maxFramePayload)
	}
	var hdr [3]byte
	// Encode length as 3 bytes big-endian.
	n := uint32(len(payload))
	hdr[0] = byte(n >> 16)
	hdr[1] = byte(n >> 8)
	hdr[2] = byte(n)
	if _, err := w.Write(hdr[:]); err != nil {
		return fmt.Errorf("wire: writeFrame header: %w", err)
	}
	if len(payload) > 0 {
		if _, err := w.Write(payload); err != nil {
			return fmt.Errorf("wire: writeFrame payload: %w", err)
		}
	}
	return nil
}

// readFrame reads one length-prefixed frame from r.
// Returns io.EOF if the reader is empty before any bytes are read.
// Returns io.ErrUnexpectedEOF if the stream ends mid-frame.
func readFrame(r io.Reader) ([]byte, error) {
	var hdr [3]byte
	_, err := io.ReadFull(r, hdr[:])
	if err != nil {
		// io.ReadFull returns io.ErrUnexpectedEOF if it got 1 or 2 bytes.
		// Convert a clean zero-byte EOF into plain io.EOF for callers.
		if err == io.ErrUnexpectedEOF {
			// Could be partial header; keep as-is for the truncated-frame case.
			// But if we read 0 bytes and got EOF, ReadFull returns io.EOF directly.
		}
		return nil, err
	}
	n := int(binary.BigEndian.Uint32([]byte{0, hdr[0], hdr[1], hdr[2]}))
	if n == 0 {
		return []byte{}, nil
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}
