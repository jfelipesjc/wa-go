package store

import (
	"bytes"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jfelipesjc/wa-go/internal/keys"
)

// credsFromIdentity builds a serializable Creds from a freshly generated identity.
func credsFromIdentity(t *testing.T) *Creds {
	t.Helper()
	id, err := keys.NewIdentity()
	if err != nil {
		t.Fatalf("NewIdentity: %v", err)
	}
	return &Creds{
		NoiseKey:       CredKeyPair{Priv: id.NoiseKey.Priv[:], Pub: id.NoiseKey.Pub[:]},
		IdentityKey:    CredKeyPair{Priv: id.IdentityKey.Priv[:], Pub: id.IdentityKey.Pub[:]},
		RegistrationID: id.RegistrationID,
		AdvSecret:      id.AdvSecret[:],
		SignedPreKey: CredSignedPreKey{
			KeyID:     id.SignedPreKey.KeyID,
			KeyPair:   CredKeyPair{Priv: id.SignedPreKey.KeyPair.Priv[:], Pub: id.SignedPreKey.KeyPair.Pub[:]},
			Signature: id.SignedPreKey.Signature[:],
		},
	}
}

func dbPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "creds.db")
}

func TestOpenCreatesSchemaAndEmptyLoad(t *testing.T) {
	st, err := OpenSQLite(dbPath(t))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer st.Close()

	c, ok, err := st.LoadCreds()
	if err != nil {
		t.Fatalf("LoadCreds on empty db: %v", err)
	}
	if ok {
		t.Fatal("LoadCreds on empty db returned ok=true")
	}
	if c != nil {
		t.Fatalf("LoadCreds on empty db returned non-nil creds: %+v", c)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	st, err := OpenSQLite(dbPath(t))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer st.Close()

	want := credsFromIdentity(t)
	// Set post-pairing fields too, to cover them in the round-trip.
	want.Me = "5511999998888:7@s.whatsapp.net"
	want.Account = []byte{0x0a, 0x10, 0xde, 0xad, 0xbe, 0xef}
	want.Platform = "android"
	want.PushName = "Chip Sacrificial"
	want.Registered = true

	if err := st.SaveCreds(want); err != nil {
		t.Fatalf("SaveCreds: %v", err)
	}

	got, ok, err := st.LoadCreds()
	if err != nil {
		t.Fatalf("LoadCreds: %v", err)
	}
	if !ok {
		t.Fatal("LoadCreds returned ok=false after SaveCreds")
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("round-trip mismatch:\n want %+v\n  got %+v", want, got)
	}
}

func TestPersistenceAcrossConnections(t *testing.T) {
	path := dbPath(t)

	st1, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite 1: %v", err)
	}
	want := credsFromIdentity(t)
	want.Me = "5511000000000:1@s.whatsapp.net"
	want.Registered = true
	if err := st1.SaveCreds(want); err != nil {
		t.Fatalf("SaveCreds: %v", err)
	}
	if err := st1.Close(); err != nil {
		t.Fatalf("Close 1: %v", err)
	}

	st2, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite 2 (reopen): %v", err)
	}
	defer st2.Close()

	got, ok, err := st2.LoadCreds()
	if err != nil {
		t.Fatalf("LoadCreds after reopen: %v", err)
	}
	if !ok {
		t.Fatal("LoadCreds after reopen: ok=false")
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("persisted creds mismatch:\n want %+v\n  got %+v", want, got)
	}
}

func TestSaveCredsOverwrites(t *testing.T) {
	st, err := OpenSQLite(dbPath(t))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer st.Close()

	c := credsFromIdentity(t)
	c.PushName = "first"
	if err := st.SaveCreds(c); err != nil {
		t.Fatalf("SaveCreds 1: %v", err)
	}
	c.PushName = "second"
	if err := st.SaveCreds(c); err != nil {
		t.Fatalf("SaveCreds 2: %v", err)
	}
	got, _, err := st.LoadCreds()
	if err != nil {
		t.Fatalf("LoadCreds: %v", err)
	}
	if got.PushName != "second" {
		t.Fatalf("overwrite failed: PushName = %q, want %q", got.PushName, "second")
	}
}

func TestSignalKVRoundTrip(t *testing.T) {
	st, err := OpenSQLite(dbPath(t))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer st.Close()

	// SignalStore methods exercise the (namespace, key) kv table.
	sessAddr := "5511999998888.1"
	rec := []byte{0x01, 0x02, 0x03, 0xff, 0x00}
	if err := st.StoreSession(sessAddr, rec); err != nil {
		t.Fatalf("StoreSession: %v", err)
	}
	got, ok, err := st.LoadSession(sessAddr)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if !ok {
		t.Fatal("LoadSession: ok=false for stored session")
	}
	if !bytes.Equal(got, rec) {
		t.Fatalf("session blob mismatch: got %x want %x", got, rec)
	}

	// Missing key -> not found, no error.
	_, ok, err = st.LoadSession("nonexistent.9")
	if err != nil {
		t.Fatalf("LoadSession(missing): %v", err)
	}
	if ok {
		t.Fatal("LoadSession(missing): ok=true")
	}

	// Pre-keys + signed pre-key namespaces are isolated.
	if err := st.PutPreKeys(map[uint32][]byte{42: {0xaa, 0xbb}}); err != nil {
		t.Fatalf("PutPreKeys: %v", err)
	}
	if _, ok, _ := st.GetSignedPreKey(42); ok {
		t.Fatal("GetSignedPreKey(42) found a pre-key blob — namespaces not isolated")
	}

	// Generic KV (the kvGet of a missing key) via the concrete type.
	concrete := st.(*sqliteStore)
	if _, ok, err := concrete.KVGet("custom_ns", "missing"); err != nil || ok {
		t.Fatalf("KVGet(missing) = ok %v err %v, want false/nil", ok, err)
	}
	if err := concrete.KVPut("custom_ns", "k", []byte("v")); err != nil {
		t.Fatalf("KVPut: %v", err)
	}
	v, ok, err := concrete.KVGet("custom_ns", "k")
	if err != nil || !ok || string(v) != "v" {
		t.Fatalf("KVGet round-trip: v=%q ok=%v err=%v", v, ok, err)
	}
}
