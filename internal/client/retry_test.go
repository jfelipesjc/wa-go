package client

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jfelipesjc/wa-go/internal/keys"
	"github.com/jfelipesjc/wa-go/internal/signal"
	"github.com/jfelipesjc/wa-go/internal/store"
	"github.com/jfelipesjc/wa-go/internal/waproto"
	"github.com/jfelipesjc/wa-go/internal/wire"
	"google.golang.org/protobuf/proto"
)

// newTestClient builds a Client over a fresh SQLite store for offline tests.
func newTestClient(t *testing.T) *Client {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st)
}

// --- buildRetryReceipt: count=1 (no keys) ---

func TestBuildRetryReceiptCount1NoKeys(t *testing.T) {
	c := newTestClient(t)
	creds := credsForTest(t)

	inMsg := wire.Node{
		Tag: "message",
		Attrs: map[string]string{
			"from": "5551111111@s.whatsapp.net",
			"id":   "MSG-1",
			"t":    "1700000000",
		},
	}
	receipt, minted, err := c.buildRetryReceipt(inMsg, creds, 1)
	if err != nil {
		t.Fatalf("buildRetryReceipt: %v", err)
	}
	if minted != nil {
		t.Fatalf("count=1 should not mint a pre-key, got %+v", minted)
	}
	if receipt.Tag != "receipt" {
		t.Fatalf("tag = %q, want receipt", receipt.Tag)
	}
	for k, want := range map[string]string{"id": "MSG-1", "type": "retry", "to": "5551111111@s.whatsapp.net"} {
		if receipt.Attrs[k] != want {
			t.Errorf("attr %q = %q, want %q", k, receipt.Attrs[k], want)
		}
	}
	retry, ok := childByTag(receipt, "retry")
	if !ok {
		t.Fatal("missing <retry>")
	}
	for k, want := range map[string]string{"count": "1", "id": "MSG-1", "t": "1700000000", "v": "1"} {
		if retry.Attrs[k] != want {
			t.Errorf("retry attr %q = %q, want %q", k, retry.Attrs[k], want)
		}
	}
	// registration: 4 bytes BE == regId.
	if got := childUInt(t, receipt, "registration", 4); got != creds.RegistrationID {
		t.Errorf("registration = %d, want %d", got, creds.RegistrationID)
	}
	// No <keys> at count 1.
	if _, ok := childByTag(receipt, "keys"); ok {
		t.Error("count=1 receipt should not carry <keys>")
	}
}

// --- buildRetryReceipt: count=2 (with <registration> + <keys> bundle) ---

func TestBuildRetryReceiptCount2WithKeys(t *testing.T) {
	c := newTestClient(t)
	creds := credsForTest(t)
	creds.Account = []byte("fake-account-identity")

	inMsg := wire.Node{
		Tag: "message",
		Attrs: map[string]string{
			"from":        "5551111111@s.whatsapp.net",
			"id":          "MSG-2",
			"t":           "1700000001",
			"participant": "5552222222@s.whatsapp.net",
		},
	}
	receipt, minted, err := c.buildRetryReceipt(inMsg, creds, 2)
	if err != nil {
		t.Fatalf("buildRetryReceipt: %v", err)
	}
	if minted == nil {
		t.Fatal("count=2 should mint a pre-key")
	}
	if receipt.Attrs["participant"] != "5552222222@s.whatsapp.net" {
		t.Errorf("participant attr = %q", receipt.Attrs["participant"])
	}
	retry, _ := childByTag(receipt, "retry")
	if retry.Attrs["count"] != "2" {
		t.Errorf("retry count = %q, want 2", retry.Attrs["count"])
	}

	keysNode, ok := childByTag(receipt, "keys")
	if !ok {
		t.Fatal("count=2 receipt missing <keys>")
	}
	// type: 0x05.
	typeNode, ok := childByTag(keysNode, "type")
	if !ok || !bytes.Equal(nodeBytes(typeNode), []byte{5}) {
		t.Fatalf("keys type = %x, want [05]", nodeBytes(typeNode))
	}
	// identity: raw 32 bytes == our identity pub.
	idNode, ok := childByTag(keysNode, "identity")
	if !ok || !bytes.Equal(nodeBytes(idNode), creds.IdentityKey.Pub) {
		t.Fatalf("keys identity mismatch")
	}
	// one-time pre-key node with the minted id + pub.
	keyNode, ok := childByTag(keysNode, "key")
	if !ok {
		t.Fatal("keys missing <key> one-time pre-key")
	}
	if got := childUInt(t, keyNode, "id", 3); got != minted.KeyID {
		t.Errorf("key id = %d, want minted %d", got, minted.KeyID)
	}
	kval, _ := childByTag(keyNode, "value")
	if !bytes.Equal(nodeBytes(kval), minted.KeyPair.Pub[:]) {
		t.Error("key value != minted pre-key pub")
	}
	// signed pre-key.
	skey, ok := childByTag(keysNode, "skey")
	if !ok {
		t.Fatal("keys missing <skey>")
	}
	if got := childUInt(t, skey, "id", 3); got != creds.SignedPreKey.KeyID {
		t.Errorf("skey id = %d, want %d", got, creds.SignedPreKey.KeyID)
	}
	sval, _ := childByTag(skey, "value")
	if !bytes.Equal(nodeBytes(sval), creds.SignedPreKey.KeyPair.Pub) {
		t.Error("skey value mismatch")
	}
	// device-identity == account.
	devID, ok := childByTag(keysNode, "device-identity")
	if !ok || !bytes.Equal(nodeBytes(devID), creds.Account) {
		t.Error("device-identity != account")
	}

	// The minted pre-key must be persisted so the remote can consume it.
	got, ok, err := c.store.LoadPreKey(minted.KeyID)
	if err != nil || !ok {
		t.Fatalf("minted pre-key not persisted: ok=%v err=%v", ok, err)
	}
	if !bytes.Equal(got.Pub, minted.KeyPair.Pub[:]) {
		t.Error("persisted pre-key pub mismatch")
	}
	// And its id is from the high retry range (no collision with 1..30).
	if minted.KeyID <= initialPreKeyCount {
		t.Errorf("minted pre-key id %d should be in the high retry range", minted.KeyID)
	}
}

// --- retry counter increments per msgId and stops at the cap ---

func TestNextRetryCountCap(t *testing.T) {
	c := newTestClient(t)

	for want := 1; want <= maxMsgRetryCount; want++ {
		got, ok := c.nextRetryCount("M")
		if !ok || got != want {
			t.Fatalf("attempt %d: got (%d,%v), want (%d,true)", want, got, ok, want)
		}
	}
	// Cap reached: further calls give up.
	if got, ok := c.nextRetryCount("M"); ok {
		t.Fatalf("past cap should give up, got (%d,%v)", got, ok)
	}
	// A different msgId has its own independent counter.
	if got, ok := c.nextRetryCount("OTHER"); !ok || got != 1 {
		t.Fatalf("independent counter: got (%d,%v), want (1,true)", got, ok)
	}
}

// --- send cache: populate + lookup + eviction at the cap ---

func TestSentCacheEviction(t *testing.T) {
	c := newTestClient(t)
	c.sentCap = 3

	mk := func(s string) *waproto.Message {
		return &waproto.Message{Conversation: proto.String(s)}
	}
	c.rememberSent("a", "ja@s.whatsapp.net", mk("a"))
	c.rememberSent("b", "jb@s.whatsapp.net", mk("b"))
	c.rememberSent("c", "jc@s.whatsapp.net", mk("c"))

	if got, ok := c.lookupSent("a"); !ok || got.toJID != "ja@s.whatsapp.net" || got.msg.GetConversation() != "a" {
		t.Fatalf("lookup a: ok=%v got=%+v", ok, got)
	}

	// Overflow: "a" (oldest) is evicted.
	c.rememberSent("d", "jd@s.whatsapp.net", mk("d"))
	if _, ok := c.lookupSent("a"); ok {
		t.Error("oldest entry 'a' should have been evicted")
	}
	for _, id := range []string{"b", "c", "d"} {
		if _, ok := c.lookupSent(id); !ok {
			t.Errorf("entry %q should still be cached", id)
		}
	}

	// Re-inserting an existing id updates in place without growing the order list.
	c.rememberSent("d", "jd2@s.whatsapp.net", mk("d2"))
	if got, _ := c.lookupSent("d"); got.toJID != "jd2@s.whatsapp.net" {
		t.Errorf("update in place failed: %+v", got)
	}
	if _, ok := c.lookupSent("b"); !ok {
		t.Error("updating 'd' should not have evicted 'b'")
	}
}

// --- sendMessage populates the cache (full offline send path) ---

func TestSendMessagePopulatesSentCache(t *testing.T) {
	v := loadVVectors(t)

	aliceIdentity := v.Alice.IdentityKeyPair.keyPair(t)
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "alice.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	c := New(st)
	creds := &store.Creds{
		IdentityKey:    store.CredKeyPair{Priv: aliceIdentity.Priv[:], Pub: aliceIdentity.Pub[:]},
		RegistrationID: v.Alice.RegistrationID,
		Me:             "5550000000@s.whatsapp.net",
		Registered:     true,
	}

	bobIdentity := v.Bob.IdentityKeyPair.keyPair(t)
	bobSignedPre := v.Bob.SignedPreKey.KeyPair.keyPair(t)
	bobPreKey := v.Bob.PreKey.KeyPair.keyPair(t)
	bobJID := "5551111111@s.whatsapp.net"
	spkSig, err := keys.Sign(bobIdentity.Priv, signalPubKey33(bobSignedPre.Pub))
	if err != nil {
		t.Fatalf("sign spk: %v", err)
	}

	conn := newGatedConn()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	loopDone := make(chan struct{})
	go func() { _ = c.loginLoop(ctx, conn, creds); close(loopDone) }()
	_ = waitActive(t, c)

	go func() {
		for {
			n, ok := conn.nextWritten(ctx)
			if !ok {
				return
			}
			switch n.Attrs["xmlns"] {
			case "usync":
				conn.feed(usyncResultFor(n.Attrs["id"], bobJID))
			case "encrypt":
				conn.feed(preKeyResultFor(n.Attrs["id"], bobJID, bobIdentity.Pub, v.Bob.SignedPreKey.ID, bobSignedPre.Pub, spkSig, v.Bob.PreKey.ID, bobPreKey.Pub, v.Bob.RegistrationID))
			}
		}
	}()

	msgID, err := c.SendText(ctx, bobJID, "cache me")
	if err != nil {
		t.Fatalf("SendText: %v", err)
	}
	got, ok := c.lookupSent(msgID)
	if !ok {
		t.Fatalf("sent cache not populated for %q", msgID)
	}
	if got.toJID != bobJID || got.msg.GetConversation() != "cache me" {
		t.Fatalf("cached entry wrong: %+v", got)
	}

	cancel()
	conn.close()
	<-loopDone
}

// --- extractRetryBundle round-trips a <keys> bundle ---

func TestExtractRetryBundle(t *testing.T) {
	creds := credsForTest(t)
	creds.Account = []byte("acct")
	c := newTestClient(t)

	inMsg := wire.Node{Tag: "message", Attrs: map[string]string{"from": "x@s.whatsapp.net", "id": "M", "t": "1"}}
	receipt, minted, err := c.buildRetryReceipt(inMsg, creds, 2)
	if err != nil {
		t.Fatalf("buildRetryReceipt: %v", err)
	}

	b, ok := extractRetryBundle(receipt, "5551111111@s.whatsapp.net")
	if !ok {
		t.Fatal("extractRetryBundle returned ok=false on a valid bundle")
	}
	if b.JID != "5551111111@s.whatsapp.net" {
		t.Errorf("bundle JID = %q", b.JID)
	}
	if b.IdentityKey != to32(creds.IdentityKey.Pub) {
		t.Error("bundle identity mismatch")
	}
	if b.RegistrationID != creds.RegistrationID {
		t.Errorf("bundle regID = %d, want %d", b.RegistrationID, creds.RegistrationID)
	}
	if b.SignedPreKeyID != creds.SignedPreKey.KeyID {
		t.Errorf("bundle skey id = %d", b.SignedPreKeyID)
	}
	if !b.HasPreKey || b.PreKeyID != minted.KeyID {
		t.Errorf("bundle prekey: has=%v id=%d, want minted %d", b.HasPreKey, b.PreKeyID, minted.KeyID)
	}
	if b.PreKeyPub != minted.KeyPair.Pub {
		t.Error("bundle prekey pub mismatch")
	}

	// No <keys> => ok=false.
	bare := wire.Node{Tag: "receipt", Attrs: map[string]string{"type": "retry"}}
	if _, ok := extractRetryBundle(bare, "x@s.whatsapp.net"); ok {
		t.Error("extractRetryBundle should be false with no <keys>")
	}
}

func to32(b []byte) [32]byte {
	var out [32]byte
	copy(out[:], b)
	return out
}

// --- handleRetryReceipt: re-encrypts and resends from cache (with bundle) ---

func TestHandleRetryReceiptResendsFromCache(t *testing.T) {
	v := loadVVectors(t)

	// Our device = alice (the original sender). We cache an outgoing message to bob
	// then receive a retry receipt from bob carrying a fresh key bundle.
	aliceIdentity := v.Alice.IdentityKeyPair.keyPair(t)
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "alice.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	c := New(st)
	creds := &store.Creds{
		IdentityKey:    store.CredKeyPair{Priv: aliceIdentity.Priv[:], Pub: aliceIdentity.Pub[:]},
		RegistrationID: v.Alice.RegistrationID,
		Me:             "5550000000@s.whatsapp.net",
		Registered:     true,
	}

	bobIdentity := v.Bob.IdentityKeyPair.keyPair(t)
	bobSignedPre := v.Bob.SignedPreKey.KeyPair.keyPair(t)
	bobPreKey := v.Bob.PreKey.KeyPair.keyPair(t)
	bobJID := "5551111111@s.whatsapp.net"
	spkSig, err := keys.Sign(bobIdentity.Priv, signalPubKey33(bobSignedPre.Pub))
	if err != nil {
		t.Fatalf("sign spk: %v", err)
	}

	// Cache an outgoing message under a known msgId.
	const msgID = "3EB0RESEND"
	const wantText = "please resend me"
	c.rememberSent(msgID, bobJID, &waproto.Message{Conversation: proto.String(wantText)})

	// Build the retry receipt bob would send: type=retry, from=bob, with a <keys>
	// bundle (bob's identity + signed prekey + one-time prekey + registration).
	receipt := wire.Node{
		Tag: "receipt",
		Attrs: map[string]string{
			"id":   msgID,
			"type": "retry",
			"from": bobJID,
		},
		Content: []wire.Node{
			{Tag: "retry", Attrs: map[string]string{"count": "2", "id": msgID, "t": "1", "v": "1"}},
			{Tag: "registration", Attrs: map[string]string{}, Content: encodeBigEndianN(v.Bob.RegistrationID, 4)},
			{Tag: "keys", Attrs: map[string]string{}, Content: []wire.Node{
				{Tag: "type", Attrs: map[string]string{}, Content: []byte{5}},
				{Tag: "identity", Attrs: map[string]string{}, Content: append([]byte(nil), bobIdentity.Pub[:]...)},
				{Tag: "key", Attrs: map[string]string{}, Content: []wire.Node{
					{Tag: "id", Attrs: map[string]string{}, Content: encodeBigEndianN(v.Bob.PreKey.ID, 3)},
					{Tag: "value", Attrs: map[string]string{}, Content: append([]byte(nil), bobPreKey.Pub[:]...)},
				}},
				{Tag: "skey", Attrs: map[string]string{}, Content: []wire.Node{
					{Tag: "id", Attrs: map[string]string{}, Content: encodeBigEndianN(v.Bob.SignedPreKey.ID, 3)},
					{Tag: "value", Attrs: map[string]string{}, Content: append([]byte(nil), bobSignedPre.Pub[:]...)},
					{Tag: "signature", Attrs: map[string]string{}, Content: spkSig[:]},
				}},
			}},
		},
	}

	rc := &recordConn{}
	sess := &session{send: rc.SendNode, creds: creds}
	if err := c.handleRetryReceipt(context.Background(), sess, receipt); err != nil {
		t.Fatalf("handleRetryReceipt: %v", err)
	}

	// A <message> targeting bob must have been sent, reusing the same msgId.
	var stanza *wire.Node
	for i := range rc.sent {
		if rc.sent[i].Tag == "message" {
			stanza = &rc.sent[i]
			break
		}
	}
	if stanza == nil {
		t.Fatal("no <message> resent")
	}
	if stanza.Attrs["to"] != bobJID || stanza.Attrs["id"] != msgID {
		t.Fatalf("resent message attrs wrong: %+v", stanza.Attrs)
	}
	parts, _ := childByTag(*stanza, "participants")
	tos := childrenByTag(parts, "to")
	if len(tos) != 1 || tos[0].Attrs["jid"] != bobJID {
		t.Fatalf("resend target wrong: %+v", tos)
	}
	enc, _ := childByTag(tos[0], "enc")
	if enc.Attrs["type"] != "pkmsg" {
		t.Fatalf("resend enc type = %q, want pkmsg (fresh session from bundle)", enc.Attrs["type"])
	}

	// Decrypt as bob to confirm the resent content matches the cached message.
	padded, _, err := signal.ProcessPreKeyMessage(nodeBytes(enc), bobIdentity, bobSignedPre, &bobPreKey, v.Bob.RegistrationID)
	if err != nil {
		t.Fatalf("bob decrypt resend: %v", err)
	}
	plaintext, err := unpadMessage(padded)
	if err != nil {
		t.Fatalf("unpad: %v", err)
	}
	var m waproto.Message
	if err := proto.Unmarshal(plaintext, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m.GetConversation() != wantText {
		t.Fatalf("resent text = %q, want %q", m.GetConversation(), wantText)
	}
}

// --- handleRetryReceipt: no cached message => no-op ---

func TestHandleRetryReceiptNoCacheNoOp(t *testing.T) {
	c := newTestClient(t)
	creds := credsForTest(t)
	creds.Me = "5550000000@s.whatsapp.net"
	rc := &recordConn{}
	sess := &session{send: rc.SendNode, creds: creds}

	receipt := wire.Node{
		Tag:   "receipt",
		Attrs: map[string]string{"id": "UNKNOWN", "type": "retry", "from": "5551111111@s.whatsapp.net"},
		Content: []wire.Node{
			{Tag: "retry", Attrs: map[string]string{"count": "1", "id": "UNKNOWN", "t": "1", "v": "1"}},
		},
	}
	if err := c.handleRetryReceipt(context.Background(), sess, receipt); err != nil {
		t.Fatalf("handleRetryReceipt no-cache: %v", err)
	}
	for _, n := range rc.sent {
		if n.Tag == "message" {
			t.Fatal("no-cache retry should not resend a <message>")
		}
	}
}

// --- handleMessage sends a retry receipt when decrypt fails ---

func TestHandleMessageRetryOnDecryptFailure(t *testing.T) {
	c, bobCreds, aliceJID, _ := setupBobAndAlice(t)

	// A bogus "msg" ciphertext against alice's session: there IS a session (set up
	// by setupBobAndAlice) but the ciphertext is garbage, so Decrypt fails.
	node := wire.Node{
		Tag:   "message",
		Attrs: map[string]string{"from": aliceJID, "id": "BAD-1", "t": "1700000000"},
		Content: []wire.Node{
			{Tag: "enc", Attrs: map[string]string{"type": "msg"}, Content: []byte("not a real ciphertext")},
		},
	}
	rc := &recordConn{}
	_ = c.handleMessage(rc.SendNode, node, bobCreds)

	// Expect a <receipt type=retry> for BAD-1 addressed to alice, and NO plain
	// delivery <receipt> (which would falsely confirm receipt).
	var retry *wire.Node
	for i := range rc.sent {
		n := rc.sent[i]
		if n.Tag == "receipt" && n.Attrs["type"] == "retry" {
			retry = &rc.sent[i]
		}
		if n.Tag == "receipt" && n.Attrs["type"] == "" {
			t.Fatal("plain delivery receipt should not be sent on decrypt failure")
		}
	}
	if retry == nil {
		t.Fatal("no retry receipt sent on decrypt failure")
	}
	if retry.Attrs["id"] != "BAD-1" || retry.Attrs["to"] != aliceJID {
		t.Fatalf("retry receipt attrs wrong: %+v", retry.Attrs)
	}
	rn, ok := childByTag(*retry, "retry")
	if !ok || rn.Attrs["count"] != "1" {
		t.Fatalf("retry child wrong: %+v", rn.Attrs)
	}
	// A message <ack> is still sent (so the server stops queuing).
	var acked bool
	for _, n := range rc.sent {
		if n.Tag == "ack" && n.Attrs["class"] == "message" {
			acked = true
		}
	}
	if !acked {
		t.Error("message ack should still be sent on decrypt failure")
	}
}
