package signal

import (
	"bytes"
	"testing"

	"github.com/jfelipesjc/wa-go/internal/keys"
)

// Test 4: ALICE (initiator) reproduces the golden ciphertexts byte-for-byte by
// injecting the captured ephemerals. This proves the encrypt path (X3DH
// initiator + Double Ratchet + protobuf + MAC) matches libsignal exactly.
func TestEncryptReproducesGolden(t *testing.T) {
	v := loadVectors(t)

	aliceIdentity := v.Alice.IdentityKeyPair.keyPair(t)
	aliceBase := v.Alice.BaseKey.keyPair(t)                    // EK_a = eph[0]
	aliceInitialRatchet := v.EphemeralsGenerated[1].keyPair(t) // eph[1]
	aliceNextRatchet := v.EphemeralsGenerated[3].keyPair(t)    // eph[3] for #3/#4

	p := InitiatorParams{
		LocalIdentity:   aliceIdentity,
		LocalBaseKey:    aliceBase,
		RemoteIdentity:  v.Bob.IdentityKeyPair.signalPub(t),
		RemoteSignedPre: v.Bob.SignedPreKey.KeyPair.signalPub(t),
		RemotePreKey:    v.Bob.PreKey.KeyPair.signalPub(t),
		HasPreKey:       true,
	}
	st, err := InitiateSession(p, aliceInitialRatchet)
	if err != nil {
		t.Fatalf("InitiateSession: %v", err)
	}
	st.LocalRegID = v.Alice.RegistrationID

	// Set up pending-prekey wrapping so #1 is emitted as a pkmsg matching the
	// bundle alice used.
	st.PendingActive = true
	st.PendingBaseKey = v.Alice.BaseKey.signalPub(t)
	st.PendingPreKeyID = v.Bob.PreKey.ID
	st.HasPendingPreKey = true
	st.PendingSignedPreKeyID = v.Bob.SignedPreKey.ID

	// When alice receives bob's #2 (eph[2]), she ratchets and generates eph[3]
	// as her new sending ratchet (used for #3/#4).
	genQueue := []keys.KeyPair{aliceNextRatchet}
	st.genRatchet = func() (keys.KeyPair, error) {
		kp := genQueue[0]
		genQueue = genQueue[1:]
		return kp, nil
	}

	cipher := NewSessionCipher(st)

	// --- Encrypt #1 (pkmsg) ---
	ex1 := v.Exchanges[0]
	out1, err := cipher.Encrypt([]byte(ex1.Plaintext))
	if err != nil {
		t.Fatalf("encrypt #1: %v", err)
	}
	if out1.Type != "pkmsg" {
		t.Errorf("#1 type = %q, want pkmsg", out1.Type)
	}
	if !bytes.Equal(out1.Serialized, mustHex(t, ex1.CiphertextHex)) {
		t.Fatalf("#1 ciphertext mismatch:\n got %x\nwant %s", out1.Serialized, ex1.CiphertextHex)
	}
	t.Logf("encrypt #1 (pkmsg) byte-exact OK")

	// --- Receive bob's #2 so alice's ratchet advances to eph[3] ---
	ex2 := v.Exchanges[1]
	pt2, err := cipher.Decrypt(mustHex(t, ex2.CiphertextHex))
	if err != nil {
		t.Fatalf("decrypt #2: %v", err)
	}
	if string(pt2) != ex2.Plaintext {
		t.Fatalf("#2 plaintext = %q, want %q", pt2, ex2.Plaintext)
	}

	// --- Encrypt #3 and #4 (same sending chain eph[3], counters 0 and 1) ---
	for _, idx := range []int{2, 3} {
		ex := v.Exchanges[idx]
		out, err := cipher.Encrypt([]byte(ex.Plaintext))
		if err != nil {
			t.Fatalf("encrypt #%d: %v", ex.N, err)
		}
		if out.Type != "msg" {
			t.Errorf("#%d type = %q, want msg", ex.N, out.Type)
		}
		if !bytes.Equal(out.Serialized, mustHex(t, ex.CiphertextHex)) {
			t.Fatalf("#%d ciphertext mismatch:\n got %x\nwant %s", ex.N, out.Serialized, ex.CiphertextHex)
		}
		t.Logf("encrypt #%d (msg) byte-exact OK", ex.N)
	}
}
