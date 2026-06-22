package media

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// vectorsFile is the golden vector set produced by harness/gen_media_vectors.mjs
// using Baileys' getMediaKeys/hkdf and node crypto.
const vectorsFile = "../../testdata/media/media_vectors.json"

type vectors struct {
	MediaKeyHex string `json:"mediaKeyHex"`
	Plaintexts  map[string]struct {
		Label        string `json:"label"`
		Len          int    `json:"len"`
		PlaintextHex string `json:"plaintextHex"`
	} `json:"plaintexts"`
	Cases []vecCase `json:"cases"`
}

type vecCase struct {
	Plaintext     string `json:"plaintext"`
	MediaType     string `json:"mediaType"`
	IV            string `json:"iv"`
	CipherKey     string `json:"cipherKey"`
	MacKey        string `json:"macKey"`
	RefKey        string `json:"refKey"`
	Info          string `json:"info"`
	CiphertextHex string `json:"ciphertextHex"`
	MacHex        string `json:"macHex"`
	FileSha256    string `json:"fileSha256"`
	FileEncSha256 string `json:"fileEncSha256"`
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex decode %q: %v", s, err)
	}
	return b
}

func loadVectors(t *testing.T) (vectors, [32]byte) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(vectorsFile))
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var v vectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("unmarshal vectors: %v", err)
	}
	if len(v.Cases) == 0 {
		t.Fatal("no cases in vectors")
	}
	var mk [32]byte
	copy(mk[:], mustHex(t, v.MediaKeyHex))
	return v, mk
}

func typeForName(t *testing.T, name string) MediaType {
	t.Helper()
	switch name {
	case "image":
		return Image
	case "video":
		return Video
	case "audio":
		return Audio
	case "document":
		return Document
	default:
		t.Fatalf("unknown media type name %q", name)
		return 0
	}
}

func TestExpandMediaKeyMatchesVectors(t *testing.T) {
	v, mk := loadVectors(t)
	seen := map[string]bool{}
	for _, c := range v.Cases {
		// Derivation is independent of plaintext; check once per type.
		if seen[c.MediaType] {
			continue
		}
		seen[c.MediaType] = true

		mt := typeForName(t, c.MediaType)
		iv, cipherKey, macKey, refKey, err := ExpandMediaKey(mk, mt)
		if err != nil {
			t.Fatalf("%s: ExpandMediaKey: %v", c.MediaType, err)
		}
		if got, want := iv[:], mustHex(t, c.IV); !bytes.Equal(got, want) {
			t.Errorf("%s iv = %x, want %x", c.MediaType, got, want)
		}
		if got, want := cipherKey[:], mustHex(t, c.CipherKey); !bytes.Equal(got, want) {
			t.Errorf("%s cipherKey = %x, want %x", c.MediaType, got, want)
		}
		if got, want := macKey[:], mustHex(t, c.MacKey); !bytes.Equal(got, want) {
			t.Errorf("%s macKey = %x, want %x", c.MediaType, got, want)
		}
		if got, want := refKey[:], mustHex(t, c.RefKey); !bytes.Equal(got, want) {
			t.Errorf("%s refKey = %x, want %x", c.MediaType, got, want)
		}
		if mt.String() != c.MediaType {
			t.Errorf("String() = %q, want %q", mt.String(), c.MediaType)
		}
		if info, _ := mt.info(); info != c.Info {
			t.Errorf("info() = %q, want %q", info, c.Info)
		}
	}
	if len(seen) != 4 {
		t.Errorf("expected 4 media types in vectors, saw %d", len(seen))
	}
}

func TestDecryptMatchesVectors(t *testing.T) {
	v, mk := loadVectors(t)
	for _, c := range v.Cases {
		mt := typeForName(t, c.MediaType)
		enc := mustHex(t, c.CiphertextHex)
		want := mustHex(t, v.Plaintexts[c.Plaintext].PlaintextHex)

		got, err := Decrypt(enc, mk, mt)
		if err != nil {
			t.Fatalf("%s/%s: Decrypt: %v", c.Plaintext, c.MediaType, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s/%s: plaintext mismatch (got %d bytes, want %d)",
				c.Plaintext, c.MediaType, len(got), len(want))
		}
	}
}

func TestEncryptReproducesVectors(t *testing.T) {
	v, mk := loadVectors(t)
	for _, c := range v.Cases {
		mt := typeForName(t, c.MediaType)
		plaintext := mustHex(t, v.Plaintexts[c.Plaintext].PlaintextHex)

		enc, fileSha, fileEncSha, err := Encrypt(plaintext, mk, mt)
		if err != nil {
			t.Fatalf("%s/%s: Encrypt: %v", c.Plaintext, c.MediaType, err)
		}
		if got, want := enc, mustHex(t, c.CiphertextHex); !bytes.Equal(got, want) {
			t.Errorf("%s/%s: enc mismatch\n got %x\nwant %x",
				c.Plaintext, c.MediaType, got[:min(32, len(got))], want[:min(32, len(want))])
		}
		if got, want := fileSha[:], mustHex(t, c.FileSha256); !bytes.Equal(got, want) {
			t.Errorf("%s/%s: fileSha256 = %x, want %x", c.Plaintext, c.MediaType, got, want)
		}
		if got, want := fileEncSha[:], mustHex(t, c.FileEncSha256); !bytes.Equal(got, want) {
			t.Errorf("%s/%s: fileEncSha256 = %x, want %x", c.Plaintext, c.MediaType, got, want)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	mk := [32]byte{1, 2, 3, 4, 5}
	for _, mt := range []MediaType{Image, Video, Audio, Document} {
		for _, n := range []int{0, 1, 15, 16, 17, 100, 70000} {
			plaintext := make([]byte, n)
			for i := range plaintext {
				plaintext[i] = byte(i * 7)
			}
			enc, _, _, err := Encrypt(plaintext, mk, mt)
			if err != nil {
				t.Fatalf("%s n=%d: Encrypt: %v", mt, n, err)
			}
			got, err := Decrypt(enc, mk, mt)
			if err != nil {
				t.Fatalf("%s n=%d: Decrypt: %v", mt, n, err)
			}
			if !bytes.Equal(got, plaintext) {
				t.Errorf("%s n=%d: roundtrip mismatch", mt, n)
			}
		}
	}
}

func TestTamperedMACFails(t *testing.T) {
	mk := [32]byte{9, 9, 9}
	enc, _, _, err := Encrypt([]byte("hello media world"), mk, Image)
	if err != nil {
		t.Fatal(err)
	}
	// Flip a bit in the MAC (last 10 bytes).
	bad := append([]byte(nil), enc...)
	bad[len(bad)-1] ^= 0x01
	if _, err := Decrypt(bad, mk, Image); err != ErrBadMAC {
		t.Errorf("tampered MAC: err = %v, want ErrBadMAC", err)
	}
	// Flip a bit in the ciphertext (also caught by MAC).
	bad2 := append([]byte(nil), enc...)
	bad2[0] ^= 0x01
	if _, err := Decrypt(bad2, mk, Image); err != ErrBadMAC {
		t.Errorf("tampered ciphertext: err = %v, want ErrBadMAC", err)
	}
}

func TestWrongTypeFailsMAC(t *testing.T) {
	mk := [32]byte{4, 2}
	enc, _, _, err := Encrypt([]byte("a document payload"), mk, Document)
	if err != nil {
		t.Fatal(err)
	}
	// Decrypting with a different type derives different keys -> MAC mismatch.
	if _, err := Decrypt(enc, mk, Image); err != ErrBadMAC {
		t.Errorf("wrong type: err = %v, want ErrBadMAC", err)
	}
}

func TestDifferentTypesDeriveDifferentKeys(t *testing.T) {
	mk := [32]byte{7, 7, 7, 7}
	types := []MediaType{Image, Video, Audio, Document}
	type derived struct{ iv, ck, mk_ string }
	seen := map[derived]MediaType{}
	for _, mt := range types {
		iv, ck, mkey, _, err := ExpandMediaKey(mk, mt)
		if err != nil {
			t.Fatal(err)
		}
		d := derived{string(iv[:]), string(ck[:]), string(mkey[:])}
		if other, dup := seen[d]; dup {
			t.Errorf("%s and %s derived identical keys", mt, other)
		}
		seen[d] = mt
	}
}

func TestShortBlob(t *testing.T) {
	mk := [32]byte{1}
	if _, err := Decrypt([]byte("tooshort"), mk, Image); err != ErrShortBlob {
		t.Errorf("short blob: err = %v, want ErrShortBlob", err)
	}
}

func TestUnknownTypeErrors(t *testing.T) {
	mk := [32]byte{1}
	if _, _, _, _, err := ExpandMediaKey(mk, MediaType(99)); err == nil {
		t.Error("expected error for unknown media type")
	}
}
