package signal

import (
	"testing"

	"github.com/jfelipesjc/wa-go/internal/keys"
)

// Test 5: SessionRecord serialize/deserialize round-trip, including a live
// session derived from the golden vectors and with a skipped message key.
func TestSessionRecordRoundTrip(t *testing.T) {
	v := loadVectors(t)

	bobIdentity := v.Bob.IdentityKeyPair.keyPair(t)
	bobSignedPre := v.Bob.SignedPreKey.KeyPair.keyPair(t)
	bobPreKey := v.Bob.PreKey.KeyPair.keyPair(t)

	_, st, err := ProcessPreKeyMessage(
		mustHex(t, v.Exchanges[0].CiphertextHex),
		bobIdentity, bobSignedPre, &bobPreKey, v.Bob.RegistrationID,
		WithRatchetGenerator(func() (keys.KeyPair, error) { return v.EphemeralsGenerated[2].keyPair(t), nil }),
	)
	if err != nil {
		t.Fatalf("ProcessPreKeyMessage: %v", err)
	}

	// Inject a skipped key to exercise that path of serialization.
	var sk skippedKey
	sk.Counter = 7
	copy(sk.Ratchet[:], st.TheirRatchetPub[:])
	st.Skipped[sk] = deriveMessageKeys(st.ReceivingChain, 7)

	rec := &SessionRecord{State: st}
	blob, err := rec.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	back, err := UnmarshalSessionRecord(blob)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.State == nil {
		t.Fatal("round-trip lost state")
	}
	g := back.State
	if g.RootKey != st.RootKey {
		t.Error("rootKey mismatch")
	}
	if g.RemoteIdentityPub != st.RemoteIdentityPub {
		t.Error("remoteIdentityPub mismatch")
	}
	if g.LocalIdentityPub != st.LocalIdentityPub {
		t.Error("localIdentityPub mismatch")
	}
	if g.TheirRatchetPub != st.TheirRatchetPub {
		t.Error("theirRatchetPub mismatch")
	}
	if g.ReceivingChain != st.ReceivingChain || g.HasReceivingChain != st.HasReceivingChain {
		t.Error("receiving chain mismatch")
	}
	if g.SendingRatchet != st.SendingRatchet {
		t.Error("sending ratchet mismatch")
	}
	if g.RecvCounter != st.RecvCounter || g.SendCounter != st.SendCounter {
		t.Error("counters mismatch")
	}
	if len(g.Skipped) != len(st.Skipped) {
		t.Fatalf("skipped count %d, want %d", len(g.Skipped), len(st.Skipped))
	}
	if g.Skipped[sk].cipherKey != st.Skipped[sk].cipherKey {
		t.Error("skipped key cipherKey mismatch")
	}

	// An empty record round-trips to no state.
	empty := &SessionRecord{}
	eb, err := empty.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	eback, err := UnmarshalSessionRecord(eb)
	if err != nil {
		t.Fatal(err)
	}
	if eback.State != nil {
		t.Error("empty record should have nil state")
	}
}
