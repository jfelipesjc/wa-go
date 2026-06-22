package appstate

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	waproto "github.com/felipeleal/wa-go/internal/waproto"
	"google.golang.org/protobuf/proto"
)

// Mutation is a single decoded app-state change.
type Mutation struct {
	Operation waproto.SyncdMutation_SyncdOperation
	// Index is the decoded JSON index array, e.g. ["contact", "<jid>"] or
	// ["mute", "<jid>"] or ["setting_pushName"].
	Index []string
	// Action is the decoded SyncActionValue (contactAction, muteAction, ...).
	Action *waproto.SyncActionValue
}

// DecodeResult is the outcome of applying a patch.
type DecodeResult struct {
	// State is the new HashState after the patch (only returned when the patch
	// validated; on error State is unchanged).
	State *HashState
	// Mutations are the decoded mutations in patch order.
	Mutations []Mutation
}

// KeyResolver returns the raw 32-byte appStateSyncKey for a given keyId
// (base64-encoded), or false if it is not known yet.
type KeyResolver func(keyIDB64 string) ([]byte, bool)

var (
	// ErrMissingKey is returned when the appStateSyncKey referenced by the
	// patch is not available (the caller should park the collection until an
	// APP_STATE_SYNC_KEY_SHARE arrives).
	ErrMissingKey = errors.New("appstate: missing app state sync key")
	// ErrPatchMac is returned when the patch-level MAC does not verify.
	ErrPatchMac = errors.New("appstate: invalid patch mac")
	// ErrValueMac is returned when a record value MAC does not verify.
	ErrValueMac = errors.New("appstate: invalid value mac")
	// ErrIndexMac is returned when a record index MAC does not verify.
	ErrIndexMac = errors.New("appstate: invalid index mac")
	// ErrSnapshotMac is returned when the recomputed LTHash snapshot MAC does
	// not match the one carried by the patch (integrity failure).
	ErrSnapshotMac = errors.New("appstate: snapshot mac / LTHash mismatch")
)

// DecodePatch decodes and integrity-checks a single SyncdPatch for the named
// collection, applying it on top of prev. It verifies (in order) the patch MAC,
// each record's value MAC, each record's index MAC, decrypts and decodes each
// SyncActionData, folds the mutations into the LTHash, and finally checks the
// resulting snapshot MAC. On any integrity failure it returns an error and
// leaves prev untouched.
func DecodePatch(name string, patch *waproto.SyncdPatch, prev *HashState, resolve KeyResolver) (*DecodeResult, error) {
	if patch.GetKeyId() == nil {
		return nil, fmt.Errorf("appstate: patch missing keyId")
	}
	keyIDB64 := b64(patch.GetKeyId().GetId())
	rawKey, ok := resolve(keyIDB64)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrMissingKey, keyIDB64)
	}
	mk := DeriveMutationKeys(rawKey)

	version := patch.GetVersion().GetVersion()

	// 1. Patch MAC over snapshotMac || all valueMacs || version || name.
	valueMacs := make([][]byte, 0, len(patch.GetMutations()))
	for _, m := range patch.GetMutations() {
		blob := m.GetRecord().GetValue().GetBlob()
		if len(blob) < 32 {
			return nil, fmt.Errorf("appstate: value blob too short")
		}
		valueMacs = append(valueMacs, blob[len(blob)-32:])
	}
	wantPatchMac := generatePatchMac(patch.GetSnapshotMac(), valueMacs, version, name, mk.PatchMacKey)
	if !hmac.Equal(wantPatchMac, patch.GetPatchMac()) {
		return nil, ErrPatchMac
	}

	// 2. Decode each mutation, verifying value+index MACs, folding the LTHash.
	next := prev.clone()
	next.Version = version

	addBuffs := make([][]byte, 0, len(patch.GetMutations()))
	subBuffs := make([][]byte, 0)
	muts := make([]Mutation, 0, len(patch.GetMutations()))

	for _, m := range patch.GetMutations() {
		op := m.GetOperation()
		rec := m.GetRecord()
		blob := rec.GetValue().GetBlob()
		encContent := blob[:len(blob)-32]
		valueMac := blob[len(blob)-32:]

		// value MAC
		wantValueMac := generateContentMac(op, encContent, rec.GetKeyId().GetId(), mk.ValueMacKey)
		if !hmac.Equal(wantValueMac, valueMac) {
			return nil, ErrValueMac
		}

		// decrypt: blob = IV(16) || AES-256-CBC(PKCS7(SyncActionData))
		plaintext, err := aesCBCDecrypt(mk.ValueEncryptionKey, encContent)
		if err != nil {
			return nil, fmt.Errorf("appstate: decrypt: %w", err)
		}

		var sad waproto.SyncActionData
		if err := proto.Unmarshal(plaintext, &sad); err != nil {
			return nil, fmt.Errorf("appstate: unmarshal SyncActionData: %w", err)
		}

		// index MAC over the decoded index bytes
		wantIndexMac := generateIndexMac(sad.GetIndex(), mk.IndexKey)
		if !hmac.Equal(wantIndexMac, rec.GetIndex().GetBlob()) {
			return nil, ErrIndexMac
		}

		var idx []string
		if err := json.Unmarshal(sad.GetIndex(), &idx); err != nil {
			return nil, fmt.Errorf("appstate: parse index json: %w", err)
		}

		muts = append(muts, Mutation{
			Operation: op,
			Index:     idx,
			Action:    sad.GetValue(),
		})

		// Fold into the LTHash. SET adds the new valueMac; both SET and REMOVE
		// subtract any previously folded valueMac for the same index.
		indexMacB64 := b64(rec.GetIndex().GetBlob())
		if prevVM, exists := next.indexValueMap[indexMacB64]; exists {
			subBuffs = append(subBuffs, prevVM)
		}
		if op == waproto.SyncdMutation_REMOVE {
			delete(next.indexValueMap, indexMacB64)
		} else {
			addBuffs = append(addBuffs, valueMac)
			vm := make([]byte, len(valueMac))
			copy(vm, valueMac)
			next.indexValueMap[indexMacB64] = vm
		}
	}

	next.Hash = subtractThenAdd(prev.Hash, subBuffs, addBuffs)

	// 3. Snapshot MAC over the new LTHash (integrity / anti-tamper).
	wantSnapshotMac := generateSnapshotMac(next.Hash, version, name, mk.SnapshotMacKey)
	if !hmac.Equal(wantSnapshotMac, patch.GetSnapshotMac()) {
		return nil, ErrSnapshotMac
	}

	return &DecodeResult{State: next, Mutations: muts}, nil
}

// DecodeSnapshot decodes and integrity-checks a SyncdSnapshot for the named
// collection. A snapshot is the server's full materialization of a collection:
// every live record as an (always-SET) mutation. It mirrors Baileys'
// decodeSyncdSnapshot — fold every record's valueMac into a fresh LTHash, verify
// each record's value+index MAC, decode each SyncActionData, and finally check
// the snapshot MAC over the resulting LTHash.
//
// The returned HashState has Version = snapshot.version and an indexValueMap
// seeded from every record, so a subsequent DecodePatch chains correctly on top.
// On any integrity failure it returns an error and no state.
func DecodeSnapshot(name string, snap *waproto.SyncdSnapshot, resolve KeyResolver) (*DecodeResult, error) {
	if snap.GetKeyId() == nil {
		return nil, fmt.Errorf("appstate: snapshot missing keyId")
	}
	keyIDB64 := b64(snap.GetKeyId().GetId())
	rawKey, ok := resolve(keyIDB64)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrMissingKey, keyIDB64)
	}
	mk := DeriveMutationKeys(rawKey)
	version := snap.GetVersion().GetVersion()

	next := NewHashState()
	next.Version = version

	addBuffs := make([][]byte, 0, len(snap.GetRecords()))
	muts := make([]Mutation, 0, len(snap.GetRecords()))

	// Snapshot records are always SET (the live materialized value).
	const op = waproto.SyncdMutation_SET
	for _, rec := range snap.GetRecords() {
		blob := rec.GetValue().GetBlob()
		if len(blob) < 32 {
			return nil, fmt.Errorf("appstate: snapshot value blob too short")
		}
		encContent := blob[:len(blob)-32]
		valueMac := blob[len(blob)-32:]

		wantValueMac := generateContentMac(op, encContent, rec.GetKeyId().GetId(), mk.ValueMacKey)
		if !hmac.Equal(wantValueMac, valueMac) {
			return nil, ErrValueMac
		}

		plaintext, err := aesCBCDecrypt(mk.ValueEncryptionKey, encContent)
		if err != nil {
			return nil, fmt.Errorf("appstate: snapshot decrypt: %w", err)
		}
		var sad waproto.SyncActionData
		if err := proto.Unmarshal(plaintext, &sad); err != nil {
			return nil, fmt.Errorf("appstate: snapshot unmarshal SyncActionData: %w", err)
		}
		wantIndexMac := generateIndexMac(sad.GetIndex(), mk.IndexKey)
		if !hmac.Equal(wantIndexMac, rec.GetIndex().GetBlob()) {
			return nil, ErrIndexMac
		}
		var idx []string
		if err := json.Unmarshal(sad.GetIndex(), &idx); err != nil {
			return nil, fmt.Errorf("appstate: snapshot parse index json: %w", err)
		}
		muts = append(muts, Mutation{Operation: op, Index: idx, Action: sad.GetValue()})

		indexMacB64 := b64(rec.GetIndex().GetBlob())
		addBuffs = append(addBuffs, valueMac)
		vm := make([]byte, len(valueMac))
		copy(vm, valueMac)
		next.indexValueMap[indexMacB64] = vm
	}

	next.Hash = subtractThenAdd(next.Hash, nil, addBuffs)

	wantSnapshotMac := generateSnapshotMac(next.Hash, version, name, mk.SnapshotMacKey)
	if !hmac.Equal(wantSnapshotMac, snap.GetMac()) {
		return nil, ErrSnapshotMac
	}
	return &DecodeResult{State: next, Mutations: muts}, nil
}

// EncodeSnapshot is the inverse of DecodeSnapshot: it builds a SyncdSnapshot for
// the named collection from a set of mutations, computing every MAC the decoder
// verifies. It is provided so tests (and a future full-sync sender) can produce
// a valid snapshot blob; production only ever decodes server snapshots.
//
// randReader supplies the per-value IV (nil = crypto/rand).
func EncodeSnapshot(name string, version uint64, keyIDB64 string, keys MutationKeys, mutations []MutationToEncode, randReader io.Reader) (*waproto.SyncdSnapshot, error) {
	if randReader == nil {
		randReader = rand.Reader
	}
	encKeyID, err := decodeB64(keyIDB64)
	if err != nil {
		return nil, fmt.Errorf("appstate: bad keyId base64: %w", err)
	}
	hash := make([]byte, ltHashLen)
	addBuffs := make([][]byte, 0, len(mutations))
	records := make([]*waproto.SyncdRecord, 0, len(mutations))
	const op = waproto.SyncdMutation_SET
	for _, m := range mutations {
		indexBytes, err := json.Marshal(m.Index)
		if err != nil {
			return nil, fmt.Errorf("appstate: marshal index: %w", err)
		}
		sad := &waproto.SyncActionData{
			Index:   indexBytes,
			Value:   m.Action,
			Padding: []byte{},
			Version: proto.Int32(m.APIVersion),
		}
		plaintext, err := proto.Marshal(sad)
		if err != nil {
			return nil, fmt.Errorf("appstate: marshal SyncActionData: %w", err)
		}
		encValue, err := aesCBCEncrypt(keys.ValueEncryptionKey, plaintext, randReader)
		if err != nil {
			return nil, fmt.Errorf("appstate: encrypt: %w", err)
		}
		valueMac := generateContentMac(op, encValue, encKeyID, keys.ValueMacKey)
		indexMac := generateIndexMac(indexBytes, keys.IndexKey)
		blob := make([]byte, 0, len(encValue)+len(valueMac))
		blob = append(blob, encValue...)
		blob = append(blob, valueMac...)
		records = append(records, &waproto.SyncdRecord{
			Index: &waproto.SyncdIndex{Blob: indexMac},
			Value: &waproto.SyncdValue{Blob: blob},
			KeyId: &waproto.KeyId{Id: encKeyID},
		})
		addBuffs = append(addBuffs, valueMac)
	}
	hash = subtractThenAdd(hash, nil, addBuffs)
	snapshotMac := generateSnapshotMac(hash, version, name, keys.SnapshotMacKey)
	return &waproto.SyncdSnapshot{
		Version: &waproto.SyncdVersion{Version: proto.Uint64(version)},
		Records: records,
		Mac:     snapshotMac,
		KeyId:   &waproto.KeyId{Id: encKeyID},
	}, nil
}

// aesCBCDecrypt decrypts an IV-prefixed AES-256-CBC blob (IV is the first 16
// bytes) and strips PKCS#7 padding.
func aesCBCDecrypt(key, ivAndCiphertext []byte) ([]byte, error) {
	if len(ivAndCiphertext) < aes.BlockSize {
		return nil, errors.New("appstate: ciphertext shorter than IV")
	}
	iv := ivAndCiphertext[:aes.BlockSize]
	ct := ivAndCiphertext[aes.BlockSize:]
	if len(ct) == 0 || len(ct)%aes.BlockSize != 0 {
		return nil, errors.New("appstate: bad ciphertext length")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, ct)
	return pkcs7Unpad(out)
}

func pkcs7Unpad(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return nil, errors.New("appstate: empty plaintext")
	}
	pad := int(b[len(b)-1])
	if pad == 0 || pad > aes.BlockSize || pad > len(b) {
		return nil, errors.New("appstate: invalid pkcs7 padding")
	}
	if !bytes.Equal(b[len(b)-pad:], bytes.Repeat([]byte{byte(pad)}, pad)) {
		return nil, errors.New("appstate: invalid pkcs7 padding bytes")
	}
	return b[:len(b)-pad], nil
}
