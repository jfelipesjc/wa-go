package wire

import (
	"bytes"
	"compress/zlib"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

// zlibInflate decompresses a zlib stream (used for the 0x02-prefixed frame
// payloads).
func zlibInflate(b []byte) ([]byte, error) {
	zr, err := zlib.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return io.ReadAll(zr)
}

// noiseMode is the protocol name for the WhatsApp Noise handshake. It is
// exactly 32 bytes ("Noise_XX_25519_AESGCM_SHA256" + 4 NUL bytes), so it is
// used directly as the initial hash rather than being hashed first.
var noiseMode = []byte("Noise_XX_25519_AESGCM_SHA256\x00\x00\x00\x00")

// noiseWAHeader is the WhatsApp routing header (NOISE_WA_HEADER): "WA" 0x06 0x03
// where 0x03 is DICT_VERSION.
var noiseWAHeader = []byte{0x57, 0x41, 0x06, 0x03}

// Noise implements the client side of the WhatsApp Noise_XX_25519_AESGCM_SHA256
// handshake and the resulting transport cipher state. It is a faithful port of
// @whiskeysockets/baileys/lib/Utils/noise-handler.js.
type Noise struct {
	hash   []byte
	salt   []byte
	encKey []byte
	decKey []byte

	// counter is the symmetric-state nonce counter, reset to 0 by mixKey.
	counter uint32

	// transport, once set by finish(), takes over from the handshake cipher.
	transport *transportState
}

// transportState is the post-handshake cipher with independent read/write
// counters, matching Baileys' TransportState.
type transportState struct {
	encKey       []byte
	decKey       []byte
	readCounter  uint32
	writeCounter uint32
}

// newNoise constructs a fresh handshake state and mixes in the WA header and the
// client's EPHEMERAL public key, exactly as makeNoiseHandler does at the end of
// construction (authenticate(NOISE_HEADER); authenticate(publicKey)).
//
// IMPORTANT: in Baileys, makeNoiseHandler is called with keyPair=ephemeralKeyPair
// (socket.js: `keyPair: ephemeralKeyPair`), so the `publicKey` authenticated here
// is the EPHEMERAL public key — NOT the static/noise key. The static key is mixed
// in later, only when its ciphertext is produced in step 6 of the handshake.
//
// ephemeralPub is the client's ephemeral public key.
func newNoise(ephemeralPub []byte) *Noise {
	h := make([]byte, 32)
	copy(h, noiseMode) // noiseMode is exactly 32 bytes → used directly.
	n := &Noise{
		hash:   h,
		salt:   append([]byte(nil), h...),
		encKey: append([]byte(nil), h...),
		decKey: append([]byte(nil), h...),
	}
	n.mixHash(noiseWAHeader)
	n.mixHash(ephemeralPub)
	return n
}

// mixHash sets hash = SHA256(hash || data).
func (n *Noise) mixHash(data []byte) {
	h := sha256.New()
	h.Write(n.hash)
	h.Write(data)
	n.hash = h.Sum(nil)
}

// mixKey runs HKDF-SHA256(ikm=data, salt=salt) → 64 bytes, splits into
// (write, read); salt=write, encKey=read, decKey=read, counter=0.
func (n *Noise) mixKey(data []byte) error {
	out := make([]byte, 64)
	r := hkdf.New(sha256.New, data, n.salt, nil)
	if _, err := io.ReadFull(r, out); err != nil {
		return fmt.Errorf("noise: hkdf: %w", err)
	}
	n.salt = out[:32]
	n.encKey = out[32:]
	n.decKey = out[32:]
	n.counter = 0
	return nil
}

// generateIV builds the 12-byte GCM nonce: 8 zero bytes followed by the counter
// as a 4-byte big-endian integer at offset 8 (DataView.setUint32(8, counter)).
func generateIV(counter uint32) []byte {
	iv := make([]byte, 12)
	binary.BigEndian.PutUint32(iv[8:], counter)
	return iv
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// encrypt performs AES-256-GCM with the handshake encKey, nonce=generateIV(counter),
// AAD=hash; then mixHash(ciphertext) and counter++.
func (n *Noise) encrypt(plaintext []byte) ([]byte, error) {
	if n.transport != nil {
		return n.transport.encrypt(plaintext)
	}
	aead, err := newGCM(n.encKey)
	if err != nil {
		return nil, err
	}
	ct := aead.Seal(nil, generateIV(n.counter), plaintext, n.hash)
	n.counter++
	n.mixHash(ct)
	return ct, nil
}

// decrypt performs AES-256-GCM open with the handshake decKey, nonce=generateIV(counter),
// AAD=hash; then mixHash(ciphertext-from-input) and counter++.
func (n *Noise) decrypt(ciphertext []byte) ([]byte, error) {
	if n.transport != nil {
		return n.transport.decrypt(ciphertext)
	}
	aead, err := newGCM(n.decKey)
	if err != nil {
		return nil, err
	}
	pt, err := aead.Open(nil, generateIV(n.counter), ciphertext, n.hash)
	if err != nil {
		return nil, fmt.Errorf("noise: decrypt: %w", err)
	}
	n.counter++
	n.mixHash(ciphertext)
	return pt, nil
}

// finish derives the final transport keys via HKDF over an empty input (the
// "split" step), switching the Noise object into transport mode. The first
// returned key is the write (send) key, the second the read (recv) key.
func (n *Noise) finish() (writeKey, readKey []byte, err error) {
	out := make([]byte, 64)
	r := hkdf.New(sha256.New, []byte{}, n.salt, nil)
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, nil, fmt.Errorf("noise: finish hkdf: %w", err)
	}
	writeKey = out[:32]
	readKey = out[32:]
	n.transport = &transportState{
		encKey: writeKey,
		decKey: readKey,
	}
	return writeKey, readKey, nil
}

func (t *transportState) encrypt(plaintext []byte) ([]byte, error) {
	aead, err := newGCM(t.encKey)
	if err != nil {
		return nil, err
	}
	ct := aead.Seal(nil, generateIV(t.writeCounter), plaintext, nil)
	t.writeCounter++
	return ct, nil
}

func (t *transportState) decrypt(ciphertext []byte) ([]byte, error) {
	aead, err := newGCM(t.decKey)
	if err != nil {
		return nil, err
	}
	pt, err := aead.Open(nil, generateIV(t.readCounter), ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("noise: transport decrypt: %w", err)
	}
	t.readCounter++
	return pt, nil
}

// dh computes the X25519 shared secret between a private and public key.
func dh(priv, pub []byte) ([]byte, error) {
	return curve25519.X25519(priv, pub)
}

// HandshakeResult holds the outputs of the client-side XX handshake.
type HandshakeResult struct {
	// ClientHello is the full ClientHello payload (HandshakeMessage protobuf,
	// without the WA header or length prefix).
	ClientHello []byte
	// ClientFinish is the full ClientFinish payload (HandshakeMessage protobuf).
	ClientFinish []byte
	// ServerStaticPub is the decrypted server static public key (32 bytes).
	ServerStaticPub []byte
	// ServerPayload is the decrypted server payload (cert chain).
	ServerPayload []byte
	// WriteKey / ReadKey are the final transport keys.
	WriteKey []byte
	ReadKey  []byte
}

// ClientHandshake runs the Noise_XX client handshake to completion using an
// injected ephemeral key pair (for deterministic reproduction of a trace).
//
// Inputs:
//   - ephemeralPriv/ephemeralPub: client ephemeral key pair (e)
//   - staticPriv/staticPub:       client static (auth/noise) key pair (s)
//   - serverHelloMsg:             raw HandshakeMessage protobuf bytes from the
//     ServerHello frame (after stripping the 3-byte length prefix)
//   - clientPayload:              plaintext ClientPayload to encrypt as the
//     ClientFinish payload
//
// It returns the produced ClientHello / ClientFinish bytes and the derived
// transport keys.
func (n *Noise) ClientHandshake(
	ephemeralPriv, ephemeralPub,
	staticPriv, staticPub,
	serverHelloMsg, clientPayload []byte,
) (*HandshakeResult, error) {
	res := &HandshakeResult{}

	// 1. ClientHello = HandshakeMessage{clientHello:{ephemeral}}.
	// NOTE: the client ephemeral pub is already mixed into the hash by newNoise
	// (it is the `publicKey` passed to makeNoiseHandler), so we do NOT mixHash it
	// again here. This is a WA-specific deviation from textbook Noise XX where the
	// initiator would mixHash(e) when sending the first message.
	res.ClientHello = encodeClientHello(ephemeralPub)

	// 2. Parse ServerHello {ephemeral, static, payload}.
	sh, err := parseHandshakeMessage(serverHelloMsg, 3)
	if err != nil {
		return nil, err
	}
	serverEphemeral := sh.f1
	serverStaticEnc := sh.f2
	serverPayloadEnc := sh.f3

	// 3. mixHash(re); mixKey(DH(e, re)).
	n.mixHash(serverEphemeral)
	es, err := dh(ephemeralPriv, serverEphemeral)
	if err != nil {
		return nil, fmt.Errorf("noise: DH(e,re): %w", err)
	}
	if err := n.mixKey(es); err != nil {
		return nil, err
	}

	// 4. decrypt server static; mixKey(DH(e, rs)).
	serverStatic, err := n.decrypt(serverStaticEnc)
	if err != nil {
		return nil, fmt.Errorf("noise: decrypt server static: %w", err)
	}
	res.ServerStaticPub = serverStatic
	ss, err := dh(ephemeralPriv, serverStatic)
	if err != nil {
		return nil, fmt.Errorf("noise: DH(e,rs): %w", err)
	}
	if err := n.mixKey(ss); err != nil {
		return nil, err
	}

	// 5. decrypt server payload (cert chain).
	serverPayload, err := n.decrypt(serverPayloadEnc)
	if err != nil {
		return nil, fmt.Errorf("noise: decrypt server payload: %w", err)
	}
	res.ServerPayload = serverPayload

	// 6. ClientFinish static = encrypt(s.pub); mixKey(DH(s, re)).
	staticEnc, err := n.encrypt(staticPub)
	if err != nil {
		return nil, fmt.Errorf("noise: encrypt client static: %w", err)
	}
	se, err := dh(staticPriv, serverEphemeral)
	if err != nil {
		return nil, fmt.Errorf("noise: DH(s,re): %w", err)
	}
	if err := n.mixKey(se); err != nil {
		return nil, err
	}

	// 7. ClientFinish payload = encrypt(clientPayload).
	payloadEnc, err := n.encrypt(clientPayload)
	if err != nil {
		return nil, fmt.Errorf("noise: encrypt client payload: %w", err)
	}
	res.ClientFinish = encodeClientFinish(staticEnc, payloadEnc)

	// 8. split → transport keys.
	wk, rk, err := n.finish()
	if err != nil {
		return nil, err
	}
	res.WriteKey = wk
	res.ReadKey = rk
	return res, nil
}
