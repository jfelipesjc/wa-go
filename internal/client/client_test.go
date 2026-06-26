package client

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jfelipesjc/wa-go/internal/keys"
	"github.com/jfelipesjc/wa-go/internal/waproto"
	"github.com/jfelipesjc/wa-go/internal/wire"
	"google.golang.org/protobuf/proto"
)

// --- Test 1: buildQRString matches the captured fixture ---

type qrFixture struct {
	QR    string   `json:"qr"`
	Parts []string `json:"parts"`
}

func TestBuildQRString_MatchesFixture(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "traces", "connect_pair", "qr.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fx qrFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	if len(fx.Parts) != 5 {
		t.Fatalf("fixture parts = %d, want 5", len(fx.Parts))
	}
	// The fixture's parts[0] is the full qr string split on ",", so it still
	// carries the "https://wa.me/.../#" prefix that buildPairingQRData prepends.
	// The real <ref> node from WhatsApp carries only the bare ref, so strip it.
	ref := strings.TrimPrefix(fx.Parts[0], "https://wa.me/settings/linked_devices#")
	noisePub := mustB64(t, fx.Parts[1])
	identityPub := mustB64(t, fx.Parts[2])
	advSecret := mustB64(t, fx.Parts[3])
	platform := fx.Parts[4]

	got := buildQRString(ref, noisePub, identityPub, advSecret, platform)
	if got != fx.QR {
		t.Fatalf("buildQRString mismatch:\n got=%q\nwant=%q", got, fx.QR)
	}
}

func mustB64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("base64 decode %q: %v", s, err)
	}
	return b
}

// --- Test 2: handlePairSuccess over a synthetic vector ---

// buildSyntheticPairSuccess constructs a valid pair-success <iq> node plus the
// device identity we (the companion) hold. It mints a fake "account" key pair,
// builds an ADVDeviceIdentity (the inner details), signs it with the account key
// over [6,0]||details||ourIdentityPub, then wraps it in an
// ADVSignedDeviceIdentity and an ADVSignedDeviceIdentityHMAC with a valid HMAC
// under advSecret.
func buildSyntheticPairSuccess(t *testing.T, ourID keys.Identity, advSecret []byte, jid string, keyIndex uint32) wire.Node {
	t.Helper()

	// Inner device identity (details).
	devIdentity := &waproto.ADVDeviceIdentity{
		RawId:     proto.Uint32(7),
		Timestamp: proto.Uint64(1700000000),
		KeyIndex:  proto.Uint32(keyIndex),
	}
	details, err := proto.Marshal(devIdentity)
	if err != nil {
		t.Fatalf("marshal device identity: %v", err)
	}

	// Fake account key pair (the "primary device" identity key).
	account, err := keys.GenKeyPair()
	if err != nil {
		t.Fatalf("gen account key: %v", err)
	}

	// account signs [6,0] || details || ourIdentityPub.
	accountMsg := concat([]byte{6, 0}, details, ourID.IdentityKey.Pub[:])
	accSig, err := keys.Sign(account.Priv, accountMsg)
	if err != nil {
		t.Fatalf("sign account msg: %v", err)
	}

	signed := &waproto.ADVSignedDeviceIdentity{
		Details:             details,
		AccountSignatureKey: account.Pub[:],
		AccountSignature:    accSig[:],
	}
	signedBytes, err := proto.Marshal(signed)
	if err != nil {
		t.Fatalf("marshal signed identity: %v", err)
	}

	// HMAC over the signed bytes (= details field of the HMAC message).
	mac := hmac.New(sha256.New, advSecret)
	mac.Write(signedBytes)
	hmacMsg := &waproto.ADVSignedDeviceIdentityHMAC{
		Details: signedBytes,
		Hmac:    mac.Sum(nil),
	}
	hmacBytes, err := proto.Marshal(hmacMsg)
	if err != nil {
		t.Fatalf("marshal hmac message: %v", err)
	}

	return wire.Node{
		Tag:   "iq",
		Attrs: map[string]string{"type": "set", "id": "pairid1", "from": sWhatsAppNet},
		Content: []wire.Node{
			{
				Tag:   "pair-success",
				Attrs: map[string]string{},
				Content: []wire.Node{
					{Tag: "device-identity", Attrs: map[string]string{}, Content: hmacBytes},
					{Tag: "device", Attrs: map[string]string{"jid": jid}},
					{Tag: "platform", Attrs: map[string]string{"name": "smba"}},
					{Tag: "biz", Attrs: map[string]string{"name": "Test Biz"}},
				},
			},
		},
	}
}

func TestHandlePairSuccess_Valid(t *testing.T) {
	id, err := keys.NewIdentity()
	if err != nil {
		t.Fatalf("new identity: %v", err)
	}
	advSecret := id.AdvSecret[:]
	const jid = "5511999999999:7@s.whatsapp.net"
	const keyIndex = 3

	node := buildSyntheticPairSuccess(t, id, advSecret, jid, keyIndex)

	reply, creds, err := handlePairSuccess(node, id, advSecret)
	if err != nil {
		t.Fatalf("handlePairSuccess: %v", err)
	}

	// Post-pairing creds.
	if creds.Me != jid {
		t.Errorf("Me = %q, want %q", creds.Me, jid)
	}
	if creds.Platform != "smba" {
		t.Errorf("Platform = %q, want smba", creds.Platform)
	}
	if creds.PushName != "Test Biz" {
		t.Errorf("PushName = %q, want Test Biz", creds.PushName)
	}

	// Reply structure: iq[type=result] > pair-device-sign > device-identity[key-index].
	if reply.Tag != "iq" || reply.Attrs["type"] != "result" || reply.Attrs["id"] != "pairid1" {
		t.Fatalf("reply iq attrs wrong: %+v", reply.Attrs)
	}
	pds, ok := childByTag(reply, "pair-device-sign")
	if !ok {
		t.Fatal("reply missing pair-device-sign")
	}
	di, ok := childByTag(pds, "device-identity")
	if !ok {
		t.Fatal("reply missing device-identity")
	}
	if di.Attrs["key-index"] != "3" {
		t.Errorf("key-index = %q, want 3", di.Attrs["key-index"])
	}

	// The re-serialized account in the reply must (a) have a verifiable device
	// signature, (b) have NO accountSignatureKey.
	var reSigned waproto.ADVSignedDeviceIdentity
	if err := proto.Unmarshal(nodeBytes(di), &reSigned); err != nil {
		t.Fatalf("unmarshal reply account: %v", err)
	}
	if len(reSigned.GetAccountSignatureKey()) != 0 {
		t.Errorf("accountSignatureKey should be zeroed, got %d bytes", len(reSigned.GetAccountSignatureKey()))
	}
	if len(reSigned.GetDeviceSignature()) != 64 {
		t.Fatalf("device signature len = %d, want 64", len(reSigned.GetDeviceSignature()))
	}

	// deviceSignature is sign(ourIdentity.priv, [6,1]||details||ourIdentityPub||accountSignatureKey).
	// Recover accountSignatureKey from the original node to verify.
	origAccount := decodeAccountFromNode(t, node)
	deviceMsg := concat([]byte{6, 1}, reSigned.GetDetails(), id.IdentityKey.Pub[:], origAccount.GetAccountSignatureKey())
	var devSig [64]byte
	copy(devSig[:], reSigned.GetDeviceSignature())
	if !keys.Verify(id.IdentityKey.Pub, deviceMsg, devSig) {
		t.Fatal("device signature does not verify against our identity public key")
	}
}

// decodeAccountFromNode pulls the inner ADVSignedDeviceIdentity back out of a
// synthetic pair-success node (for cross-checking in tests).
func decodeAccountFromNode(t *testing.T, node wire.Node) *waproto.ADVSignedDeviceIdentity {
	t.Helper()
	ps, _ := childByTag(node, "pair-success")
	di, _ := childByTag(ps, "device-identity")
	var h waproto.ADVSignedDeviceIdentityHMAC
	if err := proto.Unmarshal(nodeBytes(di), &h); err != nil {
		t.Fatalf("decode hmac: %v", err)
	}
	var acc waproto.ADVSignedDeviceIdentity
	if err := proto.Unmarshal(h.GetDetails(), &acc); err != nil {
		t.Fatalf("decode account: %v", err)
	}
	return &acc
}

func TestHandlePairSuccess_BadHMAC(t *testing.T) {
	id, _ := keys.NewIdentity()
	node := buildSyntheticPairSuccess(t, id, id.AdvSecret[:], "1:7@s.whatsapp.net", 1)
	// Verify with a DIFFERENT advSecret → HMAC mismatch.
	var wrong [32]byte
	wrong[0] = 0xff
	if _, _, err := handlePairSuccess(node, id, wrong[:]); err == nil {
		t.Fatal("expected HMAC mismatch error, got nil")
	}
}

func TestHandlePairSuccess_BadAccountSignature(t *testing.T) {
	id, _ := keys.NewIdentity()
	advSecret := id.AdvSecret[:]
	node := buildSyntheticPairSuccess(t, id, advSecret, "1:7@s.whatsapp.net", 1)

	// Tamper the account signature INSIDE the signed identity, then re-HMAC so
	// the HMAC check passes but the account-signature check fails.
	ps, _ := childByTag(node, "pair-success")
	di, _ := childByTag(ps, "device-identity")
	var h waproto.ADVSignedDeviceIdentityHMAC
	if err := proto.Unmarshal(nodeBytes(di), &h); err != nil {
		t.Fatal(err)
	}
	var acc waproto.ADVSignedDeviceIdentity
	if err := proto.Unmarshal(h.GetDetails(), &acc); err != nil {
		t.Fatal(err)
	}
	// Flip a byte in the account signature.
	sig := append([]byte(nil), acc.GetAccountSignature()...)
	sig[0] ^= 0xff
	acc.AccountSignature = sig
	tampered, _ := proto.Marshal(&acc)
	mac := hmac.New(sha256.New, advSecret)
	mac.Write(tampered)
	h.Details = tampered
	h.Hmac = mac.Sum(nil)
	newDI, _ := proto.Marshal(&h)

	// Rebuild the node with the tampered device-identity.
	ps.Content = []wire.Node{
		{Tag: "device-identity", Attrs: map[string]string{}, Content: newDI},
		{Tag: "device", Attrs: map[string]string{"jid": "1:7@s.whatsapp.net"}},
	}
	node.Content = []wire.Node{ps}

	if _, _, err := handlePairSuccess(node, id, advSecret); err == nil {
		t.Fatal("expected account-signature verification error, got nil")
	} else if !strings.Contains(err.Error(), "account signature") {
		t.Fatalf("expected account signature error, got: %v", err)
	}
}

// --- Test 3: pairing loop replay (offline) emits QR + writes iq result ---

// scriptedConn is a nodeConn that serves a fixed sequence of inbound nodes and
// records everything written.
type scriptedConn struct {
	inbound []wire.Node
	idx     int
	written []wire.Node
}

func (s *scriptedConn) ReadNode() (wire.Node, error) {
	if s.idx >= len(s.inbound) {
		return wire.Node{}, context.Canceled // signal "stream end"
	}
	n := s.inbound[s.idx]
	s.idx++
	return n, nil
}

func (s *scriptedConn) SendNode(n wire.Node) error {
	s.written = append(s.written, n)
	return nil
}

func (s *scriptedConn) Close() error { return nil }

func TestPairingLoop_PairDeviceEmitsQRAndReplies(t *testing.T) {
	id, err := keys.NewIdentity()
	if err != nil {
		t.Fatalf("new identity: %v", err)
	}
	creds := credsFromIdentity(id)

	pairDevice := wire.Node{
		Tag:   "iq",
		Attrs: map[string]string{"type": "set", "id": "iq-1", "from": sWhatsAppNet},
		Content: []wire.Node{
			{
				Tag:   "pair-device",
				Attrs: map[string]string{},
				Content: []wire.Node{
					{Tag: "ref", Attrs: map[string]string{}, Content: []byte("REF_ALPHA")},
					{Tag: "ref", Attrs: map[string]string{}, Content: []byte("REF_BETA")},
				},
			},
		},
	}

	conn := &scriptedConn{inbound: []wire.Node{pairDevice}}
	c := New(nil)

	// Drain events concurrently.
	var qrs []string
	done := make(chan struct{})
	go func() {
		for e := range c.events {
			if q, ok := e.(QREvent); ok {
				qrs = append(qrs, q.Code)
			}
		}
		close(done)
	}()

	paired, err := c.pairingLoop(context.Background(), conn, creds)
	close(c.events)
	<-done

	if err != nil {
		t.Fatalf("pairingLoop: %v", err)
	}
	if paired {
		t.Fatal("paired should be false (no pair-success)")
	}

	// Refs are now rotated one at a time (qrRotateInterval apart), so on a fast
	// stream-end only the first ref's QR is emitted before the loop returns and
	// cancels the rotation goroutine.
	if len(qrs) < 1 {
		t.Fatalf("got %d QR events, want at least 1", len(qrs))
	}
	wantFirst := buildQRString("REF_ALPHA", creds.NoiseKey.Pub, creds.IdentityKey.Pub, creds.AdvSecret, platformID())
	if qrs[0] != wantFirst {
		t.Fatalf("QR[0] mismatch:\n got=%q\nwant=%q", qrs[0], wantFirst)
	}

	// One iq result written, addressed to @s.whatsapp.net with the right id.
	if len(conn.written) != 1 {
		t.Fatalf("wrote %d nodes, want 1 (the iq result)", len(conn.written))
	}
	w := conn.written[0]
	if w.Tag != "iq" || w.Attrs["type"] != "result" || w.Attrs["id"] != "iq-1" || w.Attrs["to"] != sWhatsAppNet {
		t.Fatalf("iq result wrong: %+v", w.Attrs)
	}
}

// --- Test 4: NewWithDialer injects the transport ---

func TestNewWithDialer_UsesInjectedDial(t *testing.T) {
	called := false
	c := NewWithDialer(nil, func(ctx context.Context) (io.ReadWriteCloser, error) {
		called = true
		return nil, context.Canceled
	})
	if c.dial == nil {
		t.Fatal("dial not set")
	}
	// Invoking the injected dialer should run our closure.
	_, _ = c.dial(context.Background())
	if !called {
		t.Fatal("injected dialer was not used")
	}
	// nil dial must fall back to the real dialer (non-nil).
	if NewWithDialer(nil, nil).dial == nil {
		t.Fatal("nil dial should fall back to dialWebSocket")
	}
}

// guard against accidental real time usage in the loop test.
var _ = time.Second
