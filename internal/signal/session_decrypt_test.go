package signal

import (
	"testing"

	"github.com/felipeleal/wa-go/internal/keys"
)

// Test 1 + 2: BOB (responder) decrypts the pkmsg and the later ratchet msgs.
// This is the real production scenario — we receive and decrypt.
func TestDecryptResponder(t *testing.T) {
	v := loadVectors(t)

	bobIdentity := v.Bob.IdentityKeyPair.keyPair(t)
	bobSignedPre := v.Bob.SignedPreKey.KeyPair.keyPair(t)
	bobPreKey := v.Bob.PreKey.KeyPair.keyPair(t)

	// When bob receives alice's #1 (eph1), libsignal generates a NEW sending
	// ratchet for bob — in the real flow that became eph[2] (used to send #2).
	// Inject eph[2] so bob's subsequent receive of alice's eph[3] (msgs #3/#4)
	// DHs against the correct key. (In production this is random; bob would have
	// actually sent #2 with whatever he generated.)
	// genQueue: eph[2] when receiving #1, eph[4] when receiving #3 (the next
	// sending ratchet bob would generate). eph[4] is never used to decrypt here.
	eph2 := v.EphemeralsGenerated[2].keyPair(t)
	eph4 := v.EphemeralsGenerated[4].keyPair(t)
	genQueue := []keys.KeyPair{eph2, eph4}
	gen := func() (keys.KeyPair, error) {
		kp := genQueue[0]
		genQueue = genQueue[1:]
		return kp, nil
	}

	// Exchange #1: pkmsg a->b -> establishes session + first plaintext.
	ex1 := v.Exchanges[0]
	pt, st, err := ProcessPreKeyMessage(
		mustHex(t, ex1.CiphertextHex),
		bobIdentity, bobSignedPre, &bobPreKey, v.Bob.RegistrationID,
		WithRatchetGenerator(gen),
	)
	if err != nil {
		t.Fatalf("ProcessPreKeyMessage: %v", err)
	}
	if string(pt) != ex1.Plaintext {
		t.Fatalf("pkmsg plaintext = %q, want %q", pt, ex1.Plaintext)
	}
	t.Logf("decrypted #1 (pkmsg): %q", pt)

	cipher := NewSessionCipher(st)

	// Exchanges #3 and #4: msg a->b, same alice sending chain (eph[3]), counters 0,1.
	for _, idx := range []int{2, 3} {
		ex := v.Exchanges[idx]
		pt, err := cipher.Decrypt(mustHex(t, ex.CiphertextHex))
		if err != nil {
			t.Fatalf("decrypt exchange #%d: %v", ex.N, err)
		}
		if string(pt) != ex.Plaintext {
			t.Fatalf("exchange #%d plaintext = %q, want %q", ex.N, pt, ex.Plaintext)
		}
		t.Logf("decrypted #%d (msg): %q", ex.N, pt)
	}
}

// Test 3: ALICE (initiator) decrypts bob's reply (exchange #2, b->a).
// Validates X3DH initiator + receiving a ratchet from bob.
func TestDecryptInitiator(t *testing.T) {
	v := loadVectors(t)

	aliceIdentity := v.Alice.IdentityKeyPair.keyPair(t)
	aliceBase := v.Alice.BaseKey.keyPair(t)                    // EK_a = eph[0]
	aliceInitialRatchet := v.EphemeralsGenerated[1].keyPair(t) // eph[1]

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

	cipher := NewSessionCipher(st)

	// Exchange #2: b->a "oi alice 2". For alice, the MAC sender is bob (remote),
	// receiver is alice (local) — Decrypt handles that.
	ex2 := v.Exchanges[1]
	pt, err := cipher.Decrypt(mustHex(t, ex2.CiphertextHex))
	if err != nil {
		t.Fatalf("decrypt exchange #2 (b->a): %v", err)
	}
	if string(pt) != ex2.Plaintext {
		t.Fatalf("exchange #2 plaintext = %q, want %q", pt, ex2.Plaintext)
	}
	t.Logf("alice decrypted #2 (msg b->a): %q", pt)
}

// Ensure the keys package DH wrapper agrees with what we expect (sanity).
func TestKeysAvailable(t *testing.T) {
	if _, err := keys.GenKeyPair(); err != nil {
		t.Fatal(err)
	}
}
