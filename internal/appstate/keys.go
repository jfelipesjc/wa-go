package appstate

import (
	"crypto/sha256"

	"golang.org/x/crypto/hkdf"
)

// mutationKeysInfo is the HKDF info string used to expand an appStateSyncKey
// into the five mutation keys.
const mutationKeysInfo = "WhatsApp Mutation Keys"

// MutationKeys are the five keys derived from a single appStateSyncKey. The
// derivation is HKDF-SHA256(ikm=appStateSyncKey, salt=empty, info="WhatsApp
// Mutation Keys") expanded to 160 bytes and split into 5 contiguous 32-byte
// chunks, in this exact order (see mutationKeys / expandAppStateKeys in
// Baileys' chat-utils.js + whatsapp-rust-bridge).
type MutationKeys struct {
	IndexKey           []byte // HMAC-SHA256 key over the JSON index
	ValueEncryptionKey []byte // AES-256-CBC key for the value blob
	ValueMacKey        []byte // HMAC-SHA512 key for the value MAC
	SnapshotMacKey     []byte // HMAC-SHA256 key for the snapshot MAC
	PatchMacKey        []byte // HMAC-SHA256 key for the patch MAC
}

// DeriveMutationKeys expands a 32-byte appStateSyncKey into the five mutation
// keys.
func DeriveMutationKeys(appStateSyncKey []byte) MutationKeys {
	r := hkdf.New(sha256.New, appStateSyncKey, nil, []byte(mutationKeysInfo))
	expanded := make([]byte, 160)
	if _, err := readFull(r, expanded); err != nil {
		panic("appstate: hkdf mutation keys failed: " + err.Error())
	}
	return MutationKeys{
		IndexKey:           expanded[0:32],
		ValueEncryptionKey: expanded[32:64],
		ValueMacKey:        expanded[64:96],
		SnapshotMacKey:     expanded[96:128],
		PatchMacKey:        expanded[128:160],
	}
}
