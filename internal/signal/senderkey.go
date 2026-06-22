package signal

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"

	"github.com/felipeleal/wa-go/internal/keys"
)

// This file implements the Sender Key (Signal group) symmetric state: the
// sender chain key, the per-message keys derived from it, and the serializable
// SenderKeyState / SenderKeyRecord. It mirrors
// @whiskeysockets/baileys/lib/Signal/Group/{sender-chain-key, sender-message-key,
// sender-key-state, sender-key-record}.js, validated against
// testdata/signal/group_ab.json.
//
// Chain key ratchet (sender-chain-key.js):
//
//	messageKeySeed = HMAC-SHA256(chainKey, 0x01)
//	nextChainKey   = HMAC-SHA256(chainKey, 0x02)
//
// Message keys (sender-message-key.js, via libsignal crypto.deriveSecrets):
//
//	T = deriveSecrets(seed, salt=zeros[32], info="WhisperGroup")  // 3 HMAC chunks
//	iv        = T1[0:16]
//	cipherKey = T1[16:32] || T2[0:16]   // 32-byte AES-256 key
//
// deriveSecrets here is libsignal's HKDF variant:
//
//	PRK = HMAC-SHA256(salt, seed)
//	T1  = HMAC-SHA256(PRK, info || 0x01)
//	T2  = HMAC-SHA256(PRK, T1 || info || 0x02)

const infoWhisperGroup = "WhisperGroup"

// senderMessageKeySeed / senderChainKeySeed are the single-byte HMAC inputs that
// advance the sender chain.
var (
	senderMessageKeySeed = []byte{0x01}
	senderChainKeySeed   = []byte{0x02}
)

// senderMessageKey is the AES-256-CBC cipher key + IV for a single group message
// at a given iteration. There is no separate MAC key: group message integrity is
// the SenderKeyMessage's Curve signature, not an HMAC.
type senderMessageKey struct {
	iteration uint32
	seed      [32]byte // the messageKeySeed this was derived from (HMAC(chainKey,0x01))
	iv        [16]byte
	cipherKey [32]byte
}

// deriveSenderMessageKey expands a message-key seed into iv + cipherKey, matching
// libsignal SenderMessageKey (deriveSecrets(seed, zeros, "WhisperGroup")).
func deriveSenderMessageKey(iteration uint32, seed [32]byte) senderMessageKey {
	salt := make([]byte, 32)
	prkMac := hmac.New(sha256.New, salt)
	prkMac.Write(seed[:])
	prk := prkMac.Sum(nil)

	info := []byte(infoWhisperGroup)

	t1Mac := hmac.New(sha256.New, prk)
	t1Mac.Write(info)
	t1Mac.Write([]byte{0x01})
	t1 := t1Mac.Sum(nil)

	t2Mac := hmac.New(sha256.New, prk)
	t2Mac.Write(t1)
	t2Mac.Write(info)
	t2Mac.Write([]byte{0x02})
	t2 := t2Mac.Sum(nil)

	var mk senderMessageKey
	mk.iteration = iteration
	mk.seed = seed
	copy(mk.iv[:], t1[0:16])
	copy(mk.cipherKey[:16], t1[16:32])
	copy(mk.cipherKey[16:], t2[0:16])
	return mk
}

// senderChainKey is the ratcheting chain key for a sender at a given iteration.
type senderChainKey struct {
	iteration uint32
	chainKey  [32]byte
}

// messageKey derives the message key for this chain key's current iteration.
func (c senderChainKey) messageKey() senderMessageKey {
	mac := hmac.New(sha256.New, c.chainKey[:])
	mac.Write(senderMessageKeySeed)
	var seed [32]byte
	copy(seed[:], mac.Sum(nil))
	return deriveSenderMessageKey(c.iteration, seed)
}

// next advances the chain key one iteration.
func (c senderChainKey) next() senderChainKey {
	mac := hmac.New(sha256.New, c.chainKey[:])
	mac.Write(senderChainKeySeed)
	var nk [32]byte
	copy(nk[:], mac.Sum(nil))
	return senderChainKey{iteration: c.iteration + 1, chainKey: nk}
}

// maxSenderMessageKeys / maxSenderChainSkip mirror libsignal's bounds.
const (
	maxSenderMessageKeys = 2000
	maxSenderChainSkip   = 2000
)

// SenderKeyState is the per-sender state for one group: an id, the current chain
// key, the signing key pair (private may be empty for a receive-only state), and
// any retained out-of-order message keys. JSON serializable for the store; not
// byte-compatible with libsignal's blob (correctness is validated against real
// ciphertexts).
type SenderKeyState struct {
	KeyID      uint32
	Chain      senderChainKey
	SigningPub [signalKeyLen]byte // 33-byte 0x05-prefixed
	SigningKey keys.KeyPair       // private; Priv all-zero when receive-only
	HasPrivate bool

	// MessageKeys retains keys for skipped iterations (out-of-order / the
	// GroupCipher iteration+1 quirk), newest last, capped at maxSenderMessageKeys.
	MessageKeys []senderMessageKey
}

// removeMessageKey pops and returns the retained key for the iteration.
func (s *SenderKeyState) removeMessageKey(iteration uint32) (senderMessageKey, bool) {
	for i, mk := range s.MessageKeys {
		if mk.iteration == iteration {
			s.MessageKeys = append(s.MessageKeys[:i], s.MessageKeys[i+1:]...)
			return mk, true
		}
	}
	return senderMessageKey{}, false
}

// addMessageKey retains a skipped message key, evicting the oldest past the cap.
func (s *SenderKeyState) addMessageKey(mk senderMessageKey) {
	s.MessageKeys = append(s.MessageKeys, mk)
	if len(s.MessageKeys) > maxSenderMessageKeys {
		s.MessageKeys = s.MessageKeys[len(s.MessageKeys)-maxSenderMessageKeys:]
	}
}

// SenderKeyRecord holds up to maxSenderKeyStates states (so a re-keyed sender
// can still be decrypted during the transition), newest last. Mirrors
// sender-key-record.js (MAX_STATES = 5).
type SenderKeyRecord struct {
	States []*SenderKeyState
}

const maxSenderKeyStates = 5

// IsEmpty reports whether the record has no state.
func (r *SenderKeyRecord) IsEmpty() bool { return len(r.States) == 0 }

// State returns the newest state, or the one matching keyID if keyID is non-nil.
// With keyID nil it returns the most recent state (libsignal getSenderKeyState()
// with no argument).
func (r *SenderKeyRecord) State(keyID *uint32) *SenderKeyState {
	if keyID == nil {
		if len(r.States) == 0 {
			return nil
		}
		return r.States[len(r.States)-1]
	}
	for _, s := range r.States {
		if s.KeyID == *keyID {
			return s
		}
	}
	return nil
}

// SetState replaces all states with a single sending state (we created the key).
// Mirrors setSenderKeyState.
func (r *SenderKeyRecord) SetState(keyID uint32, iteration uint32, chainKey [32]byte, signing keys.KeyPair) {
	st := &SenderKeyState{
		KeyID:      keyID,
		Chain:      senderChainKey{iteration: iteration, chainKey: chainKey},
		SigningKey: signing,
		SigningPub: pubKeyOf(signing),
		HasPrivate: true,
	}
	r.States = []*SenderKeyState{st}
}

// AddState appends a receive-only state from a peer's SKDM. Mirrors
// addSenderKeyState (signing private is absent; only the public is known).
func (r *SenderKeyRecord) AddState(keyID uint32, iteration uint32, chainKey [32]byte, signingPub [signalKeyLen]byte) {
	st := &SenderKeyState{
		KeyID:      keyID,
		Chain:      senderChainKey{iteration: iteration, chainKey: chainKey},
		SigningPub: signingPub,
		HasPrivate: false,
	}
	r.States = append(r.States, st)
	if len(r.States) > maxSenderKeyStates {
		r.States = r.States[len(r.States)-maxSenderKeyStates:]
	}
}

// --- serialization ---

type jsonSenderKeyRecord struct {
	States []jsonSenderKeyState `json:"states"`
}

type jsonSenderKeyState struct {
	KeyID       uint32             `json:"keyId"`
	Iteration   uint32             `json:"iteration"`
	ChainKey    []byte             `json:"chainKey"`
	SigningPub  []byte             `json:"signingPub"`
	SigningPriv []byte             `json:"signingPriv,omitempty"`
	HasPrivate  bool               `json:"hasPrivate"`
	MessageKeys []jsonSenderMsgKey `json:"messageKeys,omitempty"`
}

type jsonSenderMsgKey struct {
	Iteration uint32 `json:"iteration"`
	Seed      []byte `json:"seed"`
}

// MarshalSenderKeyRecord serializes a record to JSON for the store.
func MarshalSenderKeyRecord(r *SenderKeyRecord) ([]byte, error) {
	jr := jsonSenderKeyRecord{}
	for _, s := range r.States {
		js := jsonSenderKeyState{
			KeyID:      s.KeyID,
			Iteration:  s.Chain.iteration,
			ChainKey:   append([]byte(nil), s.Chain.chainKey[:]...),
			SigningPub: append([]byte(nil), s.SigningPub[:]...),
			HasPrivate: s.HasPrivate,
		}
		if s.HasPrivate {
			js.SigningPriv = append([]byte(nil), s.SigningKey.Priv[:]...)
		}
		for _, mk := range s.MessageKeys {
			js.MessageKeys = append(js.MessageKeys, jsonSenderMsgKey{
				Iteration: mk.iteration,
				Seed:      append([]byte(nil), mk.seed[:]...),
			})
		}
		jr.States = append(jr.States, js)
	}
	return json.Marshal(jr)
}

// UnmarshalSenderKeyRecord deserializes a record produced by Marshal. An empty /
// missing blob yields an empty record (matching SenderKeyRecord with no states).
func UnmarshalSenderKeyRecord(data []byte) (*SenderKeyRecord, error) {
	r := &SenderKeyRecord{}
	if len(data) == 0 {
		return r, nil
	}
	var jr jsonSenderKeyRecord
	if err := json.Unmarshal(data, &jr); err != nil {
		return nil, err
	}
	for _, js := range jr.States {
		if len(js.ChainKey) != 32 {
			return nil, errors.New("signal: bad sender chainKey length")
		}
		st := &SenderKeyState{
			KeyID:      js.KeyID,
			HasPrivate: js.HasPrivate,
		}
		st.Chain.iteration = js.Iteration
		copy(st.Chain.chainKey[:], js.ChainKey)
		copy(st.SigningPub[:], js.SigningPub)
		if js.HasPrivate {
			copy(st.SigningKey.Priv[:], js.SigningPriv)
			// Recover the curve public (raw 32b) from the 0x05-prefixed pub.
			copy(st.SigningKey.Pub[:], js.SigningPub[1:])
		}
		for _, jmk := range js.MessageKeys {
			var seed [32]byte
			copy(seed[:], jmk.Seed)
			st.MessageKeys = append(st.MessageKeys, deriveSenderMessageKey(jmk.Iteration, seed))
		}
		r.States = append(r.States, st)
	}
	return r, nil
}
