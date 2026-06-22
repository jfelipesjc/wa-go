// Package keys generates the cryptographic identity of a WhatsApp multi-device
// "companion" client. The structures and generation logic mirror Baileys'
// initAuthCreds (harness/node_modules/@whiskeysockets/baileys/lib/Utils/auth-utils.js)
// so that creds produced here are wire-compatible with the WhatsApp pairing flow.
package keys

import (
	"crypto/rand"
	"crypto/sha512"
	"fmt"

	"filippo.io/edwards25519"
	"filippo.io/edwards25519/field"
	"golang.org/x/crypto/curve25519"
)

// KeyPair is a Curve25519 (X25519) key pair. Priv is the clamped private scalar
// and Pub is Priv * basepoint. This matches Baileys' Curve.generateKeyPair,
// which stores the 32-byte private key and the 32-byte public key (version byte
// stripped).
type KeyPair struct {
	Priv [32]byte
	Pub  [32]byte
}

// GenKeyPair generates a fresh Curve25519 key pair using crypto/rand. The
// private scalar is clamped per the Curve25519 spec (RFC 7748): the low 3 bits
// of the first byte are cleared, the top bit of the last byte is cleared, and
// the second-highest bit of the last byte is set. The public key is computed as
// X25519(priv, basepoint).
func GenKeyPair() (KeyPair, error) {
	var kp KeyPair
	if _, err := rand.Read(kp.Priv[:]); err != nil {
		return KeyPair{}, fmt.Errorf("keys: read random priv: %w", err)
	}
	clampCurve25519(&kp.Priv)

	pub, err := curve25519.X25519(kp.Priv[:], curve25519.Basepoint)
	if err != nil {
		return KeyPair{}, fmt.Errorf("keys: derive pub: %w", err)
	}
	copy(kp.Pub[:], pub)
	return kp, nil
}

// clampCurve25519 applies the standard Curve25519 scalar clamping in place.
func clampCurve25519(k *[32]byte) {
	k[0] &= 248
	k[31] &= 127
	k[31] |= 64
}

// NewRegistrationID returns a 14-bit registration ID, matching Baileys'
// generateRegistrationId in lib/Utils/generics.js:
//
//	Uint16Array.from(randomBytes(2))[0] & 16383
//
// i.e. a uniform value in [0, 16383] (14 bits). Note: the value can be 0; this
// faithfully mirrors Baileys (the WhatsApp server accepts it).
func NewRegistrationID() uint32 {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is catastrophic; surface it loudly rather than
		// returning a predictable ID.
		panic(fmt.Sprintf("keys: read random for registration id: %v", err))
	}
	// Little-endian, mirroring JS Uint16Array on a little-endian platform.
	v := uint16(b[0]) | uint16(b[1])<<8
	return uint32(v & 16383)
}

// NewAdvSecret returns 32 random bytes used as the advSecretKey (the HMAC key in
// the pair-success device-identity verification). Baileys stores this as a
// base64 string; here we keep the raw bytes and let the store layer encode them.
func NewAdvSecret() ([32]byte, error) {
	var s [32]byte
	if _, err := rand.Read(s[:]); err != nil {
		return [32]byte{}, fmt.Errorf("keys: read random adv secret: %w", err)
	}
	return s, nil
}

// SignedPreKey is a signed pre-key: a Curve25519 key pair whose public key is
// signed with the device identity key (XEdDSA / Curve25519 signature), mirroring
// Baileys' signedKeyPair.
type SignedPreKey struct {
	KeyID     uint32
	KeyPair   KeyPair
	Signature [64]byte
}

// GenSignedPreKey generates a signed pre-key with the given key ID, signing the
// pre-key's public key with the identity key.
//
// The signed message is the 33-byte "signal" public key: a 0x05 type prefix
// followed by the 32-byte Curve25519 public key. This matches Baileys
// (lib/Utils/crypto.js):
//
//	const pubKey = generateSignalPubKey(preKey.public) // 0x05 || pub
//	const signature = Curve.sign(identityKeyPair.private, pubKey)
func GenSignedPreKey(identity KeyPair, keyID uint32) (SignedPreKey, error) {
	pre, err := GenKeyPair()
	if err != nil {
		return SignedPreKey{}, err
	}
	msg := signalPubKey(pre.Pub)
	sig, err := xeddsaSign(identity.Priv, msg)
	if err != nil {
		return SignedPreKey{}, err
	}
	return SignedPreKey{KeyID: keyID, KeyPair: pre, Signature: sig}, nil
}

// signalPubKey prepends the 0x05 type byte to a 32-byte Curve25519 public key,
// matching libsignal's KEY_BUNDLE_TYPE / Baileys' generateSignalPubKey.
func signalPubKey(pub [32]byte) []byte {
	out := make([]byte, 33)
	out[0] = 0x05
	copy(out[1:], pub[:])
	return out
}

// Identity bundles everything generated for a fresh device, mirroring the subset
// of Baileys' initAuthCreds that #2/#3 need.
type Identity struct {
	NoiseKey       KeyPair
	IdentityKey    KeyPair
	RegistrationID uint32
	AdvSecret      [32]byte
	SignedPreKey   SignedPreKey
}

// NewIdentity generates a complete fresh device identity. The signed pre-key is
// generated with keyID 1, matching Baileys' signedKeyPair(identityKey, 1).
func NewIdentity() (Identity, error) {
	noise, err := GenKeyPair()
	if err != nil {
		return Identity{}, err
	}
	idk, err := GenKeyPair()
	if err != nil {
		return Identity{}, err
	}
	adv, err := NewAdvSecret()
	if err != nil {
		return Identity{}, err
	}
	spk, err := GenSignedPreKey(idk, 1)
	if err != nil {
		return Identity{}, err
	}
	return Identity{
		NoiseKey:       noise,
		IdentityKey:    idk,
		RegistrationID: NewRegistrationID(),
		AdvSecret:      adv,
		SignedPreKey:   spk,
	}, nil
}

// Sign produces an XEdDSA signature of msg with the raw 32-byte Curve25519
// private scalar, mirroring Baileys' Curve.sign(private, buf). It is the minimal
// exported wrapper the pairing flow (pair-success deviceSignature) needs.
func Sign(priv [32]byte, msg []byte) ([64]byte, error) {
	return xeddsaSign(priv, msg)
}

// Verify checks an XEdDSA signature of msg against the raw 32-byte Curve25519
// public key, mirroring Baileys' Curve.verify(pubKey, message, signature)
// (which internally prepends the 0x05 type byte). pub here is the raw 32-byte
// public key; verification handles the signal pub-key form internally.
func Verify(pub [32]byte, msg []byte, sig [64]byte) bool {
	return xeddsaVerify(pub, msg, sig)
}

// xeddsaSign produces a Curve25519 (XEdDSA) signature of msg using the
// Curve25519 private scalar montPriv, byte-for-byte compatible with
// curve25519-js's deterministic sign (the construction libsignal/Baileys use via
// Curve.sign).
//
// Reference: curve25519-js lib/index.js, curve25519_sign + crypto_sign_direct
// (https://github.com/digitalbazaar/x25519-key-agreement-key-2019 lineage):
//
//  1. Clamp the Curve25519 scalar to obtain the Ed25519 scalar a:
//     a[0]&=248; a[31]&=127; a[31]|=64.
//  2. A = a * B (Ed25519 basepoint). Remember signBit = A[31] & 0x80.
//  3. r = SHA512(a || M); R = r * B.
//  4. h = SHA512(R || A || M).
//  5. S = (r + h*a) mod L.
//  6. signature = R || S, then signature[63] |= signBit.
//
// Note this is the *deterministic* variant (Baileys' Curve.sign passes no random
// nonce), so the nonce r is derived directly from the scalar and message.
func xeddsaSign(montPriv [32]byte, msg []byte) ([64]byte, error) {
	var sig [64]byte

	// Step 1: clamp to the Ed25519 scalar.
	var aBytes [32]byte
	copy(aBytes[:], montPriv[:])
	aBytes[0] &= 248
	aBytes[31] &= 127
	aBytes[31] |= 64

	a, err := edwards25519.NewScalar().SetBytesWithClamping(aBytes[:])
	if err != nil {
		return sig, fmt.Errorf("keys: xeddsa set scalar: %w", err)
	}

	// Step 2: A = a*B; remember sign bit of the encoded point.
	A := (&edwards25519.Point{}).ScalarBaseMult(a)
	Abytes := A.Bytes()
	signBit := Abytes[31] & 0x80

	// Step 3: r = SHA512(a || M); R = r*B.
	hr := sha512.New()
	hr.Write(aBytes[:])
	hr.Write(msg)
	var rHash [64]byte
	hr.Sum(rHash[:0])
	r, err := edwards25519.NewScalar().SetUniformBytes(rHash[:])
	if err != nil {
		return sig, fmt.Errorf("keys: xeddsa reduce r: %w", err)
	}
	R := (&edwards25519.Point{}).ScalarBaseMult(r)
	Rbytes := R.Bytes()

	// Step 4: h = SHA512(R || A || M).
	hh := sha512.New()
	hh.Write(Rbytes)
	hh.Write(Abytes)
	hh.Write(msg)
	var hHash [64]byte
	hh.Sum(hHash[:0])
	h, err := edwards25519.NewScalar().SetUniformBytes(hHash[:])
	if err != nil {
		return sig, fmt.Errorf("keys: xeddsa reduce h: %w", err)
	}

	// Step 5: S = r + h*a mod L.
	S := edwards25519.NewScalar().MultiplyAdd(h, a, r)
	Sbytes := S.Bytes()

	// Step 6: signature = R || S with sign bit folded into the top bit of S.
	copy(sig[:32], Rbytes)
	copy(sig[32:], Sbytes)
	sig[63] |= signBit
	return sig, nil
}

// xeddsaVerify verifies an XEdDSA signature against the Curve25519 public key
// montPub, mirroring curve25519-js's verify (libsignal Curve.verify). It is the
// inverse of xeddsaSign and is used by tests to validate the construction.
//
//  1. Recover the Ed25519 public key A from the Montgomery public key u via the
//     birational map: y = (u-1)/(u+1). The sign (x) bit of A is taken from the
//     top bit of signature[63]; that bit is then cleared from S.
//  2. Standard Ed25519 verification: check S*B == R + h*A, where
//     h = SHA512(R || A || M).
func xeddsaVerify(montPub [32]byte, msg []byte, sig [64]byte) bool {
	A, ok := montgomeryToEdwards(montPub, sig[63]&0x80)
	if !ok {
		return false
	}

	var sigCopy [64]byte
	copy(sigCopy[:], sig[:])
	sigCopy[63] &= 0x7f // clear sign bit before reading S

	R, err := (&edwards25519.Point{}).SetBytes(sigCopy[:32])
	if err != nil {
		return false
	}
	S, err := edwards25519.NewScalar().SetCanonicalBytes(sigCopy[32:])
	if err != nil {
		return false
	}

	hh := sha512.New()
	hh.Write(sigCopy[:32])
	hh.Write(A.Bytes())
	hh.Write(msg)
	var hHash [64]byte
	hh.Sum(hHash[:0])
	h, err := edwards25519.NewScalar().SetUniformBytes(hHash[:])
	if err != nil {
		return false
	}

	// Check: [S]B = R + [h]A  <=>  R == [S]B - [h]A.
	negA := (&edwards25519.Point{}).Negate(A)
	rhs := (&edwards25519.Point{}).VarTimeDoubleScalarBaseMult(h, negA, S)
	return rhs.Equal(R) == 1
}

// montgomeryToEdwards converts a Montgomery u-coordinate (Curve25519 public key)
// to an Edwards point using y = (u-1)/(u+1), choosing the x sign from signBit
// (the top bit, 0x80, of the recovered point's encoding).
func montgomeryToEdwards(montPub [32]byte, signBit byte) (*edwards25519.Point, bool) {
	u, err := new(field.Element).SetBytes(montPub[:])
	if err != nil {
		return nil, false
	}
	one := new(field.Element).One()
	uMinus1 := new(field.Element).Subtract(u, one)
	uPlus1 := new(field.Element).Add(u, one)
	uPlus1Inv := new(field.Element).Invert(uPlus1)
	y := new(field.Element).Multiply(uMinus1, uPlus1Inv)

	yBytes := y.Bytes()
	// Fold the desired sign bit into the top bit of the y-encoding; SetBytes on
	// edwards25519.Point reads that bit as the sign of x.
	yBytes[31] |= signBit

	p, err := (&edwards25519.Point{}).SetBytes(yBytes)
	if err != nil {
		return nil, false
	}
	return p, true
}
