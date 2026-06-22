package signal

import (
	"bytes"
	"testing"
)

func TestParsePreKeyWhisperMessage(t *testing.T) {
	v := loadVectors(t)
	ex := v.Exchanges[0] // pkmsg a->b
	if ex.Type != "pkmsg" {
		t.Fatalf("exchange 0 not pkmsg")
	}
	pm, err := ParsePreKeyWhisperMessage(mustHex(t, ex.CiphertextHex))
	if err != nil {
		t.Fatalf("parse pkmsg: %v", err)
	}
	if !pm.HasPreKeyID || pm.PreKeyID != v.Bob.PreKey.ID {
		t.Errorf("preKeyId = %d, want %d", pm.PreKeyID, v.Bob.PreKey.ID)
	}
	if pm.SignedPreKeyID != v.Bob.SignedPreKey.ID {
		t.Errorf("signedPreKeyId = %d, want %d", pm.SignedPreKeyID, v.Bob.SignedPreKey.ID)
	}
	if pm.RegistrationID != v.Alice.RegistrationID {
		t.Errorf("registrationId = %d, want %d", pm.RegistrationID, v.Alice.RegistrationID)
	}
	if pm.BaseKey != v.Alice.BaseKey.signalPub(t) {
		t.Errorf("baseKey mismatch")
	}
	if pm.IdentityKey != v.Alice.IdentityKeyPair.signalPub(t) {
		t.Errorf("identityKey mismatch")
	}

	// Re-serialize and confirm byte-identical (field order matters).
	if got := pm.Serialize(); !bytes.Equal(got, mustHex(t, ex.CiphertextHex)) {
		t.Errorf("pkmsg re-serialize mismatch:\n got %x\nwant %s", got, ex.CiphertextHex)
	}
}

func TestParseWhisperMessage(t *testing.T) {
	v := loadVectors(t)
	ex := v.Exchanges[1] // msg b->a
	wm, signed, mac, err := parseWhisperMessage(mustHex(t, ex.CiphertextHex))
	if err != nil {
		t.Fatalf("parse msg: %v", err)
	}
	if wm.RatchetKey != v.EphemeralsGenerated[2].signalPub(t) {
		t.Errorf("ratchetKey mismatch (expected eph[2] = bob ratchet for msg2)")
	}
	if wm.Counter != 0 {
		t.Errorf("counter = %d, want 0", wm.Counter)
	}
	if len(mac) != macLength {
		t.Errorf("mac len = %d, want %d", len(mac), macLength)
	}
	if signed[0] != versionByte() {
		t.Errorf("version byte = 0x%02x", signed[0])
	}
}
