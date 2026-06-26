package signal

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jfelipesjc/wa-go/internal/keys"
)

// groupVectors mirrors testdata/signal/group_ab.json (only the fields the Go
// side needs). Produced by harness/gen_group_vectors.mjs from the same
// libsignal/Baileys group implementation WhatsApp uses.
type groupVectors struct {
	SenderKey struct {
		KeyID      uint32 `json:"keyId"`
		Iteration  uint32 `json:"iteration"`
		ChainKey   string `json:"chainKey"`
		SigningKey struct {
			Priv string `json:"priv"`
			Pub  string `json:"pub"`
		} `json:"signingKey"`
	} `json:"senderKey"`
	SKDMBytesHex string `json:"skdmBytesHex"`
	Messages     []struct {
		N             int    `json:"n"`
		Iteration     uint32 `json:"iteration"`
		Plaintext     string `json:"plaintext"`
		PlaintextHex  string `json:"plaintextHex"`
		CiphertextHex string `json:"ciphertextHex"`
	} `json:"messages"`
}

func loadGroupVectors(t *testing.T) groupVectors {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "signal", "group_ab.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read group vectors: %v", err)
	}
	var v groupVectors
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("unmarshal group vectors: %v", err)
	}
	return v
}

func mustHex32(t *testing.T, s string) [32]byte {
	t.Helper()
	var out [32]byte
	b := mustHex(t, s)
	if len(b) != 32 {
		t.Fatalf("expected 32 bytes, got %d", len(b))
	}
	copy(out[:], b)
	return out
}

// TestProcessSKDMAndDecrypt is the primary cross-impl check: bob installs alice's
// sender key from the real SKDM bytes and decrypts the real group ciphertexts.
func TestProcessSKDMAndDecrypt(t *testing.T) {
	v := loadGroupVectors(t)

	skdmBytes := mustHex(t, v.SKDMBytesHex)
	skdm, err := ParseSenderKeyDistributionMessage(skdmBytes)
	if err != nil {
		t.Fatalf("parse SKDM: %v", err)
	}

	// SKDM fields must match the vector.
	if skdm.KeyID != v.SenderKey.KeyID {
		t.Errorf("SKDM keyId = %d, want %d", skdm.KeyID, v.SenderKey.KeyID)
	}
	if skdm.Iteration != v.SenderKey.Iteration {
		t.Errorf("SKDM iteration = %d, want %d", skdm.Iteration, v.SenderKey.Iteration)
	}
	if got, want := skdm.ChainKey, mustHex32(t, v.SenderKey.ChainKey); got != want {
		t.Errorf("SKDM chainKey mismatch")
	}
	if got, want := skdm.SigningPub[:], mustHex(t, v.SenderKey.SigningKey.Pub); !bytes.Equal(got, want) {
		t.Errorf("SKDM signingKey mismatch")
	}

	// bob processes and decrypts.
	bob := NewGroupCipher(&SenderKeyRecord{})
	bob.ProcessSenderKeyDistribution(skdm)

	for _, m := range v.Messages {
		ct := mustHex(t, m.CiphertextHex)
		pt, err := bob.DecryptGroup(ct)
		if err != nil {
			t.Fatalf("decrypt group msg %d: %v", m.N, err)
		}
		if want := mustHex(t, m.PlaintextHex); !bytes.Equal(pt, want) {
			t.Errorf("msg %d plaintext = %q, want %q", m.N, pt, want)
		}
	}
}

// TestSenderKeyMessageIterations confirms the on-wire iterations match libsignal's
// (0, 2, 4) iteration+1 quirk, parsed straight from the golden ciphertexts.
func TestSenderKeyMessageIterations(t *testing.T) {
	v := loadGroupVectors(t)
	wantIters := []uint32{0, 2, 4}
	for i, m := range v.Messages {
		skm, err := ParseSenderKeyMessage(mustHex(t, m.CiphertextHex))
		if err != nil {
			t.Fatalf("parse msg %d: %v", m.N, err)
		}
		if skm.Iteration != m.Iteration {
			t.Errorf("msg %d parsed iteration %d != vector %d", m.N, skm.Iteration, m.Iteration)
		}
		if i < len(wantIters) && skm.Iteration != wantIters[i] {
			t.Errorf("msg %d iteration = %d, want %d", m.N, skm.Iteration, wantIters[i])
		}
	}
}

// TestEncryptGroupReproducesVectors checks byte-for-byte reproduction: replaying
// alice's sender key + signing key, EncryptGroup must reproduce each golden
// ciphertext exactly (the signature is deterministic XEdDSA over the same bytes).
func TestEncryptGroupReproducesVectors(t *testing.T) {
	v := loadGroupVectors(t)

	chainSeed := mustHex32(t, v.SenderKey.ChainKey)
	signing := keys.KeyPair{}
	copy(signing.Priv[:], mustHex(t, v.SenderKey.SigningKey.Priv))
	copy(signing.Pub[:], mustHex(t, v.SenderKey.SigningKey.Pub)[1:]) // strip 0x05

	alice := NewGroupCipher(&SenderKeyRecord{})
	skdm := alice.CreateSenderKeyDistribution(v.SenderKey.KeyID, chainSeed, signing)

	// The minted SKDM must reproduce the golden SKDM bytes byte-for-byte.
	got := SerializeSenderKeyDistributionMessage(skdm.KeyID, skdm.Iteration, skdm.ChainKey, skdm.SigningPub)
	if want := mustHex(t, v.SKDMBytesHex); !bytes.Equal(got, want) {
		t.Errorf("SKDM bytes mismatch:\n got %x\nwant %x", got, want)
	}

	for _, m := range v.Messages {
		ct, err := alice.EncryptGroup(mustHex(t, m.PlaintextHex))
		if err != nil {
			t.Fatalf("encrypt group msg %d: %v", m.N, err)
		}
		if want := mustHex(t, m.CiphertextHex); !bytes.Equal(ct, want) {
			t.Errorf("msg %d ciphertext mismatch:\n got %x\nwant %x", m.N, ct, want)
		}
	}
}

// TestGroupRoundTrip exercises a fresh sender->receiver round trip with random
// keys (no vector), verifying the two GroupCiphers interop end to end.
func TestGroupRoundTrip(t *testing.T) {
	signing, err := keys.GenKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	var chainSeed [32]byte
	for i := range chainSeed {
		chainSeed[i] = byte(i + 7)
	}

	alice := NewGroupCipher(&SenderKeyRecord{})
	skdm := alice.CreateSenderKeyDistribution(424242, chainSeed, signing)

	// Serialize/parse the SKDM as it would cross the 1:1 channel.
	skdmBytes := SerializeSenderKeyDistributionMessage(skdm.KeyID, skdm.Iteration, skdm.ChainKey, skdm.SigningPub)
	parsed, err := ParseSenderKeyDistributionMessage(skdmBytes)
	if err != nil {
		t.Fatal(err)
	}

	bob := NewGroupCipher(&SenderKeyRecord{})
	bob.ProcessSenderKeyDistribution(parsed)

	msgs := [][]byte{[]byte("ola grupo"), []byte("mais uma"), []byte("e a ultima aqui")}
	for i, pt := range msgs {
		ct, err := alice.EncryptGroup(pt)
		if err != nil {
			t.Fatalf("encrypt %d: %v", i, err)
		}
		dec, err := bob.DecryptGroup(ct)
		if err != nil {
			t.Fatalf("decrypt %d: %v", i, err)
		}
		if !bytes.Equal(dec, pt) {
			t.Errorf("msg %d round trip: got %q want %q", i, dec, pt)
		}
	}
}

// TestGroupSerializationRoundTrip checks the record persists and restores.
func TestGroupSerializationRoundTrip(t *testing.T) {
	v := loadGroupVectors(t)
	skdm, err := ParseSenderKeyDistributionMessage(mustHex(t, v.SKDMBytesHex))
	if err != nil {
		t.Fatal(err)
	}
	bob := NewGroupCipher(&SenderKeyRecord{})
	bob.ProcessSenderKeyDistribution(skdm)

	// Decrypt first message to advance/retain keys, then serialize.
	if _, err := bob.DecryptGroup(mustHex(t, v.Messages[0].CiphertextHex)); err != nil {
		t.Fatal(err)
	}
	blob, err := MarshalSenderKeyRecord(bob.Record())
	if err != nil {
		t.Fatal(err)
	}
	restored, err := UnmarshalSenderKeyRecord(blob)
	if err != nil {
		t.Fatal(err)
	}

	// Decrypt the remaining messages from the restored record.
	bob2 := NewGroupCipher(restored)
	for _, m := range v.Messages[1:] {
		pt, err := bob2.DecryptGroup(mustHex(t, m.CiphertextHex))
		if err != nil {
			t.Fatalf("decrypt after restore msg %d: %v", m.N, err)
		}
		if want := mustHex(t, m.PlaintextHex); !bytes.Equal(pt, want) {
			t.Errorf("restored msg %d mismatch", m.N)
		}
	}
}

// TestBadSignatureRejected flips a signature byte and expects DecryptGroup to
// fail signature verification.
func TestBadSignatureRejected(t *testing.T) {
	v := loadGroupVectors(t)
	skdm, err := ParseSenderKeyDistributionMessage(mustHex(t, v.SKDMBytesHex))
	if err != nil {
		t.Fatal(err)
	}
	bob := NewGroupCipher(&SenderKeyRecord{})
	bob.ProcessSenderKeyDistribution(skdm)

	ct := mustHex(t, v.Messages[0].CiphertextHex)
	// Corrupt the last byte (within the 64-byte trailing signature).
	bad := append([]byte(nil), ct...)
	bad[len(bad)-1] ^= 0xff
	if _, err := bob.DecryptGroup(bad); err == nil {
		t.Fatal("expected signature verification failure, got nil")
	}
}

// TestTamperedCiphertextRejected flips a ciphertext byte; the signature no longer
// matches so it must be rejected before decryption.
func TestTamperedCiphertextRejected(t *testing.T) {
	v := loadGroupVectors(t)
	skdm, err := ParseSenderKeyDistributionMessage(mustHex(t, v.SKDMBytesHex))
	if err != nil {
		t.Fatal(err)
	}
	bob := NewGroupCipher(&SenderKeyRecord{})
	bob.ProcessSenderKeyDistribution(skdm)

	ct := mustHex(t, v.Messages[0].CiphertextHex)
	bad := append([]byte(nil), ct...)
	// Flip a byte inside the protobuf ciphertext region (after version+tags,
	// before the signature).
	bad[10] ^= 0x01
	if _, err := bob.DecryptGroup(bad); err == nil {
		t.Fatal("expected failure on tampered ciphertext, got nil")
	}
}

// TestWrongSigningKeyRejected verifies a message signed by a different signing
// key is rejected.
func TestWrongSigningKeyRejected(t *testing.T) {
	wrong, err := keys.GenKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	v := loadGroupVectors(t)
	chainSeed := mustHex32(t, v.SenderKey.ChainKey)

	// alice signs with the WRONG key but advertises the CORRECT signing pub in
	// the SKDM bob installs.
	correctPub := mustHex(t, v.SenderKey.SigningKey.Pub)
	var correctPub33 [signalKeyLen]byte
	copy(correctPub33[:], correctPub)

	bob := NewGroupCipher(&SenderKeyRecord{})
	bob.ProcessSenderKeyDistribution(&SenderKeyDistributionMessage{
		KeyID:      v.SenderKey.KeyID,
		Iteration:  0,
		ChainKey:   chainSeed,
		SigningPub: correctPub33,
	})

	// Encrypt with a record whose signing key is `wrong`.
	alice := NewGroupCipher(&SenderKeyRecord{})
	alice.CreateSenderKeyDistribution(v.SenderKey.KeyID, chainSeed, wrong)
	ct, err := alice.EncryptGroup([]byte("forjada"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bob.DecryptGroup(ct); err == nil {
		t.Fatal("expected rejection of message signed by wrong key")
	}
}
