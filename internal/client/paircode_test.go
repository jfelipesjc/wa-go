package client

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/felipeleal/wa-go/internal/keys"
	"github.com/felipeleal/wa-go/internal/store"
	"github.com/felipeleal/wa-go/internal/wire"
)

// paircodeVectors mirrors testdata/paircode/vectors.json (generated OFFLINE by
// harness/gen_paircode_vectors.mjs from Baileys). Only the fields the Go port
// reproduces are decoded.
type paircodeVectors struct {
	Crockford struct {
		InputHex string `json:"input_hex"`
		Expected string `json:"expected"`
	} `json:"crockford"`
	DerivePairingCodeKey struct {
		Code       string `json:"code"`
		SaltHex    string `json:"salt_hex"`
		Iterations int    `json:"iterations"`
		KeyHex     string `json:"key_hex"`
	} `json:"derivePairingCodeKey"`
	AesEncryptCTR struct {
		EphemeralPubHex string `json:"ephemeral_pub_hex"`
		IvHex           string `json:"iv_hex"`
		CiphertextHex   string `json:"ciphertext_hex"`
	} `json:"aesEncryptCTR"`
	GeneratePairingKey struct {
		Code            string `json:"code"`
		SaltHex         string `json:"salt_hex"`
		IvHex           string `json:"iv_hex"`
		EphemeralPubHex string `json:"ephemeral_pub_hex"`
		BlobHex         string `json:"blob_hex"`
	} `json:"generatePairingKey"`
}

func loadPaircodeVectors(t *testing.T) paircodeVectors {
	t.Helper()
	// internal/client -> repo root is ../../.
	path := filepath.Join("..", "..", "testdata", "paircode", "vectors.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var v paircodeVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("unmarshal vectors: %v", err)
	}
	return v
}

func pcHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("decode hex %q: %v", s, err)
	}
	return b
}

func TestCrockfordEncode_GoldenVector(t *testing.T) {
	v := loadPaircodeVectors(t)
	got := crockfordEncode(pcHex(t, v.Crockford.InputHex))
	if got != v.Crockford.Expected {
		t.Fatalf("crockfordEncode = %q, want %q", got, v.Crockford.Expected)
	}
	if len(got) != 8 {
		t.Fatalf("expected 8-char code, got %d (%q)", len(got), got)
	}
}

func TestCrockfordEncode_Alphabet(t *testing.T) {
	// Sanity: the alphabet must match Baileys exactly. It starts at '1' (no
	// leading '0') and omits I, O, U — but INCLUDES L (unlike canonical
	// Crockford, which also excludes L). 32 chars.
	if crockfordAlphabet != "123456789ABCDEFGHJKLMNPQRSTVWXYZ" {
		t.Fatalf("alphabet = %q, does not match Baileys", crockfordAlphabet)
	}
	if len(crockfordAlphabet) != 32 {
		t.Fatalf("alphabet length = %d, want 32", len(crockfordAlphabet))
	}
	for _, bad := range []byte{'0', 'I', 'O', 'U'} {
		for i := 0; i < len(crockfordAlphabet); i++ {
			if crockfordAlphabet[i] == bad {
				t.Fatalf("alphabet must not contain %q", string(bad))
			}
		}
	}
}

func TestDerivePairingCodeKey_GoldenVector(t *testing.T) {
	v := loadPaircodeVectors(t)
	if v.DerivePairingCodeKey.Iterations != pairingCodeIterations {
		t.Fatalf("vector iterations %d != const %d", v.DerivePairingCodeKey.Iterations, pairingCodeIterations)
	}
	got := derivePairingCodeKey(v.DerivePairingCodeKey.Code, pcHex(t, v.DerivePairingCodeKey.SaltHex))
	want := pcHex(t, v.DerivePairingCodeKey.KeyHex)
	if string(got) != string(want) {
		t.Fatalf("derivePairingCodeKey = %x, want %x", got, want)
	}
	if len(got) != 32 {
		t.Fatalf("key length = %d, want 32", len(got))
	}
}

func TestWrapCompanionEphemeral_GoldenVector(t *testing.T) {
	v := loadPaircodeVectors(t)
	g := v.GeneratePairingKey
	blob, err := wrapCompanionEphemeral(
		g.Code,
		pcHex(t, g.EphemeralPubHex),
		pcHex(t, g.SaltHex),
		pcHex(t, g.IvHex),
	)
	if err != nil {
		t.Fatalf("wrapCompanionEphemeral: %v", err)
	}
	want := pcHex(t, g.BlobHex)
	if string(blob) != string(want) {
		t.Fatalf("wrapCompanionEphemeral blob = %x, want %x", blob, want)
	}

	// The blob must be salt || iv || ciphertext; verify the ciphertext segment
	// matches the standalone aesEncryptCTR vector.
	saltLen := len(pcHex(t, g.SaltHex))
	ivLen := len(pcHex(t, g.IvHex))
	ct := blob[saltLen+ivLen:]
	if string(ct) != string(pcHex(t, v.AesEncryptCTR.CiphertextHex)) {
		t.Fatalf("ciphertext segment = %x, want %x", ct, pcHex(t, v.AesEncryptCTR.CiphertextHex))
	}
}

func TestGeneratePairingCode_Format(t *testing.T) {
	code, err := GeneratePairingCode()
	if err != nil {
		t.Fatalf("GeneratePairingCode: %v", err)
	}
	if len(code) != 8 {
		t.Fatalf("pairing code length = %d, want 8 (%q)", len(code), code)
	}
	for i := 0; i < len(code); i++ {
		found := false
		for j := 0; j < len(crockfordAlphabet); j++ {
			if code[i] == crockfordAlphabet[j] {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("char %q at %d not in crockford alphabet", string(code[i]), i)
		}
	}
}

func TestBuildCompanionHelloIQ_Structure(t *testing.T) {
	v := loadPaircodeVectors(t)
	g := v.GeneratePairingKey
	noisePub := []byte("noise-pub-32-bytes-placeholder!!") // 32 bytes
	p := companionHelloParams{
		iqID:         "ID123",
		jid:          "5511999998888@s.whatsapp.net",
		code:         g.Code,
		ephemeralPub: pcHex(t, g.EphemeralPubHex),
		noisePub:     noisePub,
		salt:         pcHex(t, g.SaltHex),
		iv:           pcHex(t, g.IvHex),
		platformID:   "1",
		platformDisp: "Chrome (Ubuntu)",
	}
	iq, err := buildCompanionHelloIQ(p)
	if err != nil {
		t.Fatalf("buildCompanionHelloIQ: %v", err)
	}

	// Outer iq.
	if iq.Tag != "iq" {
		t.Fatalf("outer tag = %q, want iq", iq.Tag)
	}
	wantAttrs := map[string]string{"to": sWhatsAppNet, "type": "set", "id": "ID123", "xmlns": "md"}
	for k, want := range wantAttrs {
		if iq.Attrs[k] != want {
			t.Fatalf("iq attr %q = %q, want %q", k, iq.Attrs[k], want)
		}
	}

	reg, ok := childByTag(iq, "link_code_companion_reg")
	if !ok {
		t.Fatalf("missing link_code_companion_reg")
	}
	if reg.Attrs["jid"] != p.jid {
		t.Fatalf("reg jid = %q, want %q", reg.Attrs["jid"], p.jid)
	}
	if reg.Attrs["stage"] != "companion_hello" {
		t.Fatalf("reg stage = %q, want companion_hello", reg.Attrs["stage"])
	}
	if reg.Attrs["should_show_push_notification"] != "true" {
		t.Fatalf("should_show_push_notification = %q, want true", reg.Attrs["should_show_push_notification"])
	}

	// Exactly 5 children in order.
	kids := children(reg)
	wantTags := []string{
		"link_code_pairing_wrapped_companion_ephemeral_pub",
		"companion_server_auth_key_pub",
		"companion_platform_id",
		"companion_platform_display",
		"link_code_pairing_nonce",
	}
	if len(kids) != len(wantTags) {
		t.Fatalf("reg has %d children, want %d", len(kids), len(wantTags))
	}
	for i, want := range wantTags {
		if kids[i].Tag != want {
			t.Fatalf("child %d tag = %q, want %q", i, kids[i].Tag, want)
		}
	}

	// Wrapped ephemeral blob must match the golden generatePairingKey blob.
	wrapped, _ := childByTag(reg, "link_code_pairing_wrapped_companion_ephemeral_pub")
	if got := nodeBytes(wrapped); string(got) != string(pcHex(t, g.BlobHex)) {
		t.Fatalf("wrapped ephemeral = %x, want %x", got, pcHex(t, g.BlobHex))
	}

	// Other leaves.
	authPub, _ := childByTag(reg, "companion_server_auth_key_pub")
	if string(nodeBytes(authPub)) != string(noisePub) {
		t.Fatalf("companion_server_auth_key_pub = %x, want %x", nodeBytes(authPub), noisePub)
	}
	pid, _ := childByTag(reg, "companion_platform_id")
	if string(nodeBytes(pid)) != "1" {
		t.Fatalf("companion_platform_id = %q, want 1", nodeBytes(pid))
	}
	disp, _ := childByTag(reg, "companion_platform_display")
	if string(nodeBytes(disp)) != "Chrome (Ubuntu)" {
		t.Fatalf("companion_platform_display = %q, want Chrome (Ubuntu)", nodeBytes(disp))
	}
	nonce, _ := childByTag(reg, "link_code_pairing_nonce")
	if string(nodeBytes(nonce)) != "0" {
		t.Fatalf("link_code_pairing_nonce = %q, want 0", nodeBytes(nonce))
	}
}

func TestRequestPairingCode_SendsHelloAndPersists(t *testing.T) {
	st := newTestStore(t)
	c := NewWithDialer(st, nil)

	creds := &store.Creds{
		NoiseKey: store.CredKeyPair{
			Priv: make([]byte, 32),
			Pub:  []byte("noise-pub-32-bytes-placeholder!!"),
		},
	}

	var sent wire.Node
	sentCount := 0
	c.setActive(&session{
		creds: creds,
		send: func(n wire.Node) error {
			sent = n
			sentCount++
			return nil
		},
	})

	code, err := c.RequestPairingCode(context.Background(), "5511999998888")
	if err != nil {
		t.Fatalf("RequestPairingCode: %v", err)
	}
	if len(code) != 8 {
		t.Fatalf("code length = %d, want 8 (%q)", len(code), code)
	}
	if sentCount != 1 {
		t.Fatalf("send called %d times, want 1", sentCount)
	}

	// The sent node must be a companion_hello iq.
	reg, ok := childByTag(sent, "link_code_companion_reg")
	if !ok {
		t.Fatalf("sent node missing link_code_companion_reg: %+v", sent)
	}
	if reg.Attrs["stage"] != "companion_hello" {
		t.Fatalf("stage = %q, want companion_hello", reg.Attrs["stage"])
	}
	if reg.Attrs["jid"] != "5511999998888@s.whatsapp.net" {
		t.Fatalf("jid = %q", reg.Attrs["jid"])
	}

	// Creds must have persisted the pairing ephemeral and me.
	if len(creds.PairingEphemeral.Priv) != 32 || len(creds.PairingEphemeral.Pub) != 32 {
		t.Fatalf("pairing ephemeral not persisted: %+v", creds.PairingEphemeral)
	}
	if creds.Me != "5511999998888@s.whatsapp.net" {
		t.Fatalf("creds.Me = %q", creds.Me)
	}

	// Round-trip: the wrapped blob in the sent iq must decrypt back to the
	// persisted ephemeral pub using the returned code + the embedded salt/iv.
	wrapped, _ := childByTag(reg, "link_code_pairing_wrapped_companion_ephemeral_pub")
	blob := nodeBytes(wrapped)
	if len(blob) != 32+16+32 {
		t.Fatalf("wrapped blob length = %d, want 80", len(blob))
	}
	gotBlob, err := wrapCompanionEphemeral(code, creds.PairingEphemeral.Pub, blob[:32], blob[32:48])
	if err != nil {
		t.Fatalf("re-wrap: %v", err)
	}
	if string(gotBlob) != string(blob) {
		t.Fatalf("re-wrapped blob mismatch")
	}
}

func TestRequestPairingCode_NoSession(t *testing.T) {
	c := NewWithDialer(newTestStore(t), nil)
	if _, err := c.RequestPairingCode(context.Background(), "5511999998888"); err == nil {
		t.Fatal("expected error without active session")
	}
}

// TestBuildCompanionFinish_RoundTrip exercises the companion_finish crypto
// OFFLINE by playing the phone's role: it wraps a freshly generated "primary
// ephemeral" public key under the pairing code (exactly as the phone does),
// feeds it to buildCompanionFinish, then independently recomputes the expected
// advSecretKey and decrypts the wrapped key bundle to verify the payload. The
// only part that cannot be tested offline is the live network exchange.
func TestBuildCompanionFinish_RoundTrip(t *testing.T) {
	mustKP := func() keys.KeyPair {
		kp, err := keys.GenKeyPair()
		if err != nil {
			t.Fatalf("genkeypair: %v", err)
		}
		return kp
	}

	const code = "WAGOTEST"

	companionEphem := mustKP() // our (companion) pairing ephemeral
	identity := mustKP()       // our signed identity key
	primaryEphem := mustKP()   // the phone's primary ephemeral
	primaryIdentity := mustKP()

	// The phone wraps its primary ephemeral pub under the pairing code.
	salt := bytesSeq(0x10, 32)
	iv := bytesSeq(0x40, 16)
	wrapped, err := wrapCompanionEphemeral(code, primaryEphem.Pub[:], salt, iv)
	if err != nil {
		t.Fatalf("wrap primary ephemeral: %v", err)
	}

	random := bytesSeq(0x70, 32)
	linkCodeSalt := bytesSeq(0xa0, 32)
	encryptIv := bytesSeq(0xb0, 12)
	ref := []byte("ref-123")

	res, err := buildCompanionFinish(companionFinishInput{
		code:                code,
		pairingEphemPriv:    companionEphem.Priv[:],
		identityPriv:        identity.Priv[:],
		identityPub:         identity.Pub[:],
		jid:                 "5511999998888@s.whatsapp.net",
		iqID:                "fin-1",
		ref:                 ref,
		primaryIdentityPub:  primaryIdentity.Pub[:],
		wrappedPrimaryEphem: wrapped,
		random:              random,
		linkCodeSalt:        linkCodeSalt,
		encryptIv:           encryptIv,
	})
	if err != nil {
		t.Fatalf("buildCompanionFinish: %v", err)
	}

	// Recompute the shared keys the way the spec/Baileys does.
	companionShared, err := x25519(companionEphem.Priv[:], primaryEphem.Pub[:])
	if err != nil {
		t.Fatalf("companion shared: %v", err)
	}
	identityShared, err := x25519(identity.Priv[:], primaryIdentity.Pub[:])
	if err != nil {
		t.Fatalf("identity shared: %v", err)
	}
	wantAdv, err := hkdfSHA256(concat(companionShared, identityShared, random), 32, nil, []byte("adv_secret"))
	if err != nil {
		t.Fatalf("adv hkdf: %v", err)
	}
	if string(res.advSecretKey) != string(wantAdv) {
		t.Fatalf("advSecretKey mismatch:\n got %x\nwant %x", res.advSecretKey, wantAdv)
	}

	// Inspect & decrypt the wrapped key bundle.
	reg, ok := childByTag(res.iq, "link_code_companion_reg")
	if !ok {
		t.Fatalf("missing link_code_companion_reg")
	}
	if reg.Attrs["stage"] != "companion_finish" {
		t.Fatalf("stage = %q, want companion_finish", reg.Attrs["stage"])
	}
	if reg.Attrs["jid"] != "5511999998888@s.whatsapp.net" {
		t.Fatalf("jid = %q", reg.Attrs["jid"])
	}
	kids := children(reg)
	wantTags := []string{"link_code_pairing_wrapped_key_bundle", "companion_identity_public", "link_code_pairing_ref"}
	if len(kids) != len(wantTags) {
		t.Fatalf("reg has %d children, want %d", len(kids), len(wantTags))
	}
	for i, want := range wantTags {
		if kids[i].Tag != want {
			t.Fatalf("child %d = %q, want %q", i, kids[i].Tag, want)
		}
	}
	refNode, _ := childByTag(reg, "link_code_pairing_ref")
	if string(nodeBytes(refNode)) != string(ref) {
		t.Fatalf("ref echoed = %q, want %q", nodeBytes(refNode), ref)
	}
	idPubNode, _ := childByTag(reg, "companion_identity_public")
	if string(nodeBytes(idPubNode)) != string(identity.Pub[:]) {
		t.Fatalf("companion_identity_public mismatch")
	}

	bundleNode, _ := childByTag(reg, "link_code_pairing_wrapped_key_bundle")
	bundle := nodeBytes(bundleNode)
	// bundle = linkCodeSalt(32) || iv(12) || ciphertext+tag.
	if string(bundle[:32]) != string(linkCodeSalt) {
		t.Fatalf("bundle salt mismatch")
	}
	if string(bundle[32:44]) != string(encryptIv) {
		t.Fatalf("bundle iv mismatch")
	}
	expanded, err := hkdfSHA256(companionShared, 32, linkCodeSalt, []byte("link_code_pairing_key_bundle_encryption_key"))
	if err != nil {
		t.Fatalf("expand hkdf: %v", err)
	}
	block, err := aes.NewCipher(expanded)
	if err != nil {
		t.Fatalf("aes: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("gcm: %v", err)
	}
	plain, err := gcm.Open(nil, encryptIv, bundle[44:], nil)
	if err != nil {
		t.Fatalf("gcm open: %v", err)
	}
	wantPayload := concat(identity.Pub[:], primaryIdentity.Pub[:], random)
	if string(plain) != string(wantPayload) {
		t.Fatalf("decrypted payload mismatch")
	}
}

// TestDecipherLinkPublicKey_InverseOfWrap verifies decipherLinkPublicKey undoes
// wrapCompanionEphemeral (the wrap/unwrap round-trip).
func TestDecipherLinkPublicKey_InverseOfWrap(t *testing.T) {
	const code = "WAGOTEST"
	pub := bytesSeq(0x01, 32)
	salt := bytesSeq(0x10, 32)
	iv := bytesSeq(0x40, 16)
	wrapped, err := wrapCompanionEphemeral(code, pub, salt, iv)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	got, err := decipherLinkPublicKey(code, wrapped)
	if err != nil {
		t.Fatalf("decipher: %v", err)
	}
	if string(got) != string(pub) {
		t.Fatalf("decipher = %x, want %x", got, pub)
	}
	if _, err := decipherLinkPublicKey(code, wrapped[:40]); err == nil {
		t.Fatal("expected error on short input")
	}
}

// bytesSeq returns n bytes starting at start and incrementing (wrapping at 256).
func bytesSeq(start byte, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = start + byte(i)
	}
	return b
}
