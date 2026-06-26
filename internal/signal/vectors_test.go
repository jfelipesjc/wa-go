package signal

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jfelipesjc/wa-go/internal/keys"
)

// goldenVectors mirrors testdata/signal/session_ab.json.
type goldenVectors struct {
	Bob struct {
		IdentityKeyPair hexPair `json:"identityKeyPair"`
		RegistrationID  uint32  `json:"registrationId"`
		SignedPreKey    struct {
			ID        uint32  `json:"id"`
			KeyPair   hexPair `json:"keyPair"`
			Signature string  `json:"signature"`
		} `json:"signedPreKey"`
		PreKey struct {
			ID      uint32  `json:"id"`
			KeyPair hexPair `json:"keyPair"`
		} `json:"preKey"`
	} `json:"bob"`
	Alice struct {
		IdentityKeyPair hexPair `json:"identityKeyPair"`
		RegistrationID  uint32  `json:"registrationId"`
		BaseKey         hexPair `json:"baseKey"`
	} `json:"alice"`
	EphemeralsGenerated []hexPair `json:"ephemeralsGenerated"`
	Exchanges           []struct {
		N             int    `json:"n"`
		Dir           string `json:"dir"`
		Type          string `json:"type"`
		CiphertextHex string `json:"ciphertextHex"`
		PlaintextHex  string `json:"plaintextHex"`
		Plaintext     string `json:"plaintext"`
	} `json:"exchanges"`
}

type hexPair struct {
	Priv string `json:"priv"`
	Pub  string `json:"pub"`
}

// keyPair converts a hexPair (32-byte priv, 33-byte 0x05-prefixed pub) to a
// keys.KeyPair (raw 32-byte pub).
func (h hexPair) keyPair(t *testing.T) keys.KeyPair {
	t.Helper()
	priv, err := hex.DecodeString(h.Priv)
	if err != nil || len(priv) != 32 {
		t.Fatalf("bad priv hex %q: %v", h.Priv, err)
	}
	pub, err := hex.DecodeString(h.Pub)
	if err != nil || len(pub) != 33 {
		t.Fatalf("bad pub hex %q: %v", h.Pub, err)
	}
	var kp keys.KeyPair
	copy(kp.Priv[:], priv)
	copy(kp.Pub[:], pub[1:]) // strip 0x05
	return kp
}

// pub33 returns the 33-byte 0x05-prefixed pub from a hexPair.
func (h hexPair) signalPub(t *testing.T) [signalKeyLen]byte {
	t.Helper()
	pub, err := hex.DecodeString(h.Pub)
	if err != nil || len(pub) != 33 {
		t.Fatalf("bad pub hex %q: %v", h.Pub, err)
	}
	var out [signalKeyLen]byte
	copy(out[:], pub)
	return out
}

func loadVectors(t *testing.T) *goldenVectors {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "signal", "session_ab.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var v goldenVectors
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("parse vectors: %v", err)
	}
	return &v
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex %q: %v", s, err)
	}
	return b
}
