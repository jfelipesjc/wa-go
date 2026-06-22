package client

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/felipeleal/wa-go/internal/keys"
	"github.com/felipeleal/wa-go/internal/signal"
	"github.com/felipeleal/wa-go/internal/store"
	"github.com/felipeleal/wa-go/internal/waproto"
	"github.com/felipeleal/wa-go/internal/wire"
	"google.golang.org/protobuf/proto"
)

// --- Unit: node-construction tests (usync + prekey fetch) ---

func TestUsyncDeviceQueryNode(t *testing.T) {
	n := usyncDeviceQueryNode("id-1", "sid-1", []string{"5551111111@s.whatsapp.net"})
	if n.Tag != "iq" || n.Attrs["xmlns"] != "usync" || n.Attrs["type"] != "get" || n.Attrs["to"] != sWhatsAppNet {
		t.Fatalf("iq attrs wrong: %+v", n.Attrs)
	}
	if n.Attrs["id"] != "id-1" {
		t.Fatalf("iq id = %q", n.Attrs["id"])
	}
	usync, ok := childByTag(n, "usync")
	if !ok {
		t.Fatal("missing <usync>")
	}
	for k, want := range map[string]string{"context": "message", "mode": "query", "sid": "sid-1", "last": "true", "index": "0"} {
		if usync.Attrs[k] != want {
			t.Fatalf("usync attr %s = %q, want %q", k, usync.Attrs[k], want)
		}
	}
	query, ok := childByTag(usync, "query")
	if !ok {
		t.Fatal("missing <query>")
	}
	dev, ok := childByTag(query, "devices")
	if !ok || dev.Attrs["version"] != "2" {
		t.Fatalf("missing <devices version=2>: %+v", dev)
	}
	list, ok := childByTag(usync, "list")
	if !ok {
		t.Fatal("missing <list>")
	}
	users := childrenByTag(list, "user")
	if len(users) != 1 || users[0].Attrs["jid"] != "5551111111@s.whatsapp.net" {
		t.Fatalf("user list wrong: %+v", users)
	}
}

func TestPreKeyFetchNode(t *testing.T) {
	n := preKeyFetchNode("id-9", []string{"a@s.whatsapp.net", "b:2@s.whatsapp.net"})
	if n.Tag != "iq" || n.Attrs["xmlns"] != "encrypt" || n.Attrs["type"] != "get" {
		t.Fatalf("iq attrs wrong: %+v", n.Attrs)
	}
	key, ok := childByTag(n, "key")
	if !ok {
		t.Fatal("missing <key>")
	}
	users := childrenByTag(key, "user")
	if len(users) != 2 || users[0].Attrs["jid"] != "a@s.whatsapp.net" || users[1].Attrs["jid"] != "b:2@s.whatsapp.net" {
		t.Fatalf("user list wrong: %+v", users)
	}
}

func TestParseUSyncDevices(t *testing.T) {
	reply := wire.Node{
		Tag:   "iq",
		Attrs: map[string]string{"type": "result", "id": "x"},
		Content: []wire.Node{{
			Tag: "usync", Attrs: map[string]string{},
			Content: []wire.Node{{
				Tag: "list", Attrs: map[string]string{},
				Content: []wire.Node{{
					Tag:   "user",
					Attrs: map[string]string{"jid": "5551111111@s.whatsapp.net"},
					Content: []wire.Node{{
						Tag: "devices", Attrs: map[string]string{},
						Content: []wire.Node{{
							Tag: "device-list", Attrs: map[string]string{},
							Content: []wire.Node{
								{Tag: "device", Attrs: map[string]string{"id": "0"}},
								{Tag: "device", Attrs: map[string]string{"id": "12", "key-index": "1"}},
								{Tag: "device", Attrs: map[string]string{"id": "99"}}, // no key-index, non-zero -> dropped
							},
						}},
					}},
				}},
			}},
		}},
	}
	devs, err := parseUSyncDevices(reply)
	if err != nil {
		t.Fatalf("parseUSyncDevices: %v", err)
	}
	if len(devs) != 2 {
		t.Fatalf("got %d devices, want 2: %+v", len(devs), devs)
	}
	if devs[0].JID != "5551111111@s.whatsapp.net" || devs[0].Device != 0 {
		t.Fatalf("device0 wrong: %+v", devs[0])
	}
	if devs[1].JID != "5551111111:12@s.whatsapp.net" || devs[1].Device != 12 {
		t.Fatalf("device12 wrong: %+v", devs[1])
	}
}

func TestPadRandomMax16RoundTrip(t *testing.T) {
	for i := 0; i < 200; i++ {
		body := []byte("hello world payload")
		padded, err := padRandomMax16(body)
		if err != nil {
			t.Fatalf("pad: %v", err)
		}
		extra := len(padded) - len(body)
		if extra < 1 || extra > 16 {
			t.Fatalf("pad length %d out of [1,16]", extra)
		}
		if int(padded[len(padded)-1]) != extra {
			t.Fatalf("last byte %d != pad length %d", padded[len(padded)-1], extra)
		}
		got, err := unpadMessage(padded)
		if err != nil {
			t.Fatalf("unpad: %v", err)
		}
		if string(got) != string(body) {
			t.Fatalf("round-trip = %q, want %q", got, body)
		}
	}
}

// --- Unit: iq request/response registry over a scripted loop ---

// TestIQRegistryRequestResponse drives loginLoop over a scripted conn whose only
// inbound node (after <success>) is the <iq type=result> reply to a usync request
// issued concurrently via sendIQ. It verifies the reply is routed back.
func TestIQRegistryRequestResponse(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	c := New(st)

	creds := &store.Creds{Me: "5550000000@s.whatsapp.net", Registered: true}

	conn := newGatedConn()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	loopDone := make(chan struct{})
	go func() {
		_ = c.loginLoop(ctx, conn, creds)
		close(loopDone)
	}()

	// Wait until the active session is published, then fire an iq request.
	sess := waitActive(t, c)

	gotID := make(chan string, 1)
	go func() {
		reply, err := c.sendIQ(ctx, sess, wire.Node{
			Tag:   "iq",
			Attrs: map[string]string{"to": sWhatsAppNet, "type": "get", "xmlns": "usync", "id": "req-1"},
		})
		if err != nil {
			gotID <- "ERR:" + err.Error()
			return
		}
		gotID <- reply.Attrs["custom"]
	}()

	// The request was written to the conn; feed back the matching result.
	waitForWritten(t, conn, "req-1")
	conn.feed(wire.Node{Tag: "iq", Attrs: map[string]string{"type": "result", "id": "req-1", "custom": "PONG"}})

	select {
	case v := <-gotID:
		if v != "PONG" {
			t.Fatalf("iq reply routed wrong: %q", v)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for iq reply")
	}

	cancel()
	conn.close()
	<-loopDone
}

// --- Integration (offline): SendText produces a decryptable pkmsg ---

// TestSendText_ProducesDecryptablePkmsg exercises the full send path offline:
// our device (alice keys) sends "ola mundo" to bob. A scripted conn answers the
// usync + prekey-bundle iqs with a synthetic bundle built from bob's golden
// keys, then captures the <message>. We extract the <enc type=pkmsg> and decrypt
// it with bob's responder logic, recovering the WAProto.Message text.
func TestSendText_ProducesDecryptablePkmsg(t *testing.T) {
	v := loadVVectors(t)

	// Our device = alice. Persist alice identity as creds.
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

	// Bob (recipient) golden keys.
	bobIdentity := v.Bob.IdentityKeyPair.keyPair(t)
	bobSignedPre := v.Bob.SignedPreKey.KeyPair.keyPair(t)
	bobPreKey := v.Bob.PreKey.KeyPair.keyPair(t)
	bobJID := "5551111111@s.whatsapp.net"

	// Re-sign bob's signed pre-key with bob's identity so the bundle passes
	// signature verification in fetchPreKeyBundles (the golden signature is over a
	// different identity context; we sign fresh here).
	signedMsg := signalPubKey33(bobSignedPre.Pub)
	spkSig, err := keys.Sign(bobIdentity.Priv, signedMsg)
	if err != nil {
		t.Fatalf("sign spk: %v", err)
	}

	conn := newGatedConn()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	loopDone := make(chan struct{})
	go func() {
		_ = c.loginLoop(ctx, conn, creds)
		close(loopDone)
	}()
	_ = waitActive(t, c)

	// Responder goroutine: answer iqs as they are written.
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

	wantText := "ola mundo"
	msgID, err := c.SendText(ctx, bobJID, wantText)
	if err != nil {
		t.Fatalf("SendText: %v", err)
	}
	if len(msgID) < 4 || msgID[:4] != "3EB0" {
		t.Fatalf("unexpected message id %q", msgID)
	}

	// Grab the <message> stanza that was written.
	stanza := waitForTag(t, conn, "message")
	if stanza.Attrs["to"] != bobJID || stanza.Attrs["type"] != "text" || stanza.Attrs["id"] != msgID {
		t.Fatalf("message attrs wrong: %+v", stanza.Attrs)
	}
	participants, ok := childByTag(stanza, "participants")
	if !ok {
		t.Fatal("message missing <participants>")
	}
	tos := childrenByTag(participants, "to")
	if len(tos) != 1 {
		t.Fatalf("got %d <to> nodes, want 1", len(tos))
	}
	if tos[0].Attrs["jid"] != bobJID {
		t.Fatalf("to jid = %q", tos[0].Attrs["jid"])
	}
	enc, ok := childByTag(tos[0], "enc")
	if !ok {
		t.Fatal("missing <enc>")
	}
	if enc.Attrs["v"] != "2" || enc.Attrs["type"] != "pkmsg" {
		t.Fatalf("enc attrs wrong: %+v (want v=2 type=pkmsg)", enc.Attrs)
	}
	ciphertext := nodeBytes(enc)
	if len(ciphertext) == 0 {
		t.Fatal("empty ciphertext")
	}

	// Decrypt as bob (responder X3DH).
	padded, _, err := signal.ProcessPreKeyMessage(
		ciphertext, bobIdentity, bobSignedPre, &bobPreKey, v.Bob.RegistrationID,
	)
	if err != nil {
		t.Fatalf("bob ProcessPreKeyMessage: %v", err)
	}
	plaintext, err := unpadMessage(padded)
	if err != nil {
		t.Fatalf("unpad: %v", err)
	}
	var m waproto.Message
	if err := proto.Unmarshal(plaintext, &m); err != nil {
		t.Fatalf("unmarshal message: %v", err)
	}
	if got := m.GetConversation(); got != wantText {
		t.Fatalf("decrypted text = %q, want %q", got, wantText)
	}

	cancel()
	conn.close()
	<-loopDone
}

// --- helpers ---

func waitActive(t *testing.T, c *Client) *session {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s, ok := c.activeSession(); ok {
			return s
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("active session never published")
	return nil
}

// usyncResultFor builds a usync device-list result with just device 0 for jid.
func usyncResultFor(id, jid string) wire.Node {
	return wire.Node{
		Tag:   "iq",
		Attrs: map[string]string{"type": "result", "id": id},
		Content: []wire.Node{{
			Tag: "usync", Attrs: map[string]string{},
			Content: []wire.Node{{
				Tag: "list", Attrs: map[string]string{},
				Content: []wire.Node{{
					Tag:   "user",
					Attrs: map[string]string{"jid": jid},
					Content: []wire.Node{{
						Tag: "devices", Attrs: map[string]string{},
						Content: []wire.Node{{
							Tag: "device-list", Attrs: map[string]string{},
							Content: []wire.Node{
								{Tag: "device", Attrs: map[string]string{"id": "0"}},
							},
						}},
					}},
				}},
			}},
		}},
	}
}

// preKeyResultFor builds an <iq xmlns=encrypt> result with one bundle for jid.
func preKeyResultFor(id, jid string, identityPub [32]byte, spkID uint32, spkPub [32]byte, spkSig [64]byte, pkID uint32, pkPub [32]byte, regID uint32) wire.Node {
	return wire.Node{
		Tag:   "iq",
		Attrs: map[string]string{"type": "result", "id": id},
		Content: []wire.Node{{
			Tag: "list", Attrs: map[string]string{},
			Content: []wire.Node{{
				Tag:   "user",
				Attrs: map[string]string{"jid": jid},
				Content: []wire.Node{
					{Tag: "registration", Attrs: map[string]string{}, Content: encodeBigEndianN(regID, 4)},
					{Tag: "identity", Attrs: map[string]string{}, Content: append([]byte(nil), identityPub[:]...)},
					{
						Tag: "skey", Attrs: map[string]string{},
						Content: []wire.Node{
							{Tag: "id", Attrs: map[string]string{}, Content: encodeBigEndianN(spkID, 3)},
							{Tag: "value", Attrs: map[string]string{}, Content: append([]byte(nil), spkPub[:]...)},
							{Tag: "signature", Attrs: map[string]string{}, Content: append([]byte(nil), spkSig[:]...)},
						},
					},
					{
						Tag: "key", Attrs: map[string]string{},
						Content: []wire.Node{
							{Tag: "id", Attrs: map[string]string{}, Content: encodeBigEndianN(pkID, 3)},
							{Tag: "value", Attrs: map[string]string{}, Content: append([]byte(nil), pkPub[:]...)},
						},
					},
				},
			}},
		}},
	}
}

// gatedConn is a nodeConn whose inbound nodes are fed on demand and whose written
// nodes are observable, so a test can interleave request/response with the loop.
type gatedConn struct {
	mu      sync.Mutex
	cond    *sync.Cond
	inbound []wire.Node
	written []wire.Node
	cursor  int
	closed  bool
}

func newGatedConn() *gatedConn {
	g := &gatedConn{}
	g.cond = sync.NewCond(&g.mu)
	return g
}

func (g *gatedConn) ReadNode() (wire.Node, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for len(g.inbound) == 0 && !g.closed {
		g.cond.Wait()
	}
	if len(g.inbound) == 0 {
		return wire.Node{}, context.Canceled
	}
	n := g.inbound[0]
	g.inbound = g.inbound[1:]
	return n, nil
}

func (g *gatedConn) SendNode(n wire.Node) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.written = append(g.written, n)
	g.cond.Broadcast()
	return nil
}

func (g *gatedConn) Close() error { g.close(); return nil }

func (g *gatedConn) close() {
	g.mu.Lock()
	g.closed = true
	g.cond.Broadcast()
	g.mu.Unlock()
}

func (g *gatedConn) feed(n wire.Node) {
	g.mu.Lock()
	g.inbound = append(g.inbound, n)
	g.cond.Broadcast()
	g.mu.Unlock()
}

// nextWritten blocks until a new written node beyond what it last returned is
// available, returning it. It tracks an internal cursor via the written slice.
func (g *gatedConn) nextWritten(ctx context.Context) (wire.Node, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for {
		if g.cursor < len(g.written) {
			n := g.written[g.cursor]
			g.cursor++
			return n, true
		}
		if g.closed || ctx.Err() != nil {
			return wire.Node{}, false
		}
		g.cond.Wait()
	}
}

func waitForWritten(t *testing.T, g *gatedConn, id string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		g.mu.Lock()
		for _, n := range g.written {
			if n.Attrs["id"] == id {
				g.mu.Unlock()
				return
			}
		}
		g.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("node with id %q never written", id)
}

func waitForTag(t *testing.T, g *gatedConn, tag string) wire.Node {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		g.mu.Lock()
		for _, n := range g.written {
			if n.Tag == tag {
				g.mu.Unlock()
				return n
			}
		}
		g.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("node with tag %q never written", tag)
	return wire.Node{}
}
