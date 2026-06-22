package wire

import "testing"

// TestSingleByteTokensSize verifies that the singleByteTokens table has the
// expected number of entries and that specific indices match the constants.js.
//
// From constants.js (0-indexed):
//   index 0  = ""
//   index 1  = "xmlstreamstart"
//   index 2  = "xmlstreamend"
//   index 3  = "s.whatsapp.net"
//   index 4  = "type"
//   index 17 = "to"
//   index 25 = "iq"
func TestSingleByteTokensSize(t *testing.T) {
	// Count from the constants.js file: 236 entries (indices 0..235).
	const wantLen = 236
	if got := len(singleByteTokens); got != wantLen {
		t.Fatalf("singleByteTokens length = %d, want %d", got, wantLen)
	}
	checks := []struct {
		idx  int
		want string
	}{
		{0, ""},
		{1, "xmlstreamstart"},
		{2, "xmlstreamend"},
		{3, "s.whatsapp.net"},
		{4, "type"},
		{17, "to"},
		{25, "iq"},
	}
	for _, c := range checks {
		if got := singleByteTokens[c.idx]; got != c.want {
			t.Errorf("singleByteTokens[%d] = %q, want %q", c.idx, got, c.want)
		}
	}
}

// TestTokenIndex verifies tokenIndex for known tokens.
func TestTokenIndex(t *testing.T) {
	cases := []struct {
		token string
		idx   int
	}{
		{"iq", 25},
		{"to", 17},
		{"type", 4},
		{"s.whatsapp.net", 3},
	}
	for _, c := range cases {
		idx, ok := tokenIndex(c.token)
		if !ok {
			t.Errorf("tokenIndex(%q): not found, want idx=%d", c.token, c.idx)
			continue
		}
		if idx != c.idx {
			t.Errorf("tokenIndex(%q) = %d, want %d", c.token, idx, c.idx)
		}
	}
}

// TestTokenIndexNotFound verifies that tokenIndex returns ok=false for unknown strings.
func TestTokenIndexNotFound(t *testing.T) {
	_, ok := tokenIndex("string-que-nao-existe")
	if ok {
		t.Fatal("tokenIndex(unknown): expected ok=false, got true")
	}
}

// TestDoubleTokenIndex verifies that doubleTokenIndex finds known entries from
// the DOUBLE_BYTE_TOKENS in constants.js.
//
// DICTIONARY_0 (dict=0) first entry = "read-self"    → idx 0
// DICTIONARY_0 (dict=0) entry idx 1 = "active"       → idx 1
// DICTIONARY_1 (dict=1) first entry = "reject"       → idx 0
// DICTIONARY_2 (dict=2) first entry = "64"           → idx 0
// DICTIONARY_3 (dict=3) first entry = "1724"         → idx 0
func TestDoubleTokenIndex(t *testing.T) {
	cases := []struct {
		token string
		dict  int
		idx   int
	}{
		{"read-self", 0, 0},
		{"active", 0, 1},
		{"reject", 1, 0},
		{"64", 2, 0},
		{"1724", 3, 0},
	}
	for _, c := range cases {
		dict, idx, ok := doubleTokenIndex(c.token)
		if !ok {
			t.Errorf("doubleTokenIndex(%q): not found", c.token)
			continue
		}
		if dict != c.dict || idx != c.idx {
			t.Errorf("doubleTokenIndex(%q) = (%d,%d), want (%d,%d)",
				c.token, dict, idx, c.dict, c.idx)
		}
	}
	// Not found case.
	_, _, ok := doubleTokenIndex("string-que-nao-existe")
	if ok {
		t.Fatal("doubleTokenIndex(unknown): expected ok=false, got true")
	}
}

// TestControlConstants verifies that the tag constants match the values in
// constants.js TAGS object.
func TestControlConstants(t *testing.T) {
	checks := []struct {
		name string
		got  byte
		want byte
	}{
		{"LIST_EMPTY", LIST_EMPTY, 0},
		{"DICTIONARY_0", DICTIONARY_0, 236},
		{"DICTIONARY_1", DICTIONARY_1, 237},
		{"DICTIONARY_2", DICTIONARY_2, 238},
		{"DICTIONARY_3", DICTIONARY_3, 239},
		{"INTEROP_JID", INTEROP_JID, 245},
		{"FB_JID", FB_JID, 246},
		{"AD_JID", AD_JID, 247},
		{"LIST_8", LIST_8, 248},
		{"LIST_16", LIST_16, 249},
		{"JID_PAIR", JID_PAIR, 250},
		{"HEX_8", HEX_8, 251},
		{"BINARY_8", BINARY_8, 252},
		{"BINARY_20", BINARY_20, 253},
		{"BINARY_32", BINARY_32, 254},
		{"NIBBLE_8", NIBBLE_8, 255},
		{"PACKED_MAX", PACKED_MAX, 127},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("const %s = %d, want %d", c.name, c.got, c.want)
		}
	}
	// STREAM_END is not in TAGS; ensure it still compiles (value checked separately).
	_ = STREAM_END
}
