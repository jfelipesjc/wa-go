package signal

import (
	"crypto/hmac"
	"crypto/sha256"
	"io"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"

	"github.com/felipeleal/wa-go/internal/keys"
)

// KDF info strings, matching libsignal/WhatsApp exactly.
const (
	infoWhisperText        = "WhisperText"
	infoWhisperRatchet     = "WhisperRatchet"
	infoWhisperMessageKeys = "WhisperMessageKeys"
)

// messageKeys are the symmetric keys derived from a chain key for a single
// message: AES-256-CBC cipher key, HMAC-SHA256 mac key, and CBC IV.
type messageKeys struct {
	cipherKey [32]byte
	macKey    [32]byte
	iv        [16]byte
	counter   uint32
}

// dh computes X25519(priv, pub) where pub is a 33-byte 0x05-prefixed public key.
// The 0x05 prefix is stripped before the curve operation; curve25519 operates on
// 32 raw bytes only.
func dh(priv [32]byte, pub33 [signalKeyLen]byte) ([32]byte, error) {
	var out [32]byte
	shared, err := curve25519.X25519(priv[:], pub33[1:])
	if err != nil {
		return out, err
	}
	copy(out[:], shared)
	return out, nil
}

// hkdfSplit runs HKDF-SHA256 over ikm with the given salt and info, producing
// outLen bytes. WhatsApp uses a 32-byte zero salt for the initial/master and
// message-keys derivations, and the current root key as salt for the ratchet.
func hkdfSplit(ikm, salt []byte, info string, outLen int) []byte {
	r := hkdf.New(sha256.New, ikm, salt, []byte(info))
	out := make([]byte, outLen)
	if _, err := io.ReadFull(r, out); err != nil {
		// HKDF over fixed-length material cannot fail for these sizes.
		panic("signal: hkdf read: " + err.Error())
	}
	return out
}

// deriveInitialRootChain runs the X3DH master-secret KDF:
//
//	HKDF-SHA256(master, salt=zeros[32], info="WhisperText", L=64) -> rootKey || chainKey
//
// master is the concatenation (0xFF*32) || DH1 || DH2 || DH3 || DH4.
func deriveInitialRootChain(master []byte) (rootKey, chainKey [32]byte) {
	salt := make([]byte, 32)
	out := hkdfSplit(master, salt, infoWhisperText, 64)
	copy(rootKey[:], out[:32])
	copy(chainKey[:], out[32:])
	return
}

// rootKDF performs one DH-ratchet step:
//
//	dhOut = X25519(myRatchetPriv, theirRatchetPub)
//	HKDF-SHA256(ikm=dhOut, salt=rootKey, info="WhisperRatchet", L=64)
//	  -> newRootKey || newChainKey
//
// This matches libsignal's RootKey.createChain (input=dhOut, salt=rootKey).
func rootKDF(rootKey [32]byte, myRatchetPriv [32]byte, theirRatchetPub [signalKeyLen]byte) (newRoot, newChain [32]byte, err error) {
	dhOut, err := dh(myRatchetPriv, theirRatchetPub)
	if err != nil {
		return newRoot, newChain, err
	}
	out := hkdfSplit(dhOut[:], rootKey[:], infoWhisperRatchet, 64)
	copy(newRoot[:], out[:32])
	copy(newChain[:], out[32:])
	return newRoot, newChain, nil
}

// chainKeyNext advances a chain key: HMAC-SHA256(chainKey, 0x02).
func chainKeyNext(chainKey [32]byte) [32]byte {
	mac := hmac.New(sha256.New, chainKey[:])
	mac.Write([]byte{0x02})
	var out [32]byte
	copy(out[:], mac.Sum(nil))
	return out
}

// deriveMessageKeys derives the per-message keys from a chain key:
//
//	msgKeyMaterial = HMAC-SHA256(chainKey, 0x01)
//	HKDF-SHA256(msgKeyMaterial, salt=zeros[32], info="WhisperMessageKeys", L=80)
//	  -> cipherKey[32] || macKey[32] || iv[16]
func deriveMessageKeys(chainKey [32]byte, counter uint32) messageKeys {
	mac := hmac.New(sha256.New, chainKey[:])
	mac.Write([]byte{0x01})
	material := mac.Sum(nil)

	salt := make([]byte, 32)
	out := hkdfSplit(material, salt, infoWhisperMessageKeys, 80)

	var mk messageKeys
	copy(mk.cipherKey[:], out[:32])
	copy(mk.macKey[:], out[32:64])
	copy(mk.iv[:], out[64:80])
	mk.counter = counter
	return mk
}

// pubKeyOf returns the 33-byte 0x05-prefixed public key for a key pair.
func pubKeyOf(kp keys.KeyPair) [signalKeyLen]byte {
	var out [signalKeyLen]byte
	out[0] = 0x05
	copy(out[1:], kp.Pub[:])
	return out
}
