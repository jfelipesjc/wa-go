package wire

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

// TestFrameRoundTrip verifies that writeFrame followed by readFrame returns
// the original payload for various sizes.
func TestFrameRoundTrip(t *testing.T) {
	sizes := []int{0, 1, 255, 256, 1000}
	for _, sz := range sizes {
		payload := make([]byte, sz)
		for i := range payload {
			payload[i] = byte(i & 0xff)
		}
		var buf bytes.Buffer
		if err := writeFrame(&buf, payload); err != nil {
			t.Fatalf("writeFrame(size=%d): %v", sz, err)
		}
		got, err := readFrame(&buf)
		if err != nil {
			t.Fatalf("readFrame(size=%d): %v", sz, err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("size=%d: payload mismatch", sz)
		}
	}
}

// TestFrameLimit verifies that writeFrame rejects payloads larger than 2^24-1.
func TestFrameLimit(t *testing.T) {
	// We don't want to allocate 16MB, so we use a custom Writer that only
	// tracks how many bytes are written.
	big := make([]byte, 1<<24) // exactly 2^24 — must fail
	var buf bytes.Buffer
	if err := writeFrame(&buf, big); err == nil {
		t.Fatal("expected error for payload > 0xFFFFFF, got nil")
	}
}

// TestFrameTruncated verifies that readFrame returns io.ErrUnexpectedEOF when
// the stream is shorter than the announced length.
func TestFrameTruncated(t *testing.T) {
	// Write a valid frame then truncate the payload.
	payload := make([]byte, 100)
	var full bytes.Buffer
	if err := writeFrame(&full, payload); err != nil {
		t.Fatal(err)
	}
	// Keep only the 3-byte header + 50 bytes of payload (50 fewer than declared).
	truncated := full.Bytes()[:3+50]
	_, err := readFrame(bytes.NewReader(truncated))
	if err != io.ErrUnexpectedEOF {
		t.Fatalf("expected io.ErrUnexpectedEOF, got %v", err)
	}
}

// TestFrameEOF verifies that readFrame on an empty reader returns io.EOF.
func TestFrameEOF(t *testing.T) {
	_, err := readFrame(bytes.NewReader(nil))
	if err != io.EOF {
		t.Fatalf("expected io.EOF on empty reader, got %v", err)
	}
}

// traceFrame is one line of testdata/traces/connect_pair/frames_raw.jsonl.
type traceFrame struct {
	Dir string `json:"dir"`
	T   int    `json:"t"`
	Hex string `json:"hex"`
}

// TestFrameFixtureIn validates that readFrame correctly parses every "in"
// frame from the golden trace.
//
// Each "in" frame hex begins with the 3-byte length prefix followed by the
// payload. We confirm that readFrame reads len(hex_bytes)-3 bytes of payload.
//
// For "out" frames: the very first "out" frame starts with the 4-byte routing
// header "57 41 06 03" before the 3-byte length prefix. We SKIP all "out"
// frames here and only test "in" frames, because:
//   - "out" frames mix the routing header with the framing (concern of
//     the Noise/Conn layer, not the generic framing layer).
//   - "in" frames are pure 3-byte-length-prefixed, making them a clean
//     fixture for the framing layer.
func TestFrameFixtureIn(t *testing.T) {
	path := "../../testdata/traces/connect_pair/frames_raw.jsonl"
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("trace file not found (%v); skipping fixture test", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	testedIn := 0
	for i, line := range lines {
		var f traceFrame
		if err := json.Unmarshal([]byte(line), &f); err != nil {
			t.Fatalf("line %d: json parse error: %v", i+1, err)
		}
		if f.Dir != "in" {
			continue
		}
		raw, err := hex.DecodeString(f.Hex)
		if err != nil {
			t.Fatalf("line %d: hex decode: %v", i+1, err)
		}
		if len(raw) < 3 {
			t.Fatalf("line %d: frame too short (%d bytes)", i+1, len(raw))
		}
		// The first 3 bytes are the big-endian length.
		declared := int(raw[0])<<16 | int(raw[1])<<8 | int(raw[2])
		payload := raw[3:]
		if declared != len(payload) {
			t.Errorf("line %d: declared length %d != payload length %d", i+1, declared, len(payload))
		}
		// Confirm readFrame produces the same payload.
		got, err := readFrame(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("line %d: readFrame: %v", i+1, err)
		}
		if !bytes.Equal(got, payload) {
			t.Errorf("line %d: payload mismatch", i+1)
		}
		testedIn++
	}
	if testedIn == 0 {
		t.Fatal("no 'in' frames found in trace — fixture test did nothing")
	}
	t.Logf("validated %d 'in' frames from golden trace", testedIn)
}
