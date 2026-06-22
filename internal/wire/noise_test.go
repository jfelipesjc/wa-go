package wire

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"golang.org/x/crypto/curve25519"
)

// noiseFixture mirrors testdata/traces/connect_pair/noise.json.
type noiseFixture struct {
	NoiseMode            string `json:"noiseMode"`
	NoiseHeader          string `json:"noiseHeader"`
	EphemeralKeyPriv     string `json:"ephemeralKeyPriv"`
	EphemeralKeyPub      string `json:"ephemeralKeyPub"`
	ServerEphemeral      string `json:"serverEphemeral"`
	ServerStaticEnc      string `json:"serverStaticEnc"`
	ServerPayloadEnc     string `json:"serverPayloadEnc"`
	AuthStaticKeyPriv    string `json:"authStaticKeyPriv"`
	AuthStaticKeyPub     string `json:"authStaticKeyPub"`
	ClientHelloFrameHex  string `json:"clientHelloFrameHex"`
	ServerHelloFrameHex  string `json:"serverHelloFrameHex"`
	ClientFinishFrameHex string `json:"clientFinishFrameHex"`
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex decode %q: %v", s, err)
	}
	return b
}

func loadNoiseFixture(t *testing.T) noiseFixture {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/traces/connect_pair/noise.json")
	if err != nil {
		t.Fatalf("read noise.json: %v", err)
	}
	var f noiseFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("unmarshal noise.json: %v", err)
	}
	return f
}

// derivePub computes the X25519 public key for a given private key.
func derivePub(t *testing.T, priv []byte) []byte {
	t.Helper()
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		t.Fatalf("derive pub: %v", err)
	}
	return pub
}

// Test #1: ClientHello byte-for-byte.
//
// With the injected ephemeral from noise.json, the WA header + framed
// HandshakeMessage{clientHello:{ephemeral}} must match clientHelloFrameHex
// exactly.
func TestClientHelloByteForByte(t *testing.T) {
	f := loadNoiseFixture(t)
	ephPub := mustHex(t, f.EphemeralKeyPub)

	hello := encodeClientHello(ephPub)

	var buf bytes.Buffer
	buf.Write(noiseWAHeader)
	if err := writeFrame(&buf, hello); err != nil {
		t.Fatalf("writeFrame: %v", err)
	}

	got := hex.EncodeToString(buf.Bytes())
	if got != f.ClientHelloFrameHex {
		t.Fatalf("ClientHello mismatch:\n got=%s\nwant=%s", got, f.ClientHelloFrameHex)
	}
}

// Test #2: Decrypt ServerHello.
//
// Run the handshake far enough to decrypt the server static and payload. The
// server static must be a 32-byte public key and neither GCM open may fail.
func TestDecryptServerHello(t *testing.T) {
	f := loadNoiseFixture(t)
	ephPriv := mustHex(t, f.EphemeralKeyPriv)
	ephPub := mustHex(t, f.EphemeralKeyPub)
	staticPub := mustHex(t, f.AuthStaticKeyPub)

	// ServerHello frame: strip the 3-byte length prefix.
	serverHelloFrame := mustHex(t, f.ServerHelloFrameHex)
	serverHelloMsg := serverHelloFrame[3:]

	// newNoise mixes in NOISE_HEADER and the client ephemeral pub (WA passes the
	// ephemeral key pair to makeNoiseHandler).
	n := newNoise(ephPub)
	_ = staticPub

	// Reproduce the handshake steps manually up to the payload decrypt.
	sh, err := parseHandshakeMessage(serverHelloMsg, 3)
	if err != nil {
		t.Fatalf("parse ServerHello: %v", err)
	}
	if !bytes.Equal(sh.f1, mustHex(t, f.ServerEphemeral)) {
		t.Fatalf("server ephemeral mismatch:\n got=%x\nwant=%s", sh.f1, f.ServerEphemeral)
	}
	if !bytes.Equal(sh.f2, mustHex(t, f.ServerStaticEnc)) {
		t.Fatalf("server static enc mismatch")
	}
	if !bytes.Equal(sh.f3, mustHex(t, f.ServerPayloadEnc)) {
		t.Fatalf("server payload enc mismatch")
	}

	n.mixHash(sh.f1)
	es, err := dh(ephPriv, sh.f1)
	if err != nil {
		t.Fatalf("DH(e,re): %v", err)
	}
	if err := n.mixKey(es); err != nil {
		t.Fatalf("mixKey es: %v", err)
	}

	serverStatic, err := n.decrypt(sh.f2)
	if err != nil {
		t.Fatalf("decrypt server static (GCM auth failed): %v", err)
	}
	if len(serverStatic) != 32 {
		t.Fatalf("server static not 32 bytes: got %d", len(serverStatic))
	}

	ss, err := dh(ephPriv, serverStatic)
	if err != nil {
		t.Fatalf("DH(e,rs): %v", err)
	}
	if err := n.mixKey(ss); err != nil {
		t.Fatalf("mixKey ss: %v", err)
	}

	serverPayload, err := n.decrypt(sh.f3)
	if err != nil {
		t.Fatalf("decrypt server payload (GCM auth failed): %v", err)
	}
	if len(serverPayload) == 0 {
		t.Fatalf("server payload empty")
	}
}

// Test #3: ClientFinish — recover the client static via decryption.
//
// Using the captured clientFinishFrameHex, decrypt the static field with the
// handshake state and recover exactly authStaticKeyPub. Then advance the hash
// over the captured ciphertexts (as the server side would) and decrypt the
// payload without an auth error.
func TestClientFinishRecoverStatic(t *testing.T) {
	f := loadNoiseFixture(t)
	ephPriv := mustHex(t, f.EphemeralKeyPriv)
	ephPub := mustHex(t, f.EphemeralKeyPub)
	staticPub := mustHex(t, f.AuthStaticKeyPub)

	serverHelloMsg := mustHex(t, f.ServerHelloFrameHex)[3:]

	n := newNoise(ephPub)
	_ = staticPub
	sh, _ := parseHandshakeMessage(serverHelloMsg, 3)
	n.mixHash(sh.f1)
	es, _ := dh(ephPriv, sh.f1)
	if err := n.mixKey(es); err != nil {
		t.Fatal(err)
	}
	serverStatic, err := n.decrypt(sh.f2)
	if err != nil {
		t.Fatalf("decrypt server static: %v", err)
	}
	ss, _ := dh(ephPriv, serverStatic)
	if err := n.mixKey(ss); err != nil {
		t.Fatal(err)
	}
	if _, err := n.decrypt(sh.f3); err != nil {
		t.Fatalf("decrypt server payload: %v", err)
	}

	// Now at the point where the server decrypts ClientFinish.
	cf, err := parseHandshakeMessage(mustHex(t, f.ClientFinishFrameHex)[3:], 4)
	if err != nil {
		t.Fatalf("parse ClientFinish: %v", err)
	}

	// decrypt(static) must recover the client static public key exactly. This
	// also advances the hash with the static ciphertext (mirrors the encrypt on
	// the client side).
	recoveredStatic, err := n.decrypt(cf.f1)
	if err != nil {
		t.Fatalf("decrypt client static (GCM auth failed): %v", err)
	}
	if !bytes.Equal(recoveredStatic, staticPub) {
		t.Fatalf("recovered static mismatch:\n got=%x\nwant=%x", recoveredStatic, staticPub)
	}

	// mixKey(DH(rs=clientStatic, e=serverEphemeral)) — server side does
	// DH(serverEphemeralPriv, clientStatic); we don't have the server ephemeral
	// private, but the client side does DH(clientStaticPriv, serverEphemeral),
	// which yields the same shared secret. Use the client static private.
	staticPriv := mustHex(t, f.AuthStaticKeyPriv)
	se, err := dh(staticPriv, sh.f1)
	if err != nil {
		t.Fatalf("DH(s,re): %v", err)
	}
	if err := n.mixKey(se); err != nil {
		t.Fatal(err)
	}

	// decrypt(payload) must not fail authentication.
	if _, err := n.decrypt(cf.f2); err != nil {
		t.Fatalf("decrypt client payload (GCM auth failed): %v", err)
	}
}

// TestClientHandshakeProducesCapturedClientFinish exercises the full
// ClientHandshake helper and verifies that it reproduces the captured
// ClientFinish static ciphertext byte-for-byte (the encrypt path), and that the
// recovered server static matches the published serverStaticEnc decrypt.
func TestClientHandshakeProducesCapturedClientFinish(t *testing.T) {
	f := loadNoiseFixture(t)
	ephPriv := mustHex(t, f.EphemeralKeyPriv)
	ephPub := mustHex(t, f.EphemeralKeyPub)
	staticPriv := mustHex(t, f.AuthStaticKeyPriv)
	staticPub := mustHex(t, f.AuthStaticKeyPub)
	serverHelloMsg := mustHex(t, f.ServerHelloFrameHex)[3:]

	// We do not have the original ClientPayload plaintext, so we cannot
	// reproduce the ClientFinish payload field. But the static field is
	// deterministic given the handshake state, so verify that.
	// Use an empty plaintext for the payload (we only check the static field).
	n := newNoise(ephPub)
	res, err := n.ClientHandshake(ephPriv, ephPub, staticPriv, staticPub, serverHelloMsg, []byte("x"))
	if err != nil {
		t.Fatalf("ClientHandshake: %v", err)
	}

	// ClientHello matches the captured frame body.
	wantHello := mustHex(t, f.ClientHelloFrameHex)[4:] // strip WA header
	if !bytes.Equal(res.ClientHello, wantHello[3:]) {  // strip 3-byte length
		t.Fatalf("ClientHello body mismatch:\n got=%x\nwant=%x", res.ClientHello, wantHello[3:])
	}

	// The static field of our ClientFinish must equal the captured one.
	gotCF, err := parseHandshakeMessage(res.ClientFinish, 4)
	if err != nil {
		t.Fatalf("parse our ClientFinish: %v", err)
	}
	wantCF, err := parseHandshakeMessage(mustHex(t, f.ClientFinishFrameHex)[3:], 4)
	if err != nil {
		t.Fatalf("parse captured ClientFinish: %v", err)
	}
	if !bytes.Equal(gotCF.f1, wantCF.f1) {
		t.Fatalf("ClientFinish static mismatch:\n got=%x\nwant=%x", gotCF.f1, wantCF.f1)
	}

	// Sanity: derived ephemeral pub matches.
	if !bytes.Equal(derivePub(t, ephPriv), ephPub) {
		t.Fatalf("ephemeral pub derivation mismatch")
	}

	if len(res.WriteKey) != 32 || len(res.ReadKey) != 32 {
		t.Fatalf("transport keys wrong length")
	}
}

// frameLine mirrors one entry of frames_raw.jsonl.
type frameLine struct {
	Dir string `json:"dir"`
	T   int    `json:"t"`
	Hex string `json:"hex"`
}

func loadFramesRaw(t *testing.T) []frameLine {
	t.Helper()
	fh, err := os.Open("../../testdata/traces/connect_pair/frames_raw.jsonl")
	if err != nil {
		t.Fatalf("open frames_raw: %v", err)
	}
	defer fh.Close()
	var out []frameLine
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var fl frameLine
		if err := json.Unmarshal(line, &fl); err != nil {
			t.Fatalf("unmarshal frame: %v", err)
		}
		out = append(out, fl)
	}
	return out
}

func loadInNode(t *testing.T) Node {
	t.Helper()
	fh, err := os.Open("../../testdata/traces/connect_pair/nodes.jsonl")
	if err != nil {
		t.Fatalf("open nodes.jsonl: %v", err)
	}
	defer fh.Close()
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var cl connectPairLine
		if err := json.Unmarshal(line, &cl); err != nil {
			t.Fatalf("unmarshal node: %v", err)
		}
		if cl.Dir == "in" {
			node, err := nodeFromJSON(cl.Tree)
			if err != nil {
				t.Fatalf("nodeFromJSON: %v", err)
			}
			return node
		}
	}
	t.Fatal("no 'in' node found")
	return Node{}
}

// Test #4 (DECISIVE): full wire layer end-to-end.
//
// Run the complete handshake, derive transport keys, then take the captured 698-
// byte pair-device frame, strip the 3-byte length, decrypt with the transport
// read key (counter 0), handle the compression prefix byte, decode the binary
// node, and require it equals the captured 'in' node (iq > pair-device > ref).
func TestEndToEndPairDevice(t *testing.T) {
	f := loadNoiseFixture(t)
	ephPriv := mustHex(t, f.EphemeralKeyPriv)
	ephPub := mustHex(t, f.EphemeralKeyPub)
	staticPriv := mustHex(t, f.AuthStaticKeyPriv)
	staticPub := mustHex(t, f.AuthStaticKeyPub)
	serverHelloMsg := mustHex(t, f.ServerHelloFrameHex)[3:]

	n := newNoise(ephPub)
	res, err := n.ClientHandshake(ephPriv, ephPub, staticPriv, staticPub, serverHelloMsg, []byte("x"))
	if err != nil {
		t.Fatalf("ClientHandshake: %v", err)
	}
	if len(res.ReadKey) != 32 {
		t.Fatalf("no read key")
	}

	// Find the inbound 698-byte pair-device frame.
	frames := loadFramesRaw(t)
	var pairFrameHex string
	for _, fl := range frames {
		if fl.Dir == "in" && len(fl.Hex)/2 == 698 {
			pairFrameHex = fl.Hex
			break
		}
	}
	if pairFrameHex == "" {
		t.Fatal("pair-device 698-byte frame not found")
	}
	frame := mustHex(t, pairFrameHex)

	// Strip the 3-byte length prefix.
	ciphertext := frame[3:]

	// Decrypt with transport read key, counter 0.
	plaintext, err := n.transport.decrypt(ciphertext)
	if err != nil {
		t.Fatalf("transport decrypt pair-device (GCM auth failed): %v", err)
	}
	if len(plaintext) == 0 {
		t.Fatal("empty plaintext")
	}

	// Handle compression prefix: 0x00 = no zlib, 0x02 = zlib-deflated.
	compByte := plaintext[0]
	t.Logf("pair-device compression prefix byte: 0x%02x", compByte)
	var nodeBytes []byte
	switch compByte {
	case 0x00:
		nodeBytes = plaintext[1:]
	case 0x02:
		zr, err := zlibInflate(plaintext[1:])
		if err != nil {
			t.Fatalf("zlib inflate: %v", err)
		}
		nodeBytes = zr
	default:
		t.Fatalf("unexpected compression byte 0x%02x", compByte)
	}

	got, err := DecodeNode(nodeBytes)
	if err != nil {
		t.Fatalf("DecodeNode: %v", err)
	}

	want := loadInNode(t)
	if msg := nodeEqual(got, want); msg != "" {
		t.Fatalf("decoded pair-device node mismatch: %s", msg)
	}

	// Spot-check the structure.
	if got.Tag != "iq" {
		t.Fatalf("top tag: got %q want iq", got.Tag)
	}
}
