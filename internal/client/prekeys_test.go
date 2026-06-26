package client

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/jfelipesjc/wa-go/internal/keys"
	"github.com/jfelipesjc/wa-go/internal/store"
	"github.com/jfelipesjc/wa-go/internal/wire"
)

func TestEncodeBigEndianN(t *testing.T) {
	cases := []struct {
		v    uint32
		n    int
		want []byte
	}{
		{0x010203, 3, []byte{0x01, 0x02, 0x03}},
		{1, 4, []byte{0x00, 0x00, 0x00, 0x01}},
		{0xABCD, 3, []byte{0x00, 0xAB, 0xCD}},
	}
	for _, c := range cases {
		if got := encodeBigEndianN(c.v, c.n); !bytes.Equal(got, c.want) {
			t.Errorf("encodeBigEndianN(%#x,%d) = %x, want %x", c.v, c.n, got, c.want)
		}
	}
}

func credsForTest(t *testing.T) *store.Creds {
	t.Helper()
	id, err := keys.NewIdentity()
	if err != nil {
		t.Fatalf("NewIdentity: %v", err)
	}
	return credsFromIdentity(id)
}

func childUInt(t *testing.T, n wire.Node, tag string, size int) uint32 {
	t.Helper()
	ch, ok := childByTag(n, tag)
	if !ok {
		t.Fatalf("missing child %q", tag)
	}
	b := nodeBytes(ch)
	if len(b) != size {
		t.Fatalf("child %q: got %d bytes, want %d", tag, len(b), size)
	}
	var v uint32
	for _, x := range b {
		v = v<<8 | uint32(x)
	}
	return v
}

func TestBuildPreKeyUploadNode(t *testing.T) {
	creds := credsForTest(t)
	preKeys, err := keys.GenPreKeys(1, 5)
	if err != nil {
		t.Fatalf("GenPreKeys: %v", err)
	}

	node := buildPreKeyUploadNode("pk-1", creds, preKeys)

	// iq envelope.
	if node.Tag != "iq" {
		t.Fatalf("tag = %q, want iq", node.Tag)
	}
	for k, want := range map[string]string{"to": sWhatsAppNet, "type": "set", "xmlns": "encrypt", "id": "pk-1"} {
		if got := node.Attrs[k]; got != want {
			t.Errorf("attr %q = %q, want %q", k, got, want)
		}
	}

	// registration: 4 bytes BE == registrationId.
	if got := childUInt(t, node, "registration", 4); got != creds.RegistrationID {
		t.Errorf("registration = %d, want %d", got, creds.RegistrationID)
	}

	// type: single byte 0x05.
	typeNode, ok := childByTag(node, "type")
	if !ok || !bytes.Equal(nodeBytes(typeNode), []byte{5}) {
		t.Fatalf("type child = %x, want [05]", nodeBytes(typeNode))
	}

	// identity: RAW 32 bytes (no 0x05 prefix), equal to creds identity pub.
	idNode, ok := childByTag(node, "identity")
	if !ok {
		t.Fatal("missing identity child")
	}
	idBytes := nodeBytes(idNode)
	if len(idBytes) != 32 {
		t.Fatalf("identity length = %d, want 32 (raw, no 0x05 prefix)", len(idBytes))
	}
	// Note: a raw 32-byte key legitimately starts with 0x05 ~1/256 of the time,
	// so we don't heuristically "detect a 0x05 prefix" — the exact-equality check
	// below fully validates that identity is the raw public key with no prefix.
	if !bytes.Equal(idBytes, creds.IdentityKey.Pub) {
		t.Fatalf("identity bytes = %x, want %x", idBytes, creds.IdentityKey.Pub)
	}

	// list: one <key> per prekey, each with 3-byte id and 32-byte value.
	listNode, ok := childByTag(node, "list")
	if !ok {
		t.Fatal("missing list child")
	}
	keyNodes := childrenByTag(listNode, "key")
	if len(keyNodes) != len(preKeys) {
		t.Fatalf("list has %d keys, want %d", len(keyNodes), len(preKeys))
	}
	for i, kn := range keyNodes {
		gotID := childUInt(t, kn, "id", 3)
		if gotID != preKeys[i].KeyID {
			t.Errorf("key[%d] id = %d, want %d", i, gotID, preKeys[i].KeyID)
		}
		val, _ := childByTag(kn, "value")
		vb := nodeBytes(val)
		if len(vb) != 32 {
			t.Errorf("key[%d] value length = %d, want 32", i, len(vb))
		}
		if !bytes.Equal(vb, preKeys[i].KeyPair.Pub[:]) {
			t.Errorf("key[%d] value mismatch", i)
		}
	}

	// skey: id (3B), value (32B raw == signed pre-key pub), signature (64B).
	skey, ok := childByTag(node, "skey")
	if !ok {
		t.Fatal("missing skey child")
	}
	if got := childUInt(t, skey, "id", 3); got != creds.SignedPreKey.KeyID {
		t.Errorf("skey id = %d, want %d", got, creds.SignedPreKey.KeyID)
	}
	sval, _ := childByTag(skey, "value")
	if svb := nodeBytes(sval); len(svb) != 32 || !bytes.Equal(svb, creds.SignedPreKey.KeyPair.Pub) {
		t.Errorf("skey value mismatch or wrong length (%d)", len(svb))
	}
	ssig, _ := childByTag(skey, "signature")
	if ssb := nodeBytes(ssig); len(ssb) != 64 || !bytes.Equal(ssb, creds.SignedPreKey.Signature) {
		t.Errorf("skey signature mismatch or wrong length (%d)", len(ssb))
	}
}

func TestGenerateAndStorePreKeysPersists(t *testing.T) {
	st, err := store.OpenSQLite(t.TempDir() + "/c.db")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer st.Close()
	c := New(st)
	creds := credsForTest(t)

	preKeys, err := c.generateAndStorePreKeys(creds, 8)
	if err != nil {
		t.Fatalf("generateAndStorePreKeys: %v", err)
	}
	if len(preKeys) != 8 {
		t.Fatalf("got %d prekeys, want 8", len(preKeys))
	}
	// ids are 1..8.
	for i, pk := range preKeys {
		if pk.KeyID != uint32(i+1) {
			t.Errorf("prekey[%d] id = %d, want %d", i, pk.KeyID, i+1)
		}
	}
	// Each persisted prekey loads back with matching pub.
	for _, pk := range preKeys {
		got, ok, err := st.LoadPreKey(pk.KeyID)
		if err != nil || !ok {
			t.Fatalf("LoadPreKey(%d): ok=%v err=%v", pk.KeyID, ok, err)
		}
		if !bytes.Equal(got.Pub, pk.KeyPair.Pub[:]) {
			t.Errorf("LoadPreKey(%d) pub mismatch", pk.KeyID)
		}
	}
	// Signed pre-key persisted.
	if _, ok, _ := st.LoadSignedPreKey(creds.SignedPreKey.KeyID); !ok {
		t.Fatal("signed pre-key not persisted")
	}
}

// TestLoginLoopUploadsPreKeysOnlyOnce is the regression for the live "login
// failure: 401" device-unlink: loginLoop must upload the initial one-time
// pre-key batch on the FIRST login (PreKeysUploaded=false) and persist the flag,
// but must NOT re-upload on a relogin (PreKeysUploaded=true). Re-uploading a
// fresh batch (with the same ids 1..N) on every reconnect floods the server and
// trips anti-abuse. Replenishment afterwards is count-gated via the encrypt
// notification handler, not the login path.
func TestLoginLoopUploadsPreKeysOnlyOnce(t *testing.T) {
	encryptUploads := func(conn *scriptedConn) int {
		n := 0
		for _, w := range conn.written {
			if w.Tag == "iq" && w.Attrs["xmlns"] == "encrypt" && w.Attrs["type"] == "set" {
				n++
			}
		}
		return n
	}
	success := wire.Node{Tag: "success", Attrs: map[string]string{}}

	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	c := New(st)

	creds := credsForTest(t)
	creds.Me = "5550000000@s.whatsapp.net"
	creds.Registered = true
	if err := st.SaveCreds(creds); err != nil {
		t.Fatalf("SaveCreds: %v", err)
	}

	// First login: exactly one upload, flag set and persisted.
	conn := &scriptedConn{inbound: []wire.Node{success}}
	_ = c.loginLoop(context.Background(), conn, creds)
	if got := encryptUploads(conn); got != 1 {
		t.Fatalf("first login: want 1 prekey upload, got %d", got)
	}
	if !creds.PreKeysUploaded {
		t.Fatal("first login: PreKeysUploaded not set on creds")
	}
	reloaded, ok, err := st.LoadCreds()
	if err != nil || !ok {
		t.Fatalf("LoadCreds: ok=%v err=%v", ok, err)
	}
	if !reloaded.PreKeysUploaded {
		t.Fatal("first login: PreKeysUploaded not persisted")
	}

	// Relogin with the persisted (already-uploaded) creds: no upload.
	conn2 := &scriptedConn{inbound: []wire.Node{success}}
	_ = c.loginLoop(context.Background(), conn2, reloaded)
	if got := encryptUploads(conn2); got != 0 {
		t.Fatalf("relogin: want 0 prekey uploads, got %d", got)
	}
}
