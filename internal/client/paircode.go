package client

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"

	"github.com/felipeleal/wa-go/internal/store"
	"github.com/felipeleal/wa-go/internal/wire"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
	"golang.org/x/crypto/pbkdf2"
)

// Pairing-by-code (a.k.a. "link with phone number"): instead of scanning a QR,
// the user types an 8-character code on their phone. This file implements the
// full crypto for that flow, mirroring Baileys: the REQUEST stage
// (companion_hello — lib/Socket/socket.js requestPairingCode/generatePairingKey,
// lib/Utils crypto.js derivePairingCodeKey/aesEncryptCTR, generics.js
// bytesToCrockford) and the FINISH stage (companion_finish — the
// link_code_companion_reg handler in lib/Socket/messages-recv.js: decipher the
// primary ephemeral pub, derive the companion shared key, build the
// link_code_pairing_wrapped_key_bundle, set advSecretKey, and reply
// stage=companion_finish).
//
// The wiring (driving these stages over a live connection) lives in pairing.go
// (pairingCodeLoop) and client.go (ConnectWithPairingCode). The finish crypto is
// validated offline by a wrap/unwrap round-trip + recomputed adv_secret
// (paircode_test.go); the actual network exchange requires a live phone entering
// the code. See docs/superpowers/specs/2026-06-22-pairing-code-design.md.

// crockfordAlphabet is the base32 alphabet used by WhatsApp, exactly as
// Baileys' bytesToCrockford (lib/Utils/generics.js): it starts at '1' (no
// leading '0') and omits I, O, U — but INCLUDES L. NOTE this is NOT the
// canonical Crockford alphabet (which starts at '0' and also drops L); it must
// match Baileys byte-for-byte.
const crockfordAlphabet = "123456789ABCDEFGHJKLMNPQRSTVWXYZ"

// crockfordEncode encodes b as a Crockford base32 string, identical to Baileys'
// bytesToCrockford. Bits are consumed MSB-first; a trailing partial group is
// left-padded with zero bits. For a 5-byte input this yields exactly 8 chars.
func crockfordEncode(b []byte) string {
	var value, bitCount int
	out := make([]byte, 0, (len(b)*8+4)/5)
	for _, c := range b {
		value = (value << 8) | int(c)
		bitCount += 8
		for bitCount >= 5 {
			out = append(out, crockfordAlphabet[(value>>(bitCount-5))&31])
			bitCount -= 5
		}
	}
	if bitCount > 0 {
		out = append(out, crockfordAlphabet[(value<<(5-bitCount))&31])
	}
	return string(out)
}

// pairingCodeIterations is the PBKDF2 iteration count Baileys uses (2 << 16).
const pairingCodeIterations = 2 << 16 // 131072

// derivePairingCodeKey derives the 32-byte AES key from the pairing code and
// salt, matching Baileys' derivePairingCodeKey: PBKDF2-HMAC-SHA256 with 131072
// iterations over the UTF-8 bytes of the code.
func derivePairingCodeKey(code string, salt []byte) []byte {
	return pbkdf2.Key([]byte(code), salt, pairingCodeIterations, 32, sha256.New)
}

// wrapCompanionEphemeral builds the link_code_pairing_wrapped_companion_ephemeral_pub
// blob, matching Baileys' generatePairingKey:
//
//	salt || iv || AES-256-CTR(ephemeralPub, derivePairingCodeKey(code, salt), iv)
//
// ephemeralPub is the companion's pairing ephemeral public key (32 bytes), salt
// is 32 bytes and iv is 16 bytes. AES-CTR is a stream cipher, so the ciphertext
// is the same length as ephemeralPub.
func wrapCompanionEphemeral(code string, ephemeralPub, salt, iv []byte) ([]byte, error) {
	key := derivePairingCodeKey(code, salt)
	block, err := aes.NewCipher(key) // AES-256 (32-byte key)
	if err != nil {
		return nil, fmt.Errorf("client: paircode aes cipher: %w", err)
	}
	ciphertext := make([]byte, len(ephemeralPub))
	cipher.NewCTR(block, iv).XORKeyStream(ciphertext, ephemeralPub)

	blob := make([]byte, 0, len(salt)+len(iv)+len(ciphertext))
	blob = append(blob, salt...)
	blob = append(blob, iv...)
	blob = append(blob, ciphertext...)
	return blob, nil
}

// GeneratePairingCode returns a fresh 8-character pairing code, matching
// Baileys' bytesToCrockford(randomBytes(5)). The user types this on their phone.
func GeneratePairingCode() (string, error) {
	var b [5]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("client: paircode random: %w", err)
	}
	return crockfordEncode(b[:]), nil
}

// companionHelloParams carries everything buildCompanionHelloIQ needs. Splitting
// the pure builder from the I/O method keeps it unit-testable with fixed
// salt/iv/ephemeral.
type companionHelloParams struct {
	iqID         string // iq id attribute
	jid          string // phoneNumber@s.whatsapp.net
	code         string // 8-char pairing code
	ephemeralPub []byte // companion pairing ephemeral public key (32 bytes)
	noisePub     []byte // companion_server_auth_key_pub = noiseKey.public
	salt         []byte // 32 bytes
	iv           []byte // 16 bytes
	platformID   string // companion_platform_id (web client type, e.g. "1")
	platformDisp string // companion_platform_display, e.g. "Chrome (Ubuntu)"
}

// buildCompanionHelloIQ builds the link_code_companion_reg stage=companion_hello
// iq, mirroring Baileys' requestPairingCode sendNode. It is a pure function of
// its params so tests can pin salt/iv/ephemeral and assert the wrapped blob.
//
// Structure:
//
//	<iq to=s.whatsapp.net type=set id=.. xmlns=md>
//	  <link_code_companion_reg jid=.. stage=companion_hello should_show_push_notification=true>
//	    <link_code_pairing_wrapped_companion_ephemeral_pub>{salt||iv||ciphertext}</...>
//	    <companion_server_auth_key_pub>{noisePub}</...>
//	    <companion_platform_id>{platformID}</...>
//	    <companion_platform_display>{platformDisp}</...>
//	    <link_code_pairing_nonce>0</...>
//	  </link_code_companion_reg>
//	</iq>
func buildCompanionHelloIQ(p companionHelloParams) (wire.Node, error) {
	wrapped, err := wrapCompanionEphemeral(p.code, p.ephemeralPub, p.salt, p.iv)
	if err != nil {
		return wire.Node{}, err
	}
	leaf := func(tag string, content []byte) wire.Node {
		return wire.Node{Tag: tag, Attrs: map[string]string{}, Content: content}
	}
	reg := wire.Node{
		Tag: "link_code_companion_reg",
		Attrs: map[string]string{
			"jid":                           p.jid,
			"stage":                         "companion_hello",
			"should_show_push_notification": "true",
		},
		Content: []wire.Node{
			leaf("link_code_pairing_wrapped_companion_ephemeral_pub", wrapped),
			leaf("companion_server_auth_key_pub", p.noisePub),
			leaf("companion_platform_id", []byte(p.platformID)),
			leaf("companion_platform_display", []byte(p.platformDisp)),
			leaf("link_code_pairing_nonce", []byte("0")),
		},
	}
	return wire.Node{
		Tag: "iq",
		Attrs: map[string]string{
			"to":    sWhatsAppNet,
			"type":  "set",
			"id":    p.iqID,
			"xmlns": "md",
		},
		Content: []wire.Node{reg},
	}, nil
}

// platformDisplay returns the companion_platform_display string for our device
// profile, matching Baileys' `${browser[1]} (${browser[0]})` — i.e.
// "<browser> (<os>)". For the default Chrome-on-Ubuntu tuple this is
// "Chrome (Ubuntu)".
func (c *Client) platformDisplay() string {
	// profile.Browser is {os, browser, osVersion}; Baileys uses browser[1]/[0].
	return fmt.Sprintf("%s (%s)", c.profile.Browser[1], c.profile.Browser[0])
}

// RequestPairingCode requests a pairing-by-code (link-with-phone-number) login.
// It generates the 8-char code, persists the pairing ephemeral key pair and the
// code into creds, and sends the companion_hello iq over the active pairing
// session. The returned code is what the user types on their phone.
//
// PRECONDITION: a pairing session must be active — i.e. the Noise handshake has
// completed but pair-success has NOT yet occurred. Pairing-by-code is an
// ALTERNATIVE to the QR flow: after the handshake, instead of waiting for the
// pair-device (QR) iq, the client proactively sends companion_hello. The QR loop
// is left untouched; callers opt into code pairing by driving this method on a
// session exposed for that purpose (see TODO on wiring below).
//
// LIVE-PENDING: the companion_finish half of the exchange — receiving the
// server's link_code_companion_reg response and completing ADV — is NOT done
// here (see finishCompanionPairing). Until that is wired live, this method only
// performs the REQUEST stage.
func (c *Client) RequestPairingCode(ctx context.Context, phoneNumber string) (string, error) {
	sess, ok := c.activeSession()
	if !ok {
		return "", fmt.Errorf("client: RequestPairingCode requires an active pairing session")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	creds := sess.creds

	code, err := GeneratePairingCode()
	if err != nil {
		return "", err
	}

	// Fresh pairing ephemeral key pair, persisted so companion_finish (live) can
	// derive the shared key from its private half.
	eph, err := c.newEphemeral()
	if err != nil {
		return "", fmt.Errorf("client: paircode ephemeral keypair: %w", err)
	}

	salt := make([]byte, 32)
	iv := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("client: paircode salt: %w", err)
	}
	if _, err := rand.Read(iv); err != nil {
		return "", fmt.Errorf("client: paircode iv: %w", err)
	}

	jid := phoneNumber + sWhatsAppNet

	iq, err := buildCompanionHelloIQ(companionHelloParams{
		iqID:         "wa-go-paircode-" + code,
		jid:          jid,
		code:         code,
		ephemeralPub: eph.Pub[:],
		noisePub:     creds.NoiseKey.Pub,
		salt:         salt,
		iv:           iv,
		platformID:   platformID(),
		platformDisp: c.platformDisplay(),
	})
	if err != nil {
		return "", err
	}

	// Persist the pairing ephemeral + code + me into creds before sending, so a
	// later companion_finish (which arrives asynchronously) can pick them up even
	// across a reconnect.
	creds.PairingEphemeral = store.CredKeyPair{
		Priv: append([]byte(nil), eph.Priv[:]...),
		Pub:  append([]byte(nil), eph.Pub[:]...),
	}
	creds.Me = jid
	if err := c.store.SaveCreds(creds); err != nil {
		return "", fmt.Errorf("client: save creds before companion_hello: %w", err)
	}

	if err := sess.send(iq); err != nil {
		return "", fmt.Errorf("client: send companion_hello: %w", err)
	}
	return code, nil
}

// decipherLinkPublicKey is the inverse of wrapCompanionEphemeral and mirrors
// Baileys' decipherLinkPublicKey (lib/Socket/messages-recv.js): it strips the
// salt(32)||iv(16) prefix and AES-256-CTR-decrypts the trailing 32 bytes with
// derivePairingCodeKey(code, salt). CTR decrypt == encrypt, so it reuses the
// same primitive as the wrap path. The 80-byte input layout is
// salt(32) || iv(16) || ciphertext(32).
func decipherLinkPublicKey(code string, wrapped []byte) ([]byte, error) {
	if len(wrapped) < 48 {
		return nil, fmt.Errorf("client: paircode wrapped key too short: %d bytes", len(wrapped))
	}
	salt := wrapped[:32]
	iv := wrapped[32:48]
	payload := wrapped[48:]

	key := derivePairingCodeKey(code, salt)
	block, err := aes.NewCipher(key) // AES-256 (32-byte key)
	if err != nil {
		return nil, fmt.Errorf("client: paircode aes cipher: %w", err)
	}
	plaintext := make([]byte, len(payload))
	cipher.NewCTR(block, iv).XORKeyStream(plaintext, payload)
	return plaintext, nil
}

// hkdfSHA256 expands ikm into length bytes with HKDF-SHA256(salt, info),
// matching Baileys' hkdf (whatsapp-rust-bridge): a single-shot
// Extract-and-Expand with the given salt and info. A nil/empty salt means the
// all-zeros block per RFC 5869.
func hkdfSHA256(ikm []byte, length int, salt, info []byte) ([]byte, error) {
	r := hkdf.New(sha256.New, ikm, salt, info)
	out := make([]byte, length)
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, fmt.Errorf("client: hkdf expand: %w", err)
	}
	return out, nil
}

// x25519 computes the X25519 shared secret, mirroring Baileys'
// Curve.sharedKey(priv, pub).
func x25519(priv, pub []byte) ([]byte, error) {
	return curve25519.X25519(priv, pub)
}

// companionFinishResult carries the outputs of the companion_finish crypto: the
// iq to send back and the advSecretKey to persist (which the subsequent
// pair-success HMAC verifies against).
type companionFinishResult struct {
	iq           wire.Node
	advSecretKey []byte
}

// companionFinishInput pins everything buildCompanionFinish needs so the crypto
// is a pure function of its inputs (random/salt/iv injectable for round-trip
// tests). Ports Baileys' link_code_companion_reg handler.
type companionFinishInput struct {
	code             string // the 8-char pairing code we issued
	pairingEphemPriv []byte // creds.PairingEphemeral.Priv (32 bytes)
	identityPriv     []byte // creds.IdentityKey.Priv (signedIdentityKey.private)
	identityPub      []byte // creds.IdentityKey.Pub  (signedIdentityKey.public)
	jid              string // creds.Me (link_code_companion_reg jid attr)
	iqID             string // outgoing iq id

	// From the server's <link_code_companion_reg> reply.
	ref                 []byte // link_code_pairing_ref (echoed back)
	primaryIdentityPub  []byte // primary_identity_pub (32 bytes)
	wrappedPrimaryEphem []byte // link_code_pairing_wrapped_primary_ephemeral_pub

	// Injectable randomness (filled with crypto/rand in the live path).
	random       []byte // 32 bytes
	linkCodeSalt []byte // 32 bytes
	encryptIv    []byte // 12 bytes (GCM nonce)
}

// buildCompanionFinish ports the cryptographic core of Baileys'
// link_code_companion_reg handler (lib/Socket/messages-recv.js ~881-942). It is
// a pure function so the wrap/unwrap and HKDF chain can be exercised offline via
// a round-trip test (the live network leg is the only untestable part).
//
// Steps (mirroring Baileys exactly):
//
//  1. codePairingPublicKey = decipherLinkPublicKey(wrappedPrimaryEphem).
//  2. companionSharedKey   = X25519(pairingEphemeral.priv, codePairingPublicKey).
//  3. linkCodePairingExpanded = HKDF-SHA256(companionSharedKey, 32,
//     salt=linkCodeSalt, info="link_code_pairing_key_bundle_encryption_key").
//  4. payload   = identityPub || primaryIdentityPub || random.
//  5. encrypted = AES-256-GCM(payload, linkCodePairingExpanded, encryptIv, aad=∅).
//     encryptedPayload = linkCodeSalt || encryptIv || encrypted.
//  6. identitySharedKey = X25519(identityPriv, primaryIdentityPub).
//  7. advSecretKey = HKDF-SHA256(companionSharedKey || identitySharedKey ||
//     random, 32, salt=∅, info="adv_secret").
//  8. iq = <link_code_companion_reg stage=companion_finish jid=me> with
//     link_code_pairing_wrapped_key_bundle, companion_identity_public, and the
//     echoed link_code_pairing_ref (in that order).
func buildCompanionFinish(in companionFinishInput) (companionFinishResult, error) {
	var res companionFinishResult

	// 1. Decipher the primary ephemeral public key the phone wrapped under the
	//    pairing code.
	codePairingPublicKey, err := decipherLinkPublicKey(in.code, in.wrappedPrimaryEphem)
	if err != nil {
		return res, err
	}

	// 2. companionSharedKey = X25519(our pairing ephemeral priv, codePairingPub).
	companionSharedKey, err := x25519(in.pairingEphemPriv, codePairingPublicKey)
	if err != nil {
		return res, fmt.Errorf("client: companionSharedKey x25519: %w", err)
	}

	// 3. Expand to the key-bundle encryption key.
	linkCodePairingExpanded, err := hkdfSHA256(
		companionSharedKey, 32, in.linkCodeSalt,
		[]byte("link_code_pairing_key_bundle_encryption_key"),
	)
	if err != nil {
		return res, err
	}

	// 4. payload = signedIdentityKey.public || primary_identity_pub || random.
	payload := concat(in.identityPub, in.primaryIdentityPub, in.random)

	// 5. AES-256-GCM with the 12-byte nonce, empty AAD; tag suffixed (Go's Seal,
	//    matching Baileys' aesEncryptGCM). wrappedKeyBundle = salt || iv || ct.
	block, err := aes.NewCipher(linkCodePairingExpanded)
	if err != nil {
		return res, fmt.Errorf("client: paircode gcm cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block) // 12-byte nonce
	if err != nil {
		return res, fmt.Errorf("client: paircode new gcm: %w", err)
	}
	encrypted := gcm.Seal(nil, in.encryptIv, payload, nil)
	wrappedKeyBundle := concat(in.linkCodeSalt, in.encryptIv, encrypted)

	// 6. identitySharedKey = X25519(signedIdentityKey.priv, primary_identity_pub).
	identitySharedKey, err := x25519(in.identityPriv, in.primaryIdentityPub)
	if err != nil {
		return res, fmt.Errorf("client: identitySharedKey x25519: %w", err)
	}

	// 7. advSecretKey = HKDF-SHA256(companionSharedKey || identitySharedKey ||
	//    random, 32, salt=∅, info="adv_secret").
	advSecretKey, err := hkdfSHA256(
		concat(companionSharedKey, identitySharedKey, in.random),
		32, nil, []byte("adv_secret"),
	)
	if err != nil {
		return res, err
	}

	// 8. Build the companion_finish iq.
	leaf := func(tag string, content []byte) wire.Node {
		return wire.Node{Tag: tag, Attrs: map[string]string{}, Content: content}
	}
	reg := wire.Node{
		Tag: "link_code_companion_reg",
		Attrs: map[string]string{
			"jid":   in.jid,
			"stage": "companion_finish",
		},
		Content: []wire.Node{
			leaf("link_code_pairing_wrapped_key_bundle", wrappedKeyBundle),
			leaf("companion_identity_public", append([]byte(nil), in.identityPub...)),
			leaf("link_code_pairing_ref", append([]byte(nil), in.ref...)),
		},
	}
	res.iq = wire.Node{
		Tag: "iq",
		Attrs: map[string]string{
			"to":    sWhatsAppNet,
			"type":  "set",
			"id":    in.iqID,
			"xmlns": "md",
		},
		Content: []wire.Node{reg},
	}
	res.advSecretKey = advSecretKey
	return res, nil
}

// finishCompanionPairing handles the server's <iq> carrying a
// <link_code_companion_reg> reply (the companion_finish stage). It reads the
// server's fields, generates the fresh randomness, runs buildCompanionFinish,
// PERSISTS the derived advSecretKey into creds (replacing the random one — the
// subsequent pair-success HMAC verifies against THIS value), and returns the iq
// to send back. After this exchange the server proceeds to the normal
// pair-success flow, which handlePairSuccess already implements.
//
// code is the 8-char pairing code issued by RequestPairingCode; it is held in
// memory by the pairing-code loop (it is not persisted in store.Creds) and is
// needed to decipher the phone's wrapped primary ephemeral public key.
func (c *Client) finishCompanionPairing(node wire.Node, creds *store.Creds, code string) (wire.Node, error) {
	reg, ok := childByTag(node, "link_code_companion_reg")
	if !ok {
		return wire.Node{}, fmt.Errorf("client: companion_finish: missing link_code_companion_reg child")
	}
	refNode, ok := childByTag(reg, "link_code_pairing_ref")
	if !ok {
		return wire.Node{}, fmt.Errorf("client: companion_finish: missing link_code_pairing_ref")
	}
	primNode, ok := childByTag(reg, "primary_identity_pub")
	if !ok {
		return wire.Node{}, fmt.Errorf("client: companion_finish: missing primary_identity_pub")
	}
	wrappedNode, ok := childByTag(reg, "link_code_pairing_wrapped_primary_ephemeral_pub")
	if !ok {
		return wire.Node{}, fmt.Errorf("client: companion_finish: missing link_code_pairing_wrapped_primary_ephemeral_pub")
	}

	if len(creds.PairingEphemeral.Priv) != 32 {
		return wire.Node{}, fmt.Errorf("client: companion_finish: pairing ephemeral key not present (RequestPairingCode must run first)")
	}
	if code == "" {
		return wire.Node{}, fmt.Errorf("client: companion_finish: empty pairing code")
	}

	random := make([]byte, 32)
	linkCodeSalt := make([]byte, 32)
	encryptIv := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return wire.Node{}, fmt.Errorf("client: companion_finish random: %w", err)
	}
	if _, err := rand.Read(linkCodeSalt); err != nil {
		return wire.Node{}, fmt.Errorf("client: companion_finish salt: %w", err)
	}
	if _, err := rand.Read(encryptIv); err != nil {
		return wire.Node{}, fmt.Errorf("client: companion_finish iv: %w", err)
	}

	res, err := buildCompanionFinish(companionFinishInput{
		code:                code,
		pairingEphemPriv:    creds.PairingEphemeral.Priv,
		identityPriv:        creds.IdentityKey.Priv,
		identityPub:         creds.IdentityKey.Pub,
		jid:                 creds.Me,
		iqID:                node.Attrs["id"],
		ref:                 nodeBytes(refNode),
		primaryIdentityPub:  nodeBytes(primNode),
		wrappedPrimaryEphem: nodeBytes(wrappedNode),
		random:              random,
		linkCodeSalt:        linkCodeSalt,
		encryptIv:           encryptIv,
	})
	if err != nil {
		return wire.Node{}, err
	}

	// Persist the derived advSecretKey before replying so a reconnect cannot lose
	// it; the pair-success HMAC that follows verifies against it.
	creds.AdvSecret = res.advSecretKey
	if err := c.store.SaveCreds(creds); err != nil {
		return wire.Node{}, fmt.Errorf("client: save creds after companion_finish: %w", err)
	}
	return res.iq, nil
}
