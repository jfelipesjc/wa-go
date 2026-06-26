package appstate

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"

	waproto "github.com/jfelipesjc/wa-go/internal/waproto"
	"google.golang.org/protobuf/proto"
)

// MutationToEncode describes a single app-state change to be encoded into a
// SyncdPatch. It is the encode-side mirror of the decoded Mutation: Index is the
// JSON index array (e.g. ["mute", "<jid>"]), Action is the SyncActionValue, and
// APIVersion is the SyncActionData.version field (Baileys' apiVersion per
// action type). Operation is SET or REMOVE.
type MutationToEncode struct {
	Operation  waproto.SyncdMutation_SyncdOperation
	Index      []string
	Action     *waproto.SyncActionValue
	APIVersion int32
}

// EncodePatch is the inverse of DecodePatch: it serializes and encrypts each
// mutation, folds them into the LTHash, computes every MAC, and assembles a
// SyncdPatch protobuf ready to be wrapped in the w:sync:app:state iq.
//
// For each mutation it:
//   - marshals SyncActionData{index, value, padding, version},
//   - AES-256-CBC encrypts the marshaled bytes with a fresh random IV (the IV is
//     prepended to the ciphertext, matching the decode side which treats the
//     first 16 bytes of the blob as the IV),
//   - computes the valueMac with the SAME scheme the decoder verifies
//     (generateContentMac, op byte SET=0x00 / REMOVE=0x01),
//   - computes the indexMac = HMAC-SHA256(indexBytes, indexKey),
//   - folds the contribution into the LTHash (SET adds, both SET/REMOVE subtract
//     any previous contribution for the same index).
//
// It then bumps the version by 1, computes the snapshotMac over the new LTHash
// and the patchMac over (snapshotMac || valueMacs || version || name), and
// returns the encoded SyncdPatch bytes plus the new HashState.
//
// randReader supplies the per-value IV bytes; pass nil for crypto/rand. Tests
// inject a deterministic reader to make the output reproducible.
//
// EncodePatch guarantees round-trip consistency: DecodePatch over the returned
// patch (with the same key and starting state) reproduces the input mutations
// and yields the returned HashState.
func EncodePatch(name string, version uint64, keyIDB64 string, keys MutationKeys, prev *HashState, mutations []MutationToEncode, randReader io.Reader) (patchBytes []byte, newState *HashState, err error) {
	if randReader == nil {
		randReader = rand.Reader
	}
	if len(mutations) == 0 {
		return nil, nil, fmt.Errorf("appstate: EncodePatch requires at least one mutation")
	}
	encKeyID, err := decodeB64(keyIDB64)
	if err != nil {
		return nil, nil, fmt.Errorf("appstate: bad keyId base64: %w", err)
	}

	next := prev.clone()
	newVersion := version

	valueMacs := make([][]byte, 0, len(mutations))
	addBuffs := make([][]byte, 0, len(mutations))
	subBuffs := make([][]byte, 0)
	syncdMuts := make([]*waproto.SyncdMutation, 0, len(mutations))

	for _, m := range mutations {
		indexBytes, err := json.Marshal(m.Index)
		if err != nil {
			return nil, nil, fmt.Errorf("appstate: marshal index: %w", err)
		}

		sad := &waproto.SyncActionData{
			Index:   indexBytes,
			Value:   m.Action,
			Padding: []byte{},
			Version: proto.Int32(m.APIVersion),
		}
		plaintext, err := proto.Marshal(sad)
		if err != nil {
			return nil, nil, fmt.Errorf("appstate: marshal SyncActionData: %w", err)
		}

		encValue, err := aesCBCEncrypt(keys.ValueEncryptionKey, plaintext, randReader)
		if err != nil {
			return nil, nil, fmt.Errorf("appstate: encrypt: %w", err)
		}

		valueMac := generateContentMac(m.Operation, encValue, encKeyID, keys.ValueMacKey)
		indexMac := generateIndexMac(indexBytes, keys.IndexKey)

		blob := make([]byte, 0, len(encValue)+len(valueMac))
		blob = append(blob, encValue...)
		blob = append(blob, valueMac...)

		syncdMuts = append(syncdMuts, &waproto.SyncdMutation{
			Operation: m.Operation.Enum(),
			Record: &waproto.SyncdRecord{
				Index: &waproto.SyncdIndex{Blob: indexMac},
				Value: &waproto.SyncdValue{Blob: blob},
				KeyId: &waproto.KeyId{Id: encKeyID},
			},
		})
		valueMacs = append(valueMacs, valueMac)

		// Fold into the LTHash exactly as DecodePatch does.
		indexMacB64 := b64(indexMac)
		if prevVM, exists := next.indexValueMap[indexMacB64]; exists {
			subBuffs = append(subBuffs, prevVM)
		}
		if m.Operation == waproto.SyncdMutation_REMOVE {
			delete(next.indexValueMap, indexMacB64)
		} else {
			addBuffs = append(addBuffs, valueMac)
			vm := make([]byte, len(valueMac))
			copy(vm, valueMac)
			next.indexValueMap[indexMacB64] = vm
		}
	}

	next.Hash = subtractThenAdd(prev.Hash, subBuffs, addBuffs)
	newVersion++
	next.Version = newVersion

	snapshotMac := generateSnapshotMac(next.Hash, newVersion, name, keys.SnapshotMacKey)
	patchMac := generatePatchMac(snapshotMac, valueMacs, newVersion, name, keys.PatchMacKey)

	patch := &waproto.SyncdPatch{
		Version:     &waproto.SyncdVersion{Version: proto.Uint64(newVersion)},
		Mutations:   syncdMuts,
		SnapshotMac: snapshotMac,
		PatchMac:    patchMac,
		KeyId:       &waproto.KeyId{Id: encKeyID},
	}
	out, err := proto.Marshal(patch)
	if err != nil {
		return nil, nil, fmt.Errorf("appstate: marshal SyncdPatch: %w", err)
	}
	return out, next, nil
}

// aesCBCEncrypt encrypts plaintext with AES-256-CBC using a fresh random IV read
// from r, applies PKCS#7 padding, and returns IV || ciphertext (mirroring the
// IV-prefixed blob layout aesCBCDecrypt consumes).
func aesCBCEncrypt(key, plaintext []byte, r io.Reader) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	iv := make([]byte, aes.BlockSize)
	if _, err := io.ReadFull(r, iv); err != nil {
		return nil, fmt.Errorf("appstate: read iv: %w", err)
	}
	padded := pkcs7Pad(plaintext, aes.BlockSize)
	ct := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, padded)
	out := make([]byte, 0, len(iv)+len(ct))
	out = append(out, iv...)
	out = append(out, ct...)
	return out, nil
}

// pkcs7Pad appends PKCS#7 padding to a multiple of blockSize. A full padding
// block is appended when the input is already aligned, matching aesCBCDecrypt's
// pkcs7Unpad (which always strips 1..blockSize trailing bytes).
func pkcs7Pad(b []byte, blockSize int) []byte {
	pad := blockSize - len(b)%blockSize
	out := make([]byte, len(b)+pad)
	copy(out, b)
	for i := len(b); i < len(out); i++ {
		out[i] = byte(pad)
	}
	return out
}

// decodeB64 is the inverse of b64 (standard base64).
func decodeB64(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}
