package client

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"fmt"

	"github.com/felipeleal/wa-go/internal/store"
	"github.com/felipeleal/wa-go/internal/wire"
	"golang.org/x/crypto/pbkdf2"
)

// Pairing-by-code (a.k.a. "link with phone number"): instead of scanning a QR,
// the user types an 8-character code on their phone. This file implements the
// CRYPTO and the REQUEST stage (companion_hello) of that flow, mirroring Baileys
// (lib/Socket/socket.js requestPairingCode/generatePairingKey, lib/Utils
// crypto.js derivePairingCodeKey/aesEncryptCTR, generics.js bytesToCrockford).
//
// LIVE-PENDING — the companion_finish stage (handling the server's
// link_code_companion_reg response: decipher the primary ephemeral pub, derive
// the companion shared key, build the link_code_pairing_wrapped_key_bundle, set
// advSecretKey, and reply stage=companion_finish) is NOT implemented here. It
// can only be exercised against a live WhatsApp server + phone, so it is left as
// a TODO. See finishCompanionPairing below and
// docs/superpowers/specs/2026-06-22-pairing-code-design.md.

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

// finishCompanionPairing is the LIVE-PENDING companion_finish stage. It is a
// placeholder documenting the work that must be done against a live server.
//
// TODO(live): implement the companion_finish exchange. When the server replies
// with an <iq> carrying a <link_code_companion_reg> child, do (per Baileys
// lib/Socket/messages-recv.js, the "link_code_companion_reg" case):
//
//  1. Read link_code_pairing_ref, primary_identity_pub, and
//     link_code_pairing_wrapped_primary_ephemeral_pub.
//  2. codePairingPublicKey = decipherLinkPublicKey(wrappedPrimaryEphemeralPub),
//     where decipherLinkPublicKey strips the salt||iv prefix and AES-CTR-decrypts
//     with derivePairingCodeKey(code, salt) — the inverse of wrapCompanionEphemeral.
//  3. companionSharedKey = X25519(pairingEphemeral.priv, codePairingPublicKey).
//  4. random = 32 random bytes; linkCodeSalt = 32 random bytes.
//  5. linkCodePairingExpanded = HKDF-SHA256(companionSharedKey, 32,
//     salt=linkCodeSalt, info="link_code_pairing_key_bundle_encryption_key").
//  6. payload = signedIdentityKey.pub || primaryIdentityPub || random.
//  7. encrypted = AES-256-GCM(payload, linkCodePairingExpanded, iv=12 random
//     bytes, aad=empty); wrappedKeyBundle = linkCodeSalt || iv || encrypted.
//  8. identitySharedKey = X25519(signedIdentityKey.priv, primaryIdentityPub).
//  9. advSecretKey = HKDF-SHA256(companionSharedKey || identitySharedKey ||
//     random, 32, info="adv_secret"); persist it (it replaces the random
//     advSecret — pair-success HMAC verifies against THIS value).
//  10. Reply <iq type=set> with <link_code_companion_reg stage=companion_finish>
//     containing link_code_pairing_wrapped_key_bundle (=wrappedKeyBundle),
//     companion_identity_public (=signedIdentityKey.pub), and
//     link_code_pairing_ref (echoed).
//
// After companion_finish the server proceeds to the normal pair-success
// exchange, which handlePairSuccess already implements (the advSecretKey from
// step 9 makes the HMAC verify). This requires a live phone entering the code,
// so it cannot be unit-tested offline.
func (c *Client) finishCompanionPairing() error {
	return fmt.Errorf("client: companion_finish is LIVE-PENDING (not yet implemented)")
}
