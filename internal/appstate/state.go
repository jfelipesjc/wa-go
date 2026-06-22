package appstate

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"

	waproto "github.com/felipeleal/wa-go/internal/waproto"
)

// HashState is the per-collection LTHash state: a monotonically increasing
// version and the 128-byte LTHash. indexValueMap tracks the live valueMac for
// each index (keyed by base64 of the indexMac) so that a later SET/REMOVE on
// the same index can subtract the previous contribution.
type HashState struct {
	Version uint64
	Hash    []byte // 128 bytes
	// indexValueMap maps base64(indexMac) -> valueMac, the contribution
	// currently folded into Hash for that index.
	indexValueMap map[string][]byte
}

// NewHashState returns the zero state (version 0, all-zero hash), matching
// Baileys' newLTHashState().
func NewHashState() *HashState {
	return &HashState{
		Version:       0,
		Hash:          make([]byte, ltHashLen),
		indexValueMap: map[string][]byte{},
	}
}

// clone returns a deep copy so a failed patch application can be discarded.
func (s *HashState) clone() *HashState {
	h := make([]byte, len(s.Hash))
	copy(h, s.Hash)
	m := make(map[string][]byte, len(s.indexValueMap))
	for k, v := range s.indexValueMap {
		cp := make([]byte, len(v))
		copy(cp, v)
		m[k] = cp
	}
	return &HashState{Version: s.Version, Hash: h, indexValueMap: m}
}

// --- MAC helpers (mirror chat-utils.js / whatsapp-rust-bridge) ---

// generateContentMac computes the per-value MAC: HMAC-SHA512 (truncated to 32
// bytes) over keyData || data || last, where keyData = [opByte]||keyId and last
// is 8 zero bytes whose final byte holds len(keyData). The opByte is the raw
// SyncdOperation enum value (SET=0x00, REMOVE=0x01) — this matches
// whatsapp-rust-bridge's generateContentMac (note: it differs from older
// Baileys JS, which mapped SET->0x01 / REMOVE->0x02).
func generateContentMac(op waproto.SyncdMutation_SyncdOperation, data, keyID, key []byte) []byte {
	opByte := byte(op)
	keyData := make([]byte, 1+len(keyID))
	keyData[0] = opByte
	copy(keyData[1:], keyID)

	last := make([]byte, 8)
	last[7] = byte(len(keyData))

	mac := hmac.New(sha512.New, key)
	mac.Write(keyData)
	mac.Write(data)
	mac.Write(last)
	return mac.Sum(nil)[:32]
}

// generateIndexMac is HMAC-SHA256(indexBytes, indexKey).
func generateIndexMac(indexBytes, indexKey []byte) []byte {
	mac := hmac.New(sha256.New, indexKey)
	mac.Write(indexBytes)
	return mac.Sum(nil)
}

func to64BitNetworkOrder(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

// generateSnapshotMac is HMAC-SHA256(ltHash || u64be(version) || name,
// snapshotMacKey).
func generateSnapshotMac(ltHash []byte, version uint64, name string, key []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(ltHash)
	mac.Write(to64BitNetworkOrder(version))
	mac.Write([]byte(name))
	return mac.Sum(nil)
}

// generatePatchMac is HMAC-SHA256(snapshotMac || concat(valueMacs) ||
// u64be(version) || name, patchMacKey).
func generatePatchMac(snapshotMac []byte, valueMacs [][]byte, version uint64, name string, key []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(snapshotMac)
	for _, vm := range valueMacs {
		mac.Write(vm)
	}
	mac.Write(to64BitNetworkOrder(version))
	mac.Write([]byte(name))
	return mac.Sum(nil)
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }
