package signal

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/felipeleal/wa-go/internal/keys"
)

// maxSkip bounds how many message keys we will derive ahead to handle
// out-of-order delivery within a chain, preventing a malicious counter from
// forcing unbounded work.
const maxSkip = 2000

// skippedKey identifies a stored skipped message key by ratchet pub + counter.
type skippedKey struct {
	Ratchet [signalKeyLen]byte
	Counter uint32
}

// SessionState is the Double Ratchet state for one 1:1 session. It is JSON
// serializable for persistence in the store. It is intentionally not
// byte-compatible with libsignal's on-disk format — correctness is validated by
// decrypting/reproducing real ciphertexts, not by matching libsignal's blob.
type SessionState struct {
	LocalIdentityPub  [signalKeyLen]byte
	RemoteIdentityPub [signalKeyLen]byte
	LocalRegID        uint32

	RootKey [32]byte

	// Sending side.
	SendingRatchet  keys.KeyPair // our current ratchet key pair
	SendingChain    [32]byte
	HasSendingChain bool
	SendCounter     uint32
	PrevSendCounter uint32

	// Receiving side.
	TheirRatchetPub   [signalKeyLen]byte
	HasReceivingChain bool
	ReceivingChain    [32]byte
	RecvCounter       uint32

	IsInitiator bool

	// genRatchet, if set, supplies the next sending ratchet key pair instead of
	// keys.GenKeyPair. Used by tests to inject the ephemerals from the golden
	// vectors so the ratchet replays deterministically. Not serialized.
	genRatchet func() (keys.KeyPair, error) `json:"-"`

	// PendingBaseKey: when initiator hasn't yet received a reply, outgoing
	// messages are wrapped as PreKeyWhisperMessages carrying these fields.
	PendingPreKeyID       uint32
	HasPendingPreKey      bool
	PendingSignedPreKeyID uint32
	PendingBaseKey        [signalKeyLen]byte
	PendingActive         bool

	Skipped map[skippedKey]messageKeys
}

// jsonSessionState is the serialization shape (maps with struct keys aren't JSON
// friendly, so skipped keys are flattened to a slice).
type jsonSessionState struct {
	State       sessionAlias   `json:"state"`
	SkippedKeys []skippedEntry `json:"skipped"`
}

type sessionAlias struct {
	LocalIdentityPub      []byte `json:"localIdentityPub"`
	RemoteIdentityPub     []byte `json:"remoteIdentityPub"`
	LocalRegID            uint32 `json:"localRegID"`
	RootKey               []byte `json:"rootKey"`
	SendingRatchetPriv    []byte `json:"sendingRatchetPriv"`
	SendingRatchetPub     []byte `json:"sendingRatchetPub"`
	SendingChain          []byte `json:"sendingChain"`
	HasSendingChain       bool   `json:"hasSendingChain"`
	SendCounter           uint32 `json:"sendCounter"`
	PrevSendCounter       uint32 `json:"prevSendCounter"`
	TheirRatchetPub       []byte `json:"theirRatchetPub"`
	HasReceivingChain     bool   `json:"hasReceivingChain"`
	ReceivingChain        []byte `json:"receivingChain"`
	RecvCounter           uint32 `json:"recvCounter"`
	IsInitiator           bool   `json:"isInitiator"`
	PendingPreKeyID       uint32 `json:"pendingPreKeyID"`
	HasPendingPreKey      bool   `json:"hasPendingPreKey"`
	PendingSignedPreKeyID uint32 `json:"pendingSignedPreKeyID"`
	PendingBaseKey        []byte `json:"pendingBaseKey"`
	PendingActive         bool   `json:"pendingActive"`
}

type skippedEntry struct {
	Ratchet   []byte `json:"ratchet"`
	Counter   uint32 `json:"counter"`
	CipherKey []byte `json:"cipherKey"`
	MacKey    []byte `json:"macKey"`
	IV        []byte `json:"iv"`
}

// SessionRecord wraps a SessionState for persistence. A record may hold no state
// (fresh) until a session is established.
type SessionRecord struct {
	State *SessionState
}

// Marshal serializes the record to JSON bytes for the store.
func (r *SessionRecord) Marshal() ([]byte, error) {
	if r.State == nil {
		return json.Marshal(jsonSessionState{})
	}
	s := r.State
	js := jsonSessionState{
		State: sessionAlias{
			LocalIdentityPub:      s.LocalIdentityPub[:],
			RemoteIdentityPub:     s.RemoteIdentityPub[:],
			LocalRegID:            s.LocalRegID,
			RootKey:               s.RootKey[:],
			SendingRatchetPriv:    s.SendingRatchet.Priv[:],
			SendingRatchetPub:     s.SendingRatchet.Pub[:],
			SendingChain:          s.SendingChain[:],
			HasSendingChain:       s.HasSendingChain,
			SendCounter:           s.SendCounter,
			PrevSendCounter:       s.PrevSendCounter,
			TheirRatchetPub:       s.TheirRatchetPub[:],
			HasReceivingChain:     s.HasReceivingChain,
			ReceivingChain:        s.ReceivingChain[:],
			RecvCounter:           s.RecvCounter,
			IsInitiator:           s.IsInitiator,
			PendingPreKeyID:       s.PendingPreKeyID,
			HasPendingPreKey:      s.HasPendingPreKey,
			PendingSignedPreKeyID: s.PendingSignedPreKeyID,
			PendingBaseKey:        s.PendingBaseKey[:],
			PendingActive:         s.PendingActive,
		},
	}
	for k, v := range s.Skipped {
		js.SkippedKeys = append(js.SkippedKeys, skippedEntry{
			Ratchet:   append([]byte(nil), k.Ratchet[:]...),
			Counter:   k.Counter,
			CipherKey: append([]byte(nil), v.cipherKey[:]...),
			MacKey:    append([]byte(nil), v.macKey[:]...),
			IV:        append([]byte(nil), v.iv[:]...),
		})
	}
	return json.Marshal(js)
}

// UnmarshalSessionRecord deserializes a record produced by Marshal.
func UnmarshalSessionRecord(data []byte) (*SessionRecord, error) {
	var js jsonSessionState
	if err := json.Unmarshal(data, &js); err != nil {
		return nil, err
	}
	if len(js.State.RootKey) == 0 {
		return &SessionRecord{}, nil
	}
	s := &SessionState{
		LocalRegID:            js.State.LocalRegID,
		HasSendingChain:       js.State.HasSendingChain,
		SendCounter:           js.State.SendCounter,
		PrevSendCounter:       js.State.PrevSendCounter,
		HasReceivingChain:     js.State.HasReceivingChain,
		RecvCounter:           js.State.RecvCounter,
		IsInitiator:           js.State.IsInitiator,
		PendingPreKeyID:       js.State.PendingPreKeyID,
		HasPendingPreKey:      js.State.HasPendingPreKey,
		PendingSignedPreKeyID: js.State.PendingSignedPreKeyID,
		PendingActive:         js.State.PendingActive,
		Skipped:               map[skippedKey]messageKeys{},
	}
	copy(s.LocalIdentityPub[:], js.State.LocalIdentityPub)
	copy(s.RemoteIdentityPub[:], js.State.RemoteIdentityPub)
	copy(s.RootKey[:], js.State.RootKey)
	copy(s.SendingRatchet.Priv[:], js.State.SendingRatchetPriv)
	copy(s.SendingRatchet.Pub[:], js.State.SendingRatchetPub)
	copy(s.SendingChain[:], js.State.SendingChain)
	copy(s.TheirRatchetPub[:], js.State.TheirRatchetPub)
	copy(s.ReceivingChain[:], js.State.ReceivingChain)
	copy(s.PendingBaseKey[:], js.State.PendingBaseKey)
	for _, e := range js.SkippedKeys {
		var sk skippedKey
		copy(sk.Ratchet[:], e.Ratchet)
		sk.Counter = e.Counter
		var mk messageKeys
		copy(mk.cipherKey[:], e.CipherKey)
		copy(mk.macKey[:], e.MacKey)
		copy(mk.iv[:], e.IV)
		mk.counter = e.Counter
		s.Skipped[sk] = mk
	}
	return &SessionRecord{State: s}, nil
}

// SessionOption customizes a SessionState. The primary use is injecting a
// deterministic sending-ratchet generator for golden-vector replay.
type SessionOption func(*SessionState)

// WithRatchetGenerator overrides the sending-ratchet key generator. Each call
// returns the next key pair. In production this is unset (random); in tests it
// feeds the ephemerals captured in the golden vectors.
func WithRatchetGenerator(gen func() (keys.KeyPair, error)) SessionOption {
	return func(s *SessionState) { s.genRatchet = gen }
}

// SessionCipher encrypts and decrypts messages for a single session.
type SessionCipher struct {
	state *SessionState
}

// NewSessionCipher wraps an existing session state.
func NewSessionCipher(s *SessionState) *SessionCipher { return &SessionCipher{state: s} }

// State returns the underlying mutable session state.
func (c *SessionCipher) State() *SessionState { return c.state }

// CiphertextMessage is the result of Encrypt: serialized wire bytes plus its
// type ("msg" or "pkmsg").
type CiphertextMessage struct {
	Type       string
	Serialized []byte
}

// Encrypt encrypts plaintext, advancing the sending chain. If the session has a
// pending prekey (initiator that hasn't received a reply), the result is wrapped
// in a PreKeyWhisperMessage; otherwise it is a bare WhisperMessage.
func (c *SessionCipher) Encrypt(plaintext []byte) (CiphertextMessage, error) {
	s := c.state
	if s == nil {
		return CiphertextMessage{}, errNoSession
	}
	if !s.HasSendingChain {
		return CiphertextMessage{}, errors.New("signal: no sending chain")
	}

	mk := deriveMessageKeys(s.SendingChain, s.SendCounter)
	body, err := aesCBCEncrypt(mk.cipherKey[:], mk.iv[:], plaintext)
	if err != nil {
		return CiphertextMessage{}, err
	}

	wm := &WhisperMessage{
		Counter:         s.SendCounter,
		PreviousCounter: s.PrevSendCounter,
		Ciphertext:      body,
	}
	wm.RatchetKey = pubKeyOf(s.SendingRatchet)

	// MAC over (version||body) keyed by macKey, with sender=local, receiver=remote.
	serialized := wm.Serialize(func(signed []byte) []byte {
		return computeMAC(mk.macKey[:], s.LocalIdentityPub[:], s.RemoteIdentityPub[:], signed)
	})

	// Advance the sending chain.
	s.SendingChain = chainKeyNext(s.SendingChain)
	s.SendCounter++

	if s.PendingActive {
		pk := &PreKeyWhisperMessage{
			RegistrationID: s.LocalRegID,
			BaseKey:        s.PendingBaseKey,
			IdentityKey:    s.LocalIdentityPub,
			Message:        serialized,
			SignedPreKeyID: s.PendingSignedPreKeyID,
		}
		if s.HasPendingPreKey {
			pk.PreKeyID = s.PendingPreKeyID
			pk.HasPreKeyID = true
		}
		return CiphertextMessage{Type: "pkmsg", Serialized: pk.Serialize()}, nil
	}
	return CiphertextMessage{Type: "msg", Serialized: serialized}, nil
}

// Decrypt decrypts a bare WhisperMessage (type "msg"), performing a DH ratchet
// step when the message carries a new ratchet key.
func (c *SessionCipher) Decrypt(serialized []byte) ([]byte, error) {
	s := c.state
	if s == nil {
		return nil, errNoSession
	}
	wm, signed, mac, err := parseWhisperMessage(serialized)
	if err != nil {
		return nil, err
	}
	return c.decryptWhisper(wm, signed, mac, s.RemoteIdentityPub, s.LocalIdentityPub)
}

// decryptWhisper is the shared receive path. senderID/receiverID are the MAC
// input identities (sender is the remote peer, receiver is us).
func (c *SessionCipher) decryptWhisper(wm *WhisperMessage, signed, mac []byte, senderID, receiverID [signalKeyLen]byte) ([]byte, error) {
	s := c.state

	// Try a stored skipped key first.
	sk := skippedKey{Ratchet: wm.RatchetKey, Counter: wm.Counter}
	if mk, ok := s.Skipped[sk]; ok {
		pt, err := tryDecrypt(mk, wm, signed, mac, senderID, receiverID)
		if err != nil {
			return nil, err
		}
		delete(s.Skipped, sk)
		return pt, nil
	}

	// DH ratchet: if the message's ratchet key differs from the one we have a
	// receiving chain for, step the ratchet.
	if !s.HasReceivingChain || wm.RatchetKey != s.TheirRatchetPub {
		if err := c.dhRatchetReceive(wm.RatchetKey); err != nil {
			return nil, err
		}
	}

	// Skip ahead within the receiving chain to reach wm.Counter.
	if wm.Counter < s.RecvCounter {
		return nil, fmt.Errorf("signal: counter %d below current %d and no skipped key", wm.Counter, s.RecvCounter)
	}
	if wm.Counter-s.RecvCounter > maxSkip {
		return nil, errors.New("signal: too many skipped messages")
	}
	for s.RecvCounter < wm.Counter {
		mk := deriveMessageKeys(s.ReceivingChain, s.RecvCounter)
		s.Skipped[skippedKey{Ratchet: s.TheirRatchetPub, Counter: s.RecvCounter}] = mk
		s.ReceivingChain = chainKeyNext(s.ReceivingChain)
		s.RecvCounter++
	}

	mk := deriveMessageKeys(s.ReceivingChain, s.RecvCounter)
	pt, err := tryDecrypt(mk, wm, signed, mac, senderID, receiverID)
	if err != nil {
		return nil, err
	}
	s.ReceivingChain = chainKeyNext(s.ReceivingChain)
	s.RecvCounter++
	return pt, nil
}

// dhRatchetReceive performs a receiving DH ratchet step for a newly seen remote
// ratchet key. It derives the new receiving chain, then a fresh sending ratchet
// key + sending chain (so we can reply), mirroring libsignal's ratchet.
func (c *SessionCipher) dhRatchetReceive(theirRatchet [signalKeyLen]byte) error {
	s := c.state

	// Step 1: receiving chain from rootKDF(rootKey, ourCurrentRatchetPriv, theirRatchet).
	newRoot, recvChain, err := rootKDF(s.RootKey, s.SendingRatchet.Priv, theirRatchet)
	if err != nil {
		return err
	}
	s.RootKey = newRoot
	s.ReceivingChain = recvChain
	s.HasReceivingChain = true
	s.RecvCounter = 0
	s.TheirRatchetPub = theirRatchet

	// Step 2: generate a fresh sending ratchet and derive the new sending chain
	// so future Encrypt calls advance the conversation. Once we ratchet on
	// receive, any pending-prekey wrapping is dropped.
	gen := s.genRatchet
	if gen == nil {
		gen = keys.GenKeyPair
	}
	newRatchet, err := gen()
	if err != nil {
		return err
	}
	root2, sendChain, err := rootKDF(s.RootKey, newRatchet.Priv, theirRatchet)
	if err != nil {
		return err
	}
	s.RootKey = root2
	s.SendingRatchet = newRatchet
	s.SendingChain = sendChain
	s.HasSendingChain = true
	// libsignal: ratchet.previousCounter = previous sending chain's last-used
	// index. We track SendCounter as the next index to use, so the last-used
	// index is SendCounter-1. If the previous chain sent nothing (SendCounter
	// still 0, e.g. a pure responder), libsignal leaves previousCounter at its
	// prior value, so we don't touch it.
	if s.SendCounter > 0 {
		s.PrevSendCounter = s.SendCounter - 1
	}
	s.SendCounter = 0
	s.PendingActive = false
	return nil
}

// tryDecrypt verifies the MAC and decrypts the body with the given message keys.
func tryDecrypt(mk messageKeys, wm *WhisperMessage, signed, mac []byte, senderID, receiverID [signalKeyLen]byte) ([]byte, error) {
	if !verifyMAC(mk.macKey[:], senderID[:], receiverID[:], signed, mac) {
		return nil, errors.New("signal: MAC verification failed")
	}
	pt, err := aesCBCDecrypt(mk.cipherKey[:], mk.iv[:], wm.Ciphertext)
	if err != nil {
		return nil, err
	}
	return pt, nil
}

// --- AES-256-CBC + PKCS7 ---

func aesCBCEncrypt(key, iv, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	padded := pkcs7Pad(plaintext, block.BlockSize())
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, padded)
	return out, nil
}

func aesCBCDecrypt(key, iv, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) == 0 || len(ciphertext)%block.BlockSize() != 0 {
		return nil, errors.New("signal: invalid CBC ciphertext length")
	}
	out := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, ciphertext)
	return pkcs7Unpad(out, block.BlockSize())
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	pad := blockSize - len(data)%blockSize
	out := make([]byte, len(data)+pad)
	copy(out, data)
	for i := len(data); i < len(out); i++ {
		out[i] = byte(pad)
	}
	return out
}

func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 || len(data)%blockSize != 0 {
		return nil, errors.New("signal: invalid padding length")
	}
	pad := int(data[len(data)-1])
	if pad == 0 || pad > blockSize || pad > len(data) {
		return nil, errors.New("signal: invalid PKCS7 padding")
	}
	if !bytes.Equal(data[len(data)-pad:], bytes.Repeat([]byte{byte(pad)}, pad)) {
		return nil, errors.New("signal: corrupt PKCS7 padding")
	}
	return data[:len(data)-pad], nil
}
