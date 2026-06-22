package signal

import (
	"bytes"
	"testing"
)

// TestChainAndMessageKeysDeterministic verifies the chain/message KDFs are
// deterministic and structurally correct (lengths, distinctness). The end-to-end
// correctness against libsignal is proven by the decrypt/encrypt golden tests;
// this guards the primitives in isolation.
func TestChainAndMessageKeysDeterministic(t *testing.T) {
	var ck [32]byte
	for i := range ck {
		ck[i] = byte(i)
	}

	next1 := chainKeyNext(ck)
	next2 := chainKeyNext(ck)
	if next1 != next2 {
		t.Fatal("chainKeyNext not deterministic")
	}
	if next1 == ck {
		t.Fatal("chainKeyNext returned same key")
	}

	mk := deriveMessageKeys(ck, 0)
	mk2 := deriveMessageKeys(ck, 0)
	if mk.cipherKey != mk2.cipherKey || mk.macKey != mk2.macKey || mk.iv != mk2.iv {
		t.Fatal("deriveMessageKeys not deterministic")
	}
	if bytes.Equal(mk.cipherKey[:], mk.macKey[:]) {
		t.Fatal("cipherKey == macKey (KDF split broken)")
	}
}

// TestPKCS7RoundTrip exercises the AES-CBC PKCS7 helpers.
func TestPKCS7RoundTrip(t *testing.T) {
	for _, n := range []int{0, 1, 9, 15, 16, 17, 32} {
		data := bytes.Repeat([]byte{0xAB}, n)
		padded := pkcs7Pad(data, 16)
		if len(padded)%16 != 0 {
			t.Fatalf("pad %d not block-aligned", n)
		}
		got, err := pkcs7Unpad(padded, 16)
		if err != nil {
			t.Fatalf("unpad %d: %v", n, err)
		}
		if !bytes.Equal(got, data) {
			t.Fatalf("round trip %d mismatch", n)
		}
	}
}
