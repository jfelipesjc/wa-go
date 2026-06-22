package store

import (
	"bytes"
	"testing"
)

// kp builds a StoredKeyPair from two short byte patterns (padded conceptually;
// exact bytes don't matter for round-trip tests).
func kp(priv, pub byte) StoredKeyPair {
	return StoredKeyPair{
		Priv: bytes.Repeat([]byte{priv}, 32),
		Pub:  bytes.Repeat([]byte{pub}, 32),
	}
}

func openTestStore(t *testing.T) Store {
	t.Helper()
	st, err := OpenSQLite(dbPath(t))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestSignalStorePreKeyRoundTrip(t *testing.T) {
	st := openTestStore(t)

	want := map[uint32]StoredKeyPair{
		10: kp(0x01, 0x02),
		11: kp(0x03, 0x04),
	}
	if err := st.StorePreKeys(want); err != nil {
		t.Fatalf("StorePreKeys: %v", err)
	}

	for id, w := range want {
		got, ok, err := st.LoadPreKey(id)
		if err != nil {
			t.Fatalf("LoadPreKey(%d): %v", id, err)
		}
		if !ok {
			t.Fatalf("LoadPreKey(%d): ok=false", id)
		}
		if !bytes.Equal(got.Priv, w.Priv) || !bytes.Equal(got.Pub, w.Pub) {
			t.Fatalf("LoadPreKey(%d) mismatch: got %x/%x want %x/%x", id, got.Priv, got.Pub, w.Priv, w.Pub)
		}
	}

	// Remove consumes the prekey.
	if err := st.RemovePreKey(10); err != nil {
		t.Fatalf("RemovePreKey: %v", err)
	}
	if _, ok, err := st.LoadPreKey(10); err != nil || ok {
		t.Fatalf("LoadPreKey(10) after remove: ok=%v err=%v, want false/nil", ok, err)
	}
	// Sibling untouched.
	if _, ok, _ := st.LoadPreKey(11); !ok {
		t.Fatal("LoadPreKey(11) after removing 10: ok=false")
	}

	// Missing -> not found, no error.
	if _, ok, err := st.LoadPreKey(999); err != nil || ok {
		t.Fatalf("LoadPreKey(missing): ok=%v err=%v", ok, err)
	}
}

func TestSignalStoreSignedPreKeyRoundTrip(t *testing.T) {
	st := openTestStore(t)

	want := kp(0x05, 0x06)
	if err := st.StoreSignedPreKey(1, want); err != nil {
		t.Fatalf("StoreSignedPreKey: %v", err)
	}
	got, ok, err := st.LoadSignedPreKey(1)
	if err != nil || !ok {
		t.Fatalf("LoadSignedPreKey: ok=%v err=%v", ok, err)
	}
	if !bytes.Equal(got.Priv, want.Priv) || !bytes.Equal(got.Pub, want.Pub) {
		t.Fatalf("LoadSignedPreKey mismatch: got %x/%x want %x/%x", got.Priv, got.Pub, want.Priv, want.Pub)
	}

	// Namespaces are isolated: a pre-key id 1 must not collide.
	if err := st.StorePreKeys(map[uint32]StoredKeyPair{1: kp(0x07, 0x08)}); err != nil {
		t.Fatalf("StorePreKeys: %v", err)
	}
	again, _, _ := st.LoadSignedPreKey(1)
	if !bytes.Equal(again.Pub, want.Pub) {
		t.Fatal("signed pre-key 1 was clobbered by pre-key 1 — namespaces not isolated")
	}
}

func TestSignalStoreSessionRoundTrip(t *testing.T) {
	st := openTestStore(t)

	addr := "5511999998888.1"
	rec := []byte{0xde, 0xad, 0xbe, 0xef, 0x00, 0x10}
	if err := st.StoreSession(addr, rec); err != nil {
		t.Fatalf("StoreSession: %v", err)
	}
	got, ok, err := st.LoadSession(addr)
	if err != nil || !ok {
		t.Fatalf("LoadSession: ok=%v err=%v", ok, err)
	}
	if !bytes.Equal(got, rec) {
		t.Fatalf("session mismatch: got %x want %x", got, rec)
	}

	// Overwrite.
	rec2 := []byte{0x01}
	if err := st.StoreSession(addr, rec2); err != nil {
		t.Fatalf("StoreSession overwrite: %v", err)
	}
	got, _, _ = st.LoadSession(addr)
	if !bytes.Equal(got, rec2) {
		t.Fatalf("session overwrite mismatch: got %x want %x", got, rec2)
	}
}

func TestSignalStoreIdentityRoundTrip(t *testing.T) {
	st := openTestStore(t)

	addr := "5511999998888.0"
	key := bytes.Repeat([]byte{0x05}, 33)
	if err := st.SaveIdentity(addr, key); err != nil {
		t.Fatalf("SaveIdentity: %v", err)
	}
	got, ok, err := st.LoadIdentity(addr)
	if err != nil || !ok {
		t.Fatalf("LoadIdentity: ok=%v err=%v", ok, err)
	}
	if !bytes.Equal(got, key) {
		t.Fatalf("identity mismatch: got %x want %x", got, key)
	}

	if _, ok, err := st.LoadIdentity("missing.0"); err != nil || ok {
		t.Fatalf("LoadIdentity(missing): ok=%v err=%v", ok, err)
	}
}

// TestSignalStoreSenderKeyRoundTrip covers the group sender-key namespace keyed
// by (group, sender): distinct pairs are independent, and a missing pair reports
// not-found.
func TestSignalStoreSenderKeyRoundTrip(t *testing.T) {
	st := openTestStore(t)

	group := "120363000000000000@g.us"
	alice := "5551111111@s.whatsapp.net"
	bob := "5552222222@s.whatsapp.net"

	aliceRec := []byte(`{"states":[{"keyId":1}]}`)
	bobRec := []byte(`{"states":[{"keyId":2}]}`)

	if err := st.StoreSenderKey(group, alice, aliceRec); err != nil {
		t.Fatalf("StoreSenderKey(alice): %v", err)
	}
	if err := st.StoreSenderKey(group, bob, bobRec); err != nil {
		t.Fatalf("StoreSenderKey(bob): %v", err)
	}

	got, ok, err := st.LoadSenderKey(group, alice)
	if err != nil || !ok {
		t.Fatalf("LoadSenderKey(alice): ok=%v err=%v", ok, err)
	}
	if !bytes.Equal(got, aliceRec) {
		t.Fatalf("alice record = %q, want %q", got, aliceRec)
	}

	got, ok, _ = st.LoadSenderKey(group, bob)
	if !ok || !bytes.Equal(got, bobRec) {
		t.Fatalf("bob record = %q (ok=%v), want %q", got, ok, bobRec)
	}

	// Different sender in same group, and same sender in different group: not found.
	if _, ok, _ := st.LoadSenderKey(group, "5559999999@s.whatsapp.net"); ok {
		t.Fatal("unexpected sender key for unknown sender")
	}
	if _, ok, _ := st.LoadSenderKey("other@g.us", alice); ok {
		t.Fatal("unexpected sender key for different group")
	}

	// Overwrite updates in place.
	updated := []byte(`{"states":[{"keyId":1,"iteration":5}]}`)
	if err := st.StoreSenderKey(group, alice, updated); err != nil {
		t.Fatal(err)
	}
	got, _, _ = st.LoadSenderKey(group, alice)
	if !bytes.Equal(got, updated) {
		t.Fatalf("after overwrite = %q, want %q", got, updated)
	}
}

func TestSignalStoreAppStateSyncKeyRoundTrip(t *testing.T) {
	st := openTestStore(t)

	keyID := []byte{0x01, 0x02, 0x03, 0x04, 0x05}
	keyData := bytes.Repeat([]byte{0xAA}, 32)

	if _, ok, err := st.LoadAppStateSyncKey(keyID); ok || err != nil {
		t.Fatalf("LoadAppStateSyncKey before store: ok=%v err=%v", ok, err)
	}

	if err := st.StoreAppStateSyncKey(keyID, keyData); err != nil {
		t.Fatalf("StoreAppStateSyncKey: %v", err)
	}

	got, ok, err := st.LoadAppStateSyncKey(keyID)
	if err != nil || !ok {
		t.Fatalf("LoadAppStateSyncKey: ok=%v err=%v", ok, err)
	}
	if !bytes.Equal(got, keyData) {
		t.Fatalf("keyData = %x, want %x", got, keyData)
	}

	// Different keyId: not found.
	if _, ok, _ := st.LoadAppStateSyncKey([]byte{0x09}); ok {
		t.Fatal("unexpected key for unknown keyId")
	}

	// Overwrite in place.
	updated := bytes.Repeat([]byte{0xBB}, 32)
	if err := st.StoreAppStateSyncKey(keyID, updated); err != nil {
		t.Fatal(err)
	}
	got, _, _ = st.LoadAppStateSyncKey(keyID)
	if !bytes.Equal(got, updated) {
		t.Fatalf("after overwrite = %x, want %x", got, updated)
	}
}
