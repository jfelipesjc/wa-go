package signal

import (
	"errors"
	"fmt"

	"github.com/felipeleal/wa-go/internal/keys"
)

// GroupCipher encrypts and decrypts Signal group messages using a SenderKeyRecord
// for one (group, sender) pair. It mirrors
// @whiskeysockets/baileys/lib/Signal/Group/{group_cipher,group-session-builder}.js
// and is validated against testdata/signal/group_ab.json.
//
// Distribution: a sender calls CreateSenderKeyDistribution to mint its sender key
// and produce a SenderKeyDistributionMessage (SKDM); each recipient calls
// ProcessSenderKeyDistribution to install a receive-only state for that sender.
// Thereafter EncryptGroup / DecryptGroup move plaintext over the sender chain.
type GroupCipher struct {
	record *SenderKeyRecord
}

// NewGroupCipher wraps a record. The record holds the sender's state(s).
func NewGroupCipher(record *SenderKeyRecord) *GroupCipher { return &GroupCipher{record: record} }

// Record returns the underlying record (mutated by encrypt/decrypt).
func (g *GroupCipher) Record() *SenderKeyRecord { return g.record }

// CreateSenderKeyDistribution mints a fresh sender key in the record (replacing
// any existing state) and returns the SKDM to fan out to group members. keyID and
// the 32-byte chain seed are caller-supplied so production can use random values
// and tests can replay the golden vector deterministically.
//
// Mirrors GroupSessionBuilder.create: setSenderKeyState(keyId, 0, senderKey,
// signingKey) then build SKDM from the state's chain key + signing public.
func (g *GroupCipher) CreateSenderKeyDistribution(keyID uint32, chainSeed [32]byte, signing keys.KeyPair) *SenderKeyDistributionMessage {
	g.record.SetState(keyID, 0, chainSeed, signing)
	st := g.record.State(nil)
	return &SenderKeyDistributionMessage{
		KeyID:      st.KeyID,
		Iteration:  st.Chain.iteration,
		ChainKey:   st.Chain.chainKey,
		SigningPub: st.SigningPub,
	}
}

// ProcessSenderKeyDistribution installs a receive-only state from a peer's SKDM
// into the record, so that peer's group messages can be decrypted. Mirrors
// GroupSessionBuilder.process / SenderKeyRecord.addSenderKeyState.
func (g *GroupCipher) ProcessSenderKeyDistribution(skdm *SenderKeyDistributionMessage) {
	g.record.AddState(skdm.KeyID, skdm.Iteration, skdm.ChainKey, skdm.SigningPub)
}

// EncryptGroup encrypts plaintext as a SenderKeyMessage, advancing the sender
// chain. The plaintext must already be padded by the caller if the transport
// requires it (WhatsApp pads before this layer); we AES-256-CBC + PKCS7 it here,
// matching libsignal crypto.encrypt.
//
// NOTE on iteration selection: libsignal's GroupCipher.encrypt uses the key for
// iteration (chain.iteration === 0 ? 0 : chain.iteration + 1). We reproduce that
// exactly so wire iterations match (0, 2, 4, ...) and ciphertexts are
// byte-identical to the golden vectors.
func (g *GroupCipher) EncryptGroup(plaintext []byte) ([]byte, error) {
	st := g.record.State(nil)
	if st == nil {
		return nil, errors.New("signal: no sender key state to encrypt")
	}
	if !st.HasPrivate {
		return nil, errors.New("signal: cannot encrypt with receive-only sender key")
	}

	chainIter := st.Chain.iteration
	target := chainIter
	if chainIter != 0 {
		target = chainIter + 1
	}

	mk, err := g.senderKeyForIteration(st, target)
	if err != nil {
		return nil, err
	}

	body, err := aesCBCEncrypt(mk.cipherKey[:], mk.iv[:], plaintext)
	if err != nil {
		return nil, err
	}
	return SerializeSenderKeyMessage(st.KeyID, mk.iteration, body, st.SigningKey.Priv)
}

// DecryptGroup verifies the signature, derives the message key for the message's
// iteration, and AES-256-CBC decrypts. Mirrors GroupCipher.decrypt.
func (g *GroupCipher) DecryptGroup(ciphertext []byte) ([]byte, error) {
	skm, err := ParseSenderKeyMessage(ciphertext)
	if err != nil {
		return nil, err
	}
	keyID := skm.KeyID
	st := g.record.State(&keyID)
	if st == nil {
		return nil, fmt.Errorf("signal: no sender key state for id %d", keyID)
	}
	if !skm.VerifySignature(ciphertext, st.SigningPub) {
		return nil, errors.New("signal: sender key signature verification failed")
	}

	mk, err := g.senderKeyForIteration(st, skm.Iteration)
	if err != nil {
		return nil, err
	}
	return aesCBCDecrypt(mk.cipherKey[:], mk.iv[:], skm.Ciphertext)
}

// senderKeyForIteration returns the message key for the requested iteration,
// advancing or rewinding the chain as libsignal's getSenderKey does:
//   - iteration < chain: use a retained skipped key (or fail if not retained).
//   - iteration >= chain: derive forward, retaining intermediate keys, then
//     advance the chain one past the target.
func (g *GroupCipher) senderKeyForIteration(st *SenderKeyState, iteration uint32) (senderMessageKey, error) {
	chain := st.Chain
	if chain.iteration > iteration {
		if mk, ok := st.removeMessageKey(iteration); ok {
			return mk, nil
		}
		return senderMessageKey{}, fmt.Errorf("signal: received group message with old counter: %d, %d", chain.iteration, iteration)
	}
	if iteration-chain.iteration > maxSenderChainSkip {
		return senderMessageKey{}, errors.New("signal: over 2000 group messages into the future")
	}
	for chain.iteration < iteration {
		st.addMessageKey(chain.messageKey())
		chain = chain.next()
	}
	mk := chain.messageKey()
	st.Chain = chain.next()
	return mk, nil
}
