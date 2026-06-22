package keys

import (
	"bytes"
	"encoding/hex"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/curve25519"
)

func TestGenKeyPair(t *testing.T) {
	kp, err := GenKeyPair()
	if err != nil {
		t.Fatalf("GenKeyPair: %v", err)
	}

	// Pub must equal Priv * basepoint.
	want, err := curve25519.X25519(kp.Priv[:], curve25519.Basepoint)
	if err != nil {
		t.Fatalf("X25519: %v", err)
	}
	if !bytes.Equal(want, kp.Pub[:]) {
		t.Fatalf("Pub != X25519(Priv, basepoint)\n got %x\nwant %x", kp.Pub, want)
	}

	// Non-zero.
	var zero [32]byte
	if kp.Priv == zero {
		t.Fatal("Priv is all zero")
	}
	if kp.Pub == zero {
		t.Fatal("Pub is all zero")
	}

	// Clamping: low 3 bits of byte 0 clear, top bit of byte 31 clear,
	// second-highest bit of byte 31 set.
	if kp.Priv[0]&0b111 != 0 {
		t.Errorf("Priv[0] low 3 bits not cleared: %08b", kp.Priv[0])
	}
	if kp.Priv[31]&0x80 != 0 {
		t.Errorf("Priv[31] top bit not cleared: %08b", kp.Priv[31])
	}
	if kp.Priv[31]&0x40 == 0 {
		t.Errorf("Priv[31] bit 6 not set: %08b", kp.Priv[31])
	}

	// Two calls differ.
	kp2, err := GenKeyPair()
	if err != nil {
		t.Fatalf("GenKeyPair 2: %v", err)
	}
	if kp.Priv == kp2.Priv {
		t.Fatal("two GenKeyPair calls produced identical private keys")
	}
}

func TestNewRegistrationID(t *testing.T) {
	// Range matches Baileys generateRegistrationId: randomBytes(2)[0] & 16383,
	// i.e. [0, 16383] inclusive.
	const max = 16383
	sawNonTrivial := false
	for i := 0; i < 1000; i++ {
		id := NewRegistrationID()
		if id > max {
			t.Fatalf("registration id %d out of range [0, %d]", id, max)
		}
		if id != 0 {
			sawNonTrivial = true
		}
	}
	if !sawNonTrivial {
		t.Fatal("all 1000 registration ids were 0; randomness suspect")
	}
}

func TestNewAdvSecret(t *testing.T) {
	s1, err := NewAdvSecret()
	if err != nil {
		t.Fatalf("NewAdvSecret: %v", err)
	}
	if len(s1) != 32 {
		t.Fatalf("adv secret len = %d, want 32", len(s1))
	}
	var zero [32]byte
	if s1 == zero {
		t.Fatal("adv secret all zero")
	}
	s2, err := NewAdvSecret()
	if err != nil {
		t.Fatalf("NewAdvSecret 2: %v", err)
	}
	if s1 == s2 {
		t.Fatal("two NewAdvSecret calls returned identical secrets")
	}
}

func TestGenSignedPreKey(t *testing.T) {
	identity, err := GenKeyPair()
	if err != nil {
		t.Fatalf("identity GenKeyPair: %v", err)
	}
	spk, err := GenSignedPreKey(identity, 1)
	if err != nil {
		t.Fatalf("GenSignedPreKey: %v", err)
	}
	if spk.KeyID != 1 {
		t.Errorf("KeyID = %d, want 1", spk.KeyID)
	}
	if len(spk.Signature) != 64 {
		t.Fatalf("signature len = %d, want 64", len(spk.Signature))
	}
	var zeroSig [64]byte
	if spk.Signature == zeroSig {
		t.Fatal("signature is all zero")
	}

	// Validate the XEdDSA construction with our own verify against the identity
	// public key. The signed message is the 0x05-prefixed pre-key public key.
	msg := signalPubKey(spk.KeyPair.Pub)
	if !xeddsaVerify(identity.Pub, msg, spk.Signature) {
		t.Fatal("xeddsaVerify rejected a freshly generated signature")
	}

	// Tampered message must fail.
	bad := append([]byte(nil), msg...)
	bad[10] ^= 0xff
	if xeddsaVerify(identity.Pub, bad, spk.Signature) {
		t.Fatal("xeddsaVerify accepted a tampered message")
	}

	// Cross-check against Baileys' own Curve.verify if node + harness are
	// available. This proves byte-for-byte compatibility with the real client,
	// not just self-consistency.
	crossCheckWithBaileys(t, identity.Pub, msg, spk.Signature)
}

// crossCheckWithBaileys runs the harness node helper to verify the signature
// with Baileys' Curve.verify. Skips (does not fail) if node or the harness
// node_modules are unavailable.
func crossCheckWithBaileys(t *testing.T, pub [32]byte, msg []byte, sig [64]byte) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Log("node not found; skipping Baileys cross-check")
		return
	}
	// harness dir = <repo>/harness ; this test runs from internal/keys.
	harness, err := filepath.Abs(filepath.Join("..", "..", "harness"))
	if err != nil {
		t.Fatalf("abs harness path: %v", err)
	}
	script, err := filepath.Abs(filepath.Join("testdata", "verify_sig.mjs"))
	if err != nil {
		t.Fatalf("abs script path: %v", err)
	}
	cmd := exec.Command(node, script, harness, hex.EncodeToString(pub[:]), hex.EncodeToString(msg), hex.EncodeToString(sig[:]))
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Distinguish "module missing" (skip) from "verification failed" (fail).
		if bytes.Contains(out, []byte("Cannot find")) || bytes.Contains(out, []byte("ERR_MODULE_NOT_FOUND")) || bytes.Contains(out, []byte("Error: Cannot")) {
			t.Logf("Baileys not available, skipping cross-check: %s", out)
			return
		}
		t.Fatalf("Baileys Curve.verify rejected our signature: %v\n%s", err, out)
	}
	if !bytes.Contains(out, []byte("OK")) {
		t.Fatalf("Baileys cross-check did not print OK: %s", out)
	}
}

func TestNewIdentity(t *testing.T) {
	id, err := NewIdentity()
	if err != nil {
		t.Fatalf("NewIdentity: %v", err)
	}

	var zero32 [32]byte
	if id.NoiseKey.Priv == zero32 || id.NoiseKey.Pub == zero32 {
		t.Error("NoiseKey not populated")
	}
	if id.IdentityKey.Priv == zero32 || id.IdentityKey.Pub == zero32 {
		t.Error("IdentityKey not populated")
	}
	if id.AdvSecret == zero32 {
		t.Error("AdvSecret not populated")
	}
	if id.RegistrationID > 16383 {
		t.Errorf("RegistrationID %d out of range", id.RegistrationID)
	}
	if id.SignedPreKey.KeyID != 1 {
		t.Errorf("SignedPreKey.KeyID = %d, want 1", id.SignedPreKey.KeyID)
	}
	var zeroSig [64]byte
	if id.SignedPreKey.Signature == zeroSig {
		t.Error("SignedPreKey.Signature not populated")
	}
	if id.SignedPreKey.KeyPair.Pub == zero32 {
		t.Error("SignedPreKey.KeyPair not populated")
	}

	// The signed pre-key must verify against the identity key.
	msg := signalPubKey(id.SignedPreKey.KeyPair.Pub)
	if !xeddsaVerify(id.IdentityKey.Pub, msg, id.SignedPreKey.Signature) {
		t.Fatal("NewIdentity signed pre-key failed verification")
	}
}
