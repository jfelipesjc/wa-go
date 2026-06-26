package client

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/jfelipesjc/wa-go/internal/keys"
	"github.com/jfelipesjc/wa-go/internal/signal"
	"github.com/jfelipesjc/wa-go/internal/store"
	"github.com/jfelipesjc/wa-go/internal/waproto"
	"github.com/jfelipesjc/wa-go/internal/wire"
	"google.golang.org/protobuf/proto"
)

// --- golden vector loading (subset; mirrors the signal package test loader) ---

type vHexPair struct {
	Priv string `json:"priv"`
	Pub  string `json:"pub"`
}

type vVectors struct {
	Bob struct {
		IdentityKeyPair vHexPair `json:"identityKeyPair"`
		RegistrationID  uint32   `json:"registrationId"`
		SignedPreKey    struct {
			ID      uint32   `json:"id"`
			KeyPair vHexPair `json:"keyPair"`
		} `json:"signedPreKey"`
		PreKey struct {
			ID      uint32   `json:"id"`
			KeyPair vHexPair `json:"keyPair"`
		} `json:"preKey"`
	} `json:"bob"`
	Alice struct {
		IdentityKeyPair vHexPair `json:"identityKeyPair"`
		RegistrationID  uint32   `json:"registrationId"`
		BaseKey         vHexPair `json:"baseKey"`
	} `json:"alice"`
	EphemeralsGenerated []vHexPair `json:"ephemeralsGenerated"`
	Exchanges           []struct {
		N             int    `json:"n"`
		Dir           string `json:"dir"`
		Type          string `json:"type"`
		CiphertextHex string `json:"ciphertextHex"`
		Plaintext     string `json:"plaintext"`
	} `json:"exchanges"`
}

func loadVVectors(t *testing.T) *vVectors {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "signal", "session_ab.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var v vVectors
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("parse vectors: %v", err)
	}
	return &v
}

func (h vHexPair) keyPair(t *testing.T) keys.KeyPair {
	t.Helper()
	priv, _ := hex.DecodeString(h.Priv)
	pub, _ := hex.DecodeString(h.Pub)
	if len(priv) != 32 || len(pub) != 33 {
		t.Fatalf("bad keypair hex lengths priv=%d pub=%d", len(priv), len(pub))
	}
	var kp keys.KeyPair
	copy(kp.Priv[:], priv)
	copy(kp.Pub[:], pub[1:]) // strip 0x05
	return kp
}

func (h vHexPair) signalPub(t *testing.T) [33]byte {
	t.Helper()
	pub, _ := hex.DecodeString(h.Pub)
	if len(pub) != 33 {
		t.Fatalf("bad pub hex len %d", len(pub))
	}
	var out [33]byte
	copy(out[:], pub)
	return out
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex: %v", err)
	}
	return b
}

// padWAMessage applies WhatsApp's random-max-16 padding: append (pad) bytes whose
// last value equals pad (1..16). We use a fixed pad here for determinism.
func padWAMessage(b []byte, pad int) []byte {
	out := append([]byte(nil), b...)
	for i := 0; i < pad; i++ {
		out = append(out, byte(pad))
	}
	return out
}

// recordConn is a fake nodeConn that records sent nodes.
type recordConn struct {
	sent []wire.Node
}

func (r *recordConn) SendNode(n wire.Node) error { r.sent = append(r.sent, n); return nil }
func (r *recordConn) ReadNode() (wire.Node, error) {
	return wire.Node{}, os.ErrClosed
}
func (r *recordConn) Close() error { return nil }

// TestUnpadMessage covers the WhatsApp unpad: the final byte is the pad count.
func TestUnpadMessage(t *testing.T) {
	for pad := 1; pad <= 16; pad++ {
		body := []byte("payload")
		got, err := unpadMessage(padWAMessage(body, pad))
		if err != nil {
			t.Fatalf("unpad pad=%d: %v", pad, err)
		}
		if string(got) != "payload" {
			t.Fatalf("unpad pad=%d = %q, want payload", pad, got)
		}
	}
	if _, err := unpadMessage(nil); err == nil {
		t.Fatal("unpad(nil) should error")
	}
	// pad byte larger than the buffer is invalid.
	if _, err := unpadMessage([]byte{0x01, 0xff}); err == nil {
		t.Fatal("unpad with pad>len should error")
	}
}

// setupBobStore builds a store pre-loaded with BOB's identity creds, signed
// pre-key and one-time pre-key, plus an established session against ALICE created
// by processing the golden pkmsg. It returns the client, bob creds, the alice
// signal address, and an *initiator* SessionCipher for ALICE positioned to send
// the next "msg" (counter 1 on her eph[1] chain).
func setupBobAndAlice(t *testing.T) (*Client, *store.Creds, string, *signal.SessionCipher) {
	t.Helper()
	v := loadVVectors(t)

	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "bob.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	c := New(st)

	bobIdentity := v.Bob.IdentityKeyPair.keyPair(t)
	bobSignedPre := v.Bob.SignedPreKey.KeyPair.keyPair(t)
	bobPreKey := v.Bob.PreKey.KeyPair.keyPair(t)

	// BOB creds: identity key + signed pre-key (id from vector).
	bobCreds := &store.Creds{
		IdentityKey:    store.CredKeyPair{Priv: bobIdentity.Priv[:], Pub: bobIdentity.Pub[:]},
		RegistrationID: v.Bob.RegistrationID,
		SignedPreKey: store.CredSignedPreKey{
			KeyID:   v.Bob.SignedPreKey.ID,
			KeyPair: store.CredKeyPair{Priv: bobSignedPre.Priv[:], Pub: bobSignedPre.Pub[:]},
		},
		Me:         "5550000000.0@s.whatsapp.net",
		Registered: true,
	}
	// Persist bob's signed pre-key + one-time pre-key so the handler can load them.
	if err := st.StoreSignedPreKey(v.Bob.SignedPreKey.ID, store.StoredKeyPair{Priv: bobSignedPre.Priv[:], Pub: bobSignedPre.Pub[:]}); err != nil {
		t.Fatalf("StoreSignedPreKey: %v", err)
	}
	if err := st.StorePreKeys(map[uint32]store.StoredKeyPair{
		v.Bob.PreKey.ID: {Priv: bobPreKey.Priv[:], Pub: bobPreKey.Pub[:]},
	}); err != nil {
		t.Fatalf("StorePreKeys: %v", err)
	}

	// ALICE address. Use a distinct numeric user; phone device 0.
	aliceJID := "5551111111@s.whatsapp.net"
	aliceAddr, err := signalAddress(aliceJID)
	if err != nil {
		t.Fatalf("signalAddress: %v", err)
	}

	// Establish BOB's session by processing the golden pkmsg (exchange #1).
	// Inject eph[2] as bob's next sending ratchet (matches the signal pkg test).
	eph2 := v.EphemeralsGenerated[2].keyPair(t)
	gen := func() (keys.KeyPair, error) { return eph2, nil }
	ex1 := v.Exchanges[0]
	pt, bobState, err := signal.ProcessPreKeyMessage(
		mustHex(t, ex1.CiphertextHex),
		bobIdentity, bobSignedPre, &bobPreKey, v.Bob.RegistrationID,
		signal.WithRatchetGenerator(gen),
	)
	if err != nil {
		t.Fatalf("ProcessPreKeyMessage: %v", err)
	}
	if string(pt) != ex1.Plaintext {
		t.Fatalf("pkmsg plaintext = %q, want %q", pt, ex1.Plaintext)
	}
	// Persist bob's session under alice's address.
	rec := &signal.SessionRecord{State: bobState}
	blob, err := rec.Marshal()
	if err != nil {
		t.Fatalf("marshal session: %v", err)
	}
	if err := st.StoreSession(aliceAddr, blob); err != nil {
		t.Fatalf("StoreSession: %v", err)
	}

	// Build ALICE's initiator session and advance her cipher past the pkmsg
	// (counter 0) so the NEXT Encrypt produces a "msg" at counter 1 on eph[1] —
	// exactly the chain position bob's receiving session expects.
	aliceIdentity := v.Alice.IdentityKeyPair.keyPair(t)
	aliceBase := v.Alice.BaseKey.keyPair(t)
	aliceRatchet := v.EphemeralsGenerated[1].keyPair(t)
	ap := signal.InitiatorParams{
		LocalIdentity:   aliceIdentity,
		LocalBaseKey:    aliceBase,
		RemoteIdentity:  v.Bob.IdentityKeyPair.signalPub(t),
		RemoteSignedPre: v.Bob.SignedPreKey.KeyPair.signalPub(t),
		RemotePreKey:    v.Bob.PreKey.KeyPair.signalPub(t),
		HasPreKey:       true,
	}
	aliceState, err := signal.InitiateSession(ap, aliceRatchet)
	if err != nil {
		t.Fatalf("InitiateSession: %v", err)
	}
	aliceState.LocalRegID = v.Alice.RegistrationID
	aliceState.PendingActive = false // we send a bare "msg", not a pkmsg wrapper
	aliceCipher := signal.NewSessionCipher(aliceState)
	// Consume counter 0 (the position the golden pkmsg used).
	if _, err := aliceCipher.Encrypt([]byte("counter0 consumed")); err != nil {
		t.Fatalf("alice warm-up encrypt: %v", err)
	}

	return c, bobCreds, aliceJID, aliceCipher
}

// TestHandleMessageDecryptsRealWAProto exercises the full receive path with a
// real WAProto.Message: ALICE encrypts a padded WAProto.Message via the signal
// session, we wrap it in a synthetic <message><enc type=msg> stanza, and the
// handler decrypts + unpads + parses it, emits MessageEvent, and sends receipt+ack.
//
// Why a self-built WAProto.Message and not the golden ciphertext directly: the
// golden vector plaintexts ("ola bob 1") are raw strings, not WAProto.Message
// protobufs, so they cannot be unpadded/parsed as a Message. We therefore build
// a genuine Message, encrypt it through the SAME signal session the vectors
// establish, and feed that into the handler — testing decrypt+unpad+parse end to
// end while reusing the golden-derived session state.
func TestHandleMessageDecryptsRealWAProto(t *testing.T) {
	c, bobCreds, aliceJID, aliceCipher := setupBobAndAlice(t)

	const want = "hello bob, this is a real WAProto message"
	msg := &waproto.Message{Conversation: proto.String(want)}
	raw, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal WAProto.Message: %v", err)
	}
	padded := padWAMessage(raw, 12)
	ct, err := aliceCipher.Encrypt(padded)
	if err != nil {
		t.Fatalf("alice Encrypt: %v", err)
	}
	if ct.Type != "msg" {
		t.Fatalf("ciphertext type = %q, want msg", ct.Type)
	}

	node := wire.Node{
		Tag:   "message",
		Attrs: map[string]string{"from": aliceJID, "id": "MSGID123", "type": "text", "t": "1700000000"},
		Content: []wire.Node{
			{Tag: "enc", Attrs: map[string]string{"v": "2", "type": "msg"}, Content: ct.Serialized},
		},
	}

	rc := &recordConn{}
	if err := c.handleMessage(rc.SendNode, node, bobCreds); err != nil {
		t.Fatalf("handleMessage: %v", err)
	}

	// MessageEvent emitted with the decoded text.
	var got *MessageEvent
	for {
		select {
		case ev := <-c.Events():
			if me, ok := ev.(MessageEvent); ok {
				got = &me
			}
			continue
		default:
		}
		break
	}
	if got == nil {
		t.Fatal("no MessageEvent emitted")
	}
	if got.Text != want {
		t.Fatalf("MessageEvent.Text = %q, want %q", got.Text, want)
	}
	if got.From != aliceJID || got.ID != "MSGID123" {
		t.Fatalf("MessageEvent From/ID = %q/%q", got.From, got.ID)
	}

	// receipt + ack were sent.
	var sawReceipt, sawAck bool
	for _, n := range rc.sent {
		switch n.Tag {
		case "receipt":
			sawReceipt = true
			if n.Attrs["to"] != aliceJID || n.Attrs["id"] != "MSGID123" {
				t.Errorf("receipt attrs wrong: %v", n.Attrs)
			}
		case "ack":
			sawAck = true
			if n.Attrs["class"] != "message" || n.Attrs["id"] != "MSGID123" || n.Attrs["from"] != bobCreds.Me {
				t.Errorf("ack attrs wrong: %v", n.Attrs)
			}
		}
	}
	if !sawReceipt {
		t.Error("no <receipt> sent")
	}
	if !sawAck {
		t.Error("no <ack> sent")
	}
}

// TestHandleMessagePkmsgEstablishesSession exercises the pkmsg branch end to
// end: ALICE builds a fresh initiator session and encrypts a real padded
// WAProto.Message as a PreKeySignalMessage; the handler runs the responder X3DH,
// decrypts, emits the event, persists the session and CONSUMES the one-time
// pre-key (RemovePreKey). A second decrypt of the same prekey id must then fail.
func TestHandleMessagePkmsgEstablishesSession(t *testing.T) {
	v := loadVVectors(t)

	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "bob.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	c := New(st)

	bobIdentity := v.Bob.IdentityKeyPair.keyPair(t)
	bobSignedPre := v.Bob.SignedPreKey.KeyPair.keyPair(t)
	bobPreKey := v.Bob.PreKey.KeyPair.keyPair(t)

	bobCreds := &store.Creds{
		IdentityKey:    store.CredKeyPair{Priv: bobIdentity.Priv[:], Pub: bobIdentity.Pub[:]},
		RegistrationID: v.Bob.RegistrationID,
		SignedPreKey: store.CredSignedPreKey{
			KeyID:   v.Bob.SignedPreKey.ID,
			KeyPair: store.CredKeyPair{Priv: bobSignedPre.Priv[:], Pub: bobSignedPre.Pub[:]},
		},
		Me:         "5550000000@s.whatsapp.net",
		Registered: true,
	}
	if err := st.StoreSignedPreKey(v.Bob.SignedPreKey.ID, store.StoredKeyPair{Priv: bobSignedPre.Priv[:], Pub: bobSignedPre.Pub[:]}); err != nil {
		t.Fatalf("StoreSignedPreKey: %v", err)
	}
	if err := st.StorePreKeys(map[uint32]store.StoredKeyPair{
		v.Bob.PreKey.ID: {Priv: bobPreKey.Priv[:], Pub: bobPreKey.Pub[:]},
	}); err != nil {
		t.Fatalf("StorePreKeys: %v", err)
	}

	// ALICE initiator session that emits a pkmsg (PendingActive true), carrying
	// bob's signed/one-time pre-key ids so the responder loads the right keys.
	aliceIdentity := v.Alice.IdentityKeyPair.keyPair(t)
	aliceState, err := signal.InitiateSession(signal.InitiatorParams{
		LocalIdentity:   aliceIdentity,
		LocalBaseKey:    v.Alice.BaseKey.keyPair(t),
		RemoteIdentity:  v.Bob.IdentityKeyPair.signalPub(t),
		RemoteSignedPre: v.Bob.SignedPreKey.KeyPair.signalPub(t),
		RemotePreKey:    v.Bob.PreKey.KeyPair.signalPub(t),
		HasPreKey:       true,
	}, v.EphemeralsGenerated[1].keyPair(t))
	if err != nil {
		t.Fatalf("InitiateSession: %v", err)
	}
	aliceState.LocalRegID = v.Alice.RegistrationID
	aliceState.PendingActive = true
	aliceState.HasPendingPreKey = true
	aliceState.PendingPreKeyID = v.Bob.PreKey.ID
	aliceState.PendingSignedPreKeyID = v.Bob.SignedPreKey.ID
	aliceCipher := signal.NewSessionCipher(aliceState)

	const want = "first contact via pkmsg"
	raw, _ := proto.Marshal(&waproto.Message{Conversation: proto.String(want)})
	ct, err := aliceCipher.Encrypt(padWAMessage(raw, 7))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if ct.Type != "pkmsg" {
		t.Fatalf("type = %q, want pkmsg", ct.Type)
	}

	aliceJID := "5551111111@s.whatsapp.net"
	node := wire.Node{
		Tag:   "message",
		Attrs: map[string]string{"from": aliceJID, "id": "PK1"},
		Content: []wire.Node{
			{Tag: "enc", Attrs: map[string]string{"type": "pkmsg"}, Content: ct.Serialized},
		},
	}
	if err := c.handleMessage((&recordConn{}).SendNode, node, bobCreds); err != nil {
		t.Fatalf("handleMessage: %v", err)
	}

	var got string
	for {
		select {
		case ev := <-c.Events():
			if me, ok := ev.(MessageEvent); ok {
				got = me.Text
			}
			continue
		default:
		}
		break
	}
	if got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}

	// The one-time pre-key was consumed.
	if _, ok, _ := st.LoadPreKey(v.Bob.PreKey.ID); ok {
		t.Fatal("one-time pre-key was not removed after pkmsg")
	}
	// Session persisted under alice's address.
	addr, _ := signalAddress(aliceJID)
	if _, ok, _ := st.LoadSession(addr); !ok {
		t.Fatal("session not persisted after pkmsg")
	}
	// Peer identity remembered (TOFU).
	if _, ok, _ := st.LoadIdentity(addr); !ok {
		t.Fatal("peer identity not saved after pkmsg")
	}
}

// TestHandleMessageExtendedText covers the extendedTextMessage.text path.
func TestHandleMessageExtendedText(t *testing.T) {
	c, bobCreds, aliceJID, aliceCipher := setupBobAndAlice(t)

	const want = "extended text body"
	msg := &waproto.Message{
		ExtendedTextMessage: &waproto.ExtendedTextMessage{Text: proto.String(want)},
	}
	raw, _ := proto.Marshal(msg)
	ct, err := aliceCipher.Encrypt(padWAMessage(raw, 3))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	node := wire.Node{
		Tag:   "message",
		Attrs: map[string]string{"from": aliceJID, "id": "X1"},
		Content: []wire.Node{
			{Tag: "enc", Attrs: map[string]string{"type": "msg"}, Content: ct.Serialized},
		},
	}
	if err := c.handleMessage((&recordConn{}).SendNode, node, bobCreds); err != nil {
		t.Fatalf("handleMessage: %v", err)
	}
	for {
		select {
		case ev := <-c.Events():
			if me, ok := ev.(MessageEvent); ok {
				if me.Text != want {
					t.Fatalf("text = %q, want %q", me.Text, want)
				}
				return
			}
			continue
		default:
		}
		break
	}
	t.Fatal("no MessageEvent with extended text")
}

// TestHandleMessagePkmsgBurstReusesSession is the regression for the live
// "one-time pre-key N not found (used already?)" seen during history sync: a
// peer keeps wrapping messages as pkmsg (same baseKey + one-time prekey) until
// it sees our first reply, so a burst arrives as several pkmsg referencing the
// SAME prekey. The first consumes+removes it; the rest must still decrypt by
// reusing the established session instead of failing on the missing prekey.
func TestHandleMessagePkmsgBurstReusesSession(t *testing.T) {
	v := loadVVectors(t)
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "bob.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	c := New(st)

	bobIdentity := v.Bob.IdentityKeyPair.keyPair(t)
	bobSignedPre := v.Bob.SignedPreKey.KeyPair.keyPair(t)
	bobPreKey := v.Bob.PreKey.KeyPair.keyPair(t)
	bobCreds := &store.Creds{
		IdentityKey:    store.CredKeyPair{Priv: bobIdentity.Priv[:], Pub: bobIdentity.Pub[:]},
		RegistrationID: v.Bob.RegistrationID,
		SignedPreKey: store.CredSignedPreKey{
			KeyID:   v.Bob.SignedPreKey.ID,
			KeyPair: store.CredKeyPair{Priv: bobSignedPre.Priv[:], Pub: bobSignedPre.Pub[:]},
		},
		Me: "5550000000@s.whatsapp.net", Registered: true,
	}
	if err := st.StoreSignedPreKey(v.Bob.SignedPreKey.ID, store.StoredKeyPair{Priv: bobSignedPre.Priv[:], Pub: bobSignedPre.Pub[:]}); err != nil {
		t.Fatalf("StoreSignedPreKey: %v", err)
	}
	if err := st.StorePreKeys(map[uint32]store.StoredKeyPair{
		v.Bob.PreKey.ID: {Priv: bobPreKey.Priv[:], Pub: bobPreKey.Pub[:]},
	}); err != nil {
		t.Fatalf("StorePreKeys: %v", err)
	}

	aliceIdentity := v.Alice.IdentityKeyPair.keyPair(t)
	aliceState, err := signal.InitiateSession(signal.InitiatorParams{
		LocalIdentity:   aliceIdentity,
		LocalBaseKey:    v.Alice.BaseKey.keyPair(t),
		RemoteIdentity:  v.Bob.IdentityKeyPair.signalPub(t),
		RemoteSignedPre: v.Bob.SignedPreKey.KeyPair.signalPub(t),
		RemotePreKey:    v.Bob.PreKey.KeyPair.signalPub(t),
		HasPreKey:       true,
	}, v.EphemeralsGenerated[1].keyPair(t))
	if err != nil {
		t.Fatalf("InitiateSession: %v", err)
	}
	aliceState.LocalRegID = v.Alice.RegistrationID
	aliceState.PendingActive = true
	aliceState.HasPendingPreKey = true
	aliceState.PendingPreKeyID = v.Bob.PreKey.ID
	aliceState.PendingSignedPreKeyID = v.Bob.SignedPreKey.ID
	aliceCipher := signal.NewSessionCipher(aliceState)
	aliceJID := "5551111111@s.whatsapp.net"

	collectText := func() string {
		var got string
		for {
			select {
			case ev := <-c.Events():
				if me, ok := ev.(MessageEvent); ok {
					got = me.Text
				}
				continue
			default:
			}
			return got
		}
	}

	for i, want := range []string{"burst msg 1", "burst msg 2", "burst msg 3"} {
		raw, _ := proto.Marshal(&waproto.Message{Conversation: proto.String(want)})
		ct, err := aliceCipher.Encrypt(padWAMessage(raw, 7))
		if err != nil {
			t.Fatalf("Encrypt #%d: %v", i+1, err)
		}
		// Every message is still a pkmsg (alice hasn't seen a reply), all
		// referencing the same one-time prekey — removed after the first.
		if ct.Type != "pkmsg" {
			t.Fatalf("msg #%d type = %q, want pkmsg", i+1, ct.Type)
		}
		node := wire.Node{
			Tag:   "message",
			Attrs: map[string]string{"from": aliceJID, "id": fmt.Sprintf("BURST%d", i+1)},
			Content: []wire.Node{
				{Tag: "enc", Attrs: map[string]string{"type": "pkmsg"}, Content: ct.Serialized},
			},
		}
		if err := c.handleMessage((&recordConn{}).SendNode, node, bobCreds); err != nil {
			t.Fatalf("handleMessage #%d (prekey consumed=%v): %v", i+1, i > 0, err)
		}
		if got := collectText(); got != want {
			t.Fatalf("msg #%d text = %q, want %q", i+1, got, want)
		}
	}

	// The prekey was consumed by the first message and never resurrected.
	if _, ok, _ := st.LoadPreKey(v.Bob.PreKey.ID); ok {
		t.Fatal("one-time pre-key should stay removed after the burst")
	}
}
