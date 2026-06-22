package wire

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"testing"
)

// ──────────────────────────────────────────────────────────────────────────────
// JSON fixtures helpers
// ──────────────────────────────────────────────────────────────────────────────

// jsonNode is the JSON representation of a Node used in the fixtures.
// Content can be: null, string, or []jsonNode.
type jsonNode struct {
	Tag     string            `json:"tag"`
	Attrs   map[string]string `json:"attrs"`
	Content json.RawMessage   `json:"content"`
}

// batteryLine is one entry from codec_battery/nodes.jsonl.
type batteryLine struct {
	Name        string   `json:"name"`
	Tree        jsonNode `json:"tree"`
	DecodedTree jsonNode `json:"decoded_tree"`
	EncodedHex  string   `json:"encoded_hex"`
}

// connectPairLine is one entry from connect_pair/nodes.jsonl.
type connectPairLine struct {
	Dir        string   `json:"dir"`
	T          int      `json:"t"`
	Tree       jsonNode `json:"tree"`
	EncodedHex string   `json:"encoded_hex"`
}

// ──────────────────────────────────────────────────────────────────────────────
// Node comparison helpers
// ──────────────────────────────────────────────────────────────────────────────

// contentFromJSON converts a json.RawMessage content field into the Go type
// that DecodeNode would produce:
//
//   - JSON null               → nil
//   - JSON []                 → []Node (decoded recursively)
//   - JSON "" (empty string)  → nil  (empty BINARY_8 buffer, same as []byte{})
//   - JSON string (all hex)   → []byte decoded from hex
//   - JSON string (other)     → string (JID, token, etc.)
//
// The "decoded_tree" field in codec_battery uses hex strings for binary buffers
// (JS Buffer.toString('hex')). We must treat those as []byte.
func contentFromJSON(raw json.RawMessage) (any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}

	// Try array first.
	if raw[0] == '[' {
		var arr []jsonNode
		if err := json.Unmarshal(raw, &arr); err != nil {
			return nil, fmt.Errorf("content array: %w", err)
		}
		nodes := make([]Node, len(arr))
		for i, j := range arr {
			n, err := nodeFromJSON(j)
			if err != nil {
				return nil, fmt.Errorf("child[%d]: %w", i, err)
			}
			nodes[i] = n
		}
		return nodes, nil
	}

	// String.
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("content string: %w", err)
	}

	// Empty string: produced by Buffer(0).toString('hex').
	// This represents a 0-byte binary payload (BINARY_8 with length 0).
	// We return []byte{} so that EncodeNode re-emits BINARY_8 + 0 bytes.
	// The nodeEqual helper normalises both nil and []byte{} to nil for
	// decode-side comparisons, so decode tests still pass.
	if s == "" {
		return []byte{}, nil
	}

	// If the string is a valid even-length hex string, treat as []byte.
	if len(s)%2 == 0 && isAllHex(s) {
		b, err := hex.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("content hex decode: %w", err)
		}
		return b, nil
	}

	return s, nil
}

func isAllHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func nodeFromJSON(j jsonNode) (Node, error) {
	content, err := contentFromJSON(j.Content)
	if err != nil {
		return Node{}, err
	}
	attrs := j.Attrs
	if attrs == nil {
		attrs = map[string]string{}
	}
	return Node{Tag: j.Tag, Attrs: attrs, Content: content}, nil
}

// nodeEqual recursively compares two Nodes, returning "" on equality or a
// human-readable diff message on mismatch.
func nodeEqual(got, want Node) string {
	if got.Tag != want.Tag {
		return fmt.Sprintf("tag: got %q want %q", got.Tag, want.Tag)
	}
	// Compare attrs.
	for k, wv := range want.Attrs {
		gv, ok := got.Attrs[k]
		if !ok {
			return fmt.Sprintf("attrs: missing key %q", k)
		}
		if gv != wv {
			return fmt.Sprintf("attrs[%q]: got %q want %q", k, gv, wv)
		}
	}
	for k := range got.Attrs {
		if _, ok := want.Attrs[k]; !ok {
			return fmt.Sprintf("attrs: unexpected key %q = %q", k, got.Attrs[k])
		}
	}
	// Compare content.
	return contentEqual(got.Content, want.Content)
}

func contentEqual(got, want any) string {
	// Normalise nil / []byte{} / empty-string to nil for comparison.
	got = normaliseEmpty(got)
	want = normaliseEmpty(want)

	if got == nil && want == nil {
		return ""
	}
	if (got == nil) != (want == nil) {
		return fmt.Sprintf("content nil mismatch: got %v want %v", got == nil, want == nil)
	}

	switch wv := want.(type) {
	case string:
		gv, ok := got.(string)
		if !ok {
			return fmt.Sprintf("content type: got %T want string", got)
		}
		if gv != wv {
			return fmt.Sprintf("content string: got %q want %q", gv, wv)
		}
	case []byte:
		gv, ok := got.([]byte)
		if !ok {
			// Accept empty string as empty bytes.
			if gs, ok2 := got.(string); ok2 && gs == "" && len(wv) == 0 {
				return ""
			}
			return fmt.Sprintf("content type: got %T want []byte", got)
		}
		if !bytes.Equal(gv, wv) {
			return fmt.Sprintf("content bytes: got %x want %x", gv, wv)
		}
	case []Node:
		gv, ok := got.([]Node)
		if !ok {
			return fmt.Sprintf("content type: got %T want []Node", got)
		}
		if len(gv) != len(wv) {
			return fmt.Sprintf("content children count: got %d want %d", len(gv), len(wv))
		}
		for i := range wv {
			if msg := nodeEqual(gv[i], wv[i]); msg != "" {
				return fmt.Sprintf("children[%d]: %s", i, msg)
			}
		}
	default:
		return fmt.Sprintf("content: unknown want type %T", want)
	}
	return ""
}

// normaliseEmpty converts []byte{} and "" to nil so that empty-binary == nil.
func normaliseEmpty(v any) any {
	switch x := v.(type) {
	case []byte:
		if len(x) == 0 {
			return nil
		}
	case string:
		if x == "" {
			return nil
		}
	}
	return v
}

// ──────────────────────────────────────────────────────────────────────────────
// Task 5 — decode tests
// ──────────────────────────────────────────────────────────────────────────────

// TestDecodeCodecBattery decodes all 19 nodes in codec_battery/nodes.jsonl and
// compares against the decoded_tree field.
//
// Binary content in decoded_tree is hex-encoded (JS Buffer.toString('hex')).
// We decode those to []byte before comparing.
func TestDecodeCodecBattery(t *testing.T) {
	path := "../../testdata/traces/codec_battery/nodes.jsonl"
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()

	var passed, total int
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		total++

		var bl batteryLine
		if err := json.Unmarshal([]byte(line), &bl); err != nil {
			t.Errorf("line %d (%s): json: %v", total, bl.Name, err)
			continue
		}

		// Decode encoded_hex → strip first byte (0x00 prefix) → raw node bytes.
		raw, err := hex.DecodeString(bl.EncodedHex)
		if err != nil {
			t.Errorf("%s: hex decode: %v", bl.Name, err)
			continue
		}
		if len(raw) < 1 {
			t.Errorf("%s: encoded_hex too short", bl.Name)
			continue
		}
		// First byte is 0x00 (no compression) or 0x02 (zlib). Battery nodes
		// are always 0x00 (pre-decompressed), so we simply strip it.
		payload := raw[1:]

		got, err := DecodeNode(payload)
		if err != nil {
			t.Errorf("%s: DecodeNode: %v", bl.Name, err)
			continue
		}

		// Build expected from decoded_tree.
		want, err := nodeFromJSON(bl.DecodedTree)
		if err != nil {
			t.Errorf("%s: nodeFromJSON(decoded_tree): %v", bl.Name, err)
			continue
		}

		if msg := nodeEqual(got, want); msg != "" {
			t.Errorf("%s: %s", bl.Name, msg)
			continue
		}
		passed++
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scanner: %v", err)
	}
	t.Logf("codec_battery: %d/%d nodes passed", passed, total)
	if total == 0 {
		t.Fatal("no nodes found in codec_battery/nodes.jsonl")
	}
	if passed != total {
		t.Fatalf("only %d/%d nodes passed", passed, total)
	}
}

// TestDecodeConnectPair decodes both nodes from connect_pair/nodes.jsonl.
//
// The manifest says:
//   - "out" nodes: encoded_hex is raw binary node bytes (no prefix).
//   - "in" nodes: encoded_hex has a 0x00/0x02 prefix byte.
//     0x00 = no compression (strip 1 byte); 0x02 = zlib inflate the remainder.
//
// Empirically (confirmed during implementation): both nodes have 0x00 first byte.
func TestDecodeConnectPair(t *testing.T) {
	path := "../../testdata/traces/connect_pair/nodes.jsonl"
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()

	var decoded int
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		var cp connectPairLine
		if err := json.Unmarshal([]byte(line), &cp); err != nil {
			t.Fatalf("json: %v", err)
		}

		raw, err := hex.DecodeString(cp.EncodedHex)
		if err != nil {
			t.Fatalf("dir=%s: hex decode: %v", cp.Dir, err)
		}

		var payload []byte
		switch cp.Dir {
		case "out":
			// "out" nodes: encoded_hex is raw bytes (no prefix per manifest).
			// However our battery test shows the out node ALSO has 0x00 first byte.
			// Check empirically and handle both.
			if len(raw) > 0 && (raw[0] == 0x00 || raw[0] == 0x02) {
				payload, err = decompressConnectPairPayload(raw)
				if err != nil {
					t.Fatalf("dir=out: decompress: %v", err)
				}
			} else {
				payload = raw
			}
		case "in":
			// "in" nodes have a prefix byte: 0x00 = raw, 0x02 = zlib.
			if len(raw) < 1 {
				t.Fatalf("dir=in: empty encoded_hex")
			}
			payload, err = decompressConnectPairPayload(raw)
			if err != nil {
				t.Fatalf("dir=in: decompress: %v", err)
			}
		default:
			t.Fatalf("unknown dir: %s", cp.Dir)
		}

		got, err := DecodeNode(payload)
		if err != nil {
			t.Fatalf("dir=%s: DecodeNode: %v", cp.Dir, err)
		}

		want, err := nodeFromJSON(cp.Tree)
		if err != nil {
			t.Fatalf("dir=%s: nodeFromJSON: %v", cp.Dir, err)
		}

		if msg := nodeEqual(got, want); msg != "" {
			t.Errorf("dir=%s: %s", cp.Dir, msg)
		} else {
			decoded++
			t.Logf("dir=%s: tag=%q decoded OK", cp.Dir, got.Tag)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scanner: %v", err)
	}
	t.Logf("connect_pair: decoded %d nodes", decoded)
}

// decompressConnectPairPayload strips the prefix byte and optionally inflates.
// First byte: 0x00 = no compression; 0x02 = zlib compressed.
func decompressConnectPairPayload(b []byte) ([]byte, error) {
	if len(b) < 1 {
		return nil, fmt.Errorf("too short")
	}
	prefix := b[0]
	rest := b[1:]
	if prefix&0x02 != 0 {
		// zlib inflate
		r, err := zlib.NewReader(bytes.NewReader(rest))
		if err != nil {
			return nil, fmt.Errorf("zlib.NewReader: %w", err)
		}
		defer r.Close()
		out, err := io.ReadAll(r)
		if err != nil {
			return nil, fmt.Errorf("zlib inflate: %w", err)
		}
		return out, nil
	}
	return rest, nil
}
