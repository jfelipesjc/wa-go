package signal

import (
	"errors"

	"github.com/felipeleal/wa-go/internal/keys"
)

// discontinuityBytes is the 0xFF*32 prefix libsignal prepends to the X3DH master
// secret (the "discontinuity" / domain-separation block).
func discontinuityBytes() []byte {
	b := make([]byte, 32)
	for i := range b {
		b[i] = 0xFF
	}
	return b
}

// InitiatorParams holds everything the initiator (alice) needs to build a fresh
// session from a recipient's prekey bundle.
type InitiatorParams struct {
	LocalIdentity   keys.KeyPair // alice identity key pair
	LocalBaseKey    keys.KeyPair // alice ephemeral "base" key (EK_a) — eph[0] in vectors
	RemoteIdentity  [signalKeyLen]byte
	RemoteSignedPre [signalKeyLen]byte // SPK_b
	RemotePreKey    [signalKeyLen]byte // OPK_b (one-time prekey)
	HasPreKey       bool
}

// ResponderParams holds everything the responder (bob) needs to process an
// incoming PreKeyWhisperMessage and derive the same initial root key.
type ResponderParams struct {
	LocalIdentity  keys.KeyPair // bob identity key pair
	LocalSignedPre keys.KeyPair // SPK_b key pair
	LocalPreKey    keys.KeyPair // OPK_b key pair
	HasPreKey      bool
	RemoteIdentity [signalKeyLen]byte // IK_a
	RemoteBaseKey  [signalKeyLen]byte // EK_a (alice base key)
}

// x3dhInitiator computes the X3DH master secret on the initiator side:
//
//	DH1 = DH(IK_a, SPK_b)
//	DH2 = DH(EK_a, IK_b)
//	DH3 = DH(EK_a, SPK_b)
//	DH4 = DH(EK_a, OPK_b)   (omitted if no one-time prekey)
//	master = (0xFF*32) || DH1 || DH2 || DH3 || DH4
func x3dhInitiator(p InitiatorParams) ([]byte, error) {
	dh1, err := dh(p.LocalIdentity.Priv, p.RemoteSignedPre)
	if err != nil {
		return nil, err
	}
	dh2, err := dh(p.LocalBaseKey.Priv, p.RemoteIdentity)
	if err != nil {
		return nil, err
	}
	dh3, err := dh(p.LocalBaseKey.Priv, p.RemoteSignedPre)
	if err != nil {
		return nil, err
	}
	master := discontinuityBytes()
	master = append(master, dh1[:]...)
	master = append(master, dh2[:]...)
	master = append(master, dh3[:]...)
	if p.HasPreKey {
		dh4, err := dh(p.LocalBaseKey.Priv, p.RemotePreKey)
		if err != nil {
			return nil, err
		}
		master = append(master, dh4[:]...)
	}
	return master, nil
}

// x3dhResponder computes the X3DH master secret on the responder side. The DH
// operands are mirrored so the products match the initiator byte for byte:
//
//	DH1 = DH(SPK_b, IK_a)
//	DH2 = DH(IK_b, EK_a)
//	DH3 = DH(SPK_b, EK_a)
//	DH4 = DH(OPK_b, EK_a)
func x3dhResponder(p ResponderParams) ([]byte, error) {
	dh1, err := dh(p.LocalSignedPre.Priv, p.RemoteIdentity)
	if err != nil {
		return nil, err
	}
	dh2, err := dh(p.LocalIdentity.Priv, p.RemoteBaseKey)
	if err != nil {
		return nil, err
	}
	dh3, err := dh(p.LocalSignedPre.Priv, p.RemoteBaseKey)
	if err != nil {
		return nil, err
	}
	master := discontinuityBytes()
	master = append(master, dh1[:]...)
	master = append(master, dh2[:]...)
	master = append(master, dh3[:]...)
	if p.HasPreKey {
		dh4, err := dh(p.LocalPreKey.Priv, p.RemoteBaseKey)
		if err != nil {
			return nil, err
		}
		master = append(master, dh4[:]...)
	}
	return master, nil
}

// InitiateSession builds the initial SessionState for the initiator (alice).
//
// After X3DH, libsignal performs the first DH-ratchet step immediately: the
// initiator's sending ratchet key is a fresh ephemeral (sendingRatchet), and
// rootKDF(rootKey, sendingRatchet.priv, SPK_b) yields the sending chain. The
// responder's SPK_b acts as their initial ratchet public key.
//
// sendingRatchet must be injected (in tests, eph[1] from the vectors) so the
// produced ciphertexts are reproducible.
func InitiateSession(p InitiatorParams, sendingRatchet keys.KeyPair) (*SessionState, error) {
	master, err := x3dhInitiator(p)
	if err != nil {
		return nil, err
	}
	rootKey, _ := deriveInitialRootChain(master)

	st := &SessionState{
		LocalIdentityPub:  pubKeyOf(p.LocalIdentity),
		RemoteIdentityPub: p.RemoteIdentity,
		RootKey:           rootKey,
		SendingRatchet:    sendingRatchet,
		TheirRatchetPub:   p.RemoteSignedPre,
		IsInitiator:       true,
		PendingBaseKey:    pubKeyOf(p.LocalBaseKey),
		LocalRegID:        0,
		Skipped:           map[skippedKey]messageKeys{},
	}
	// First sending chain: rootKDF over (rootKey, sendingRatchet.priv, SPK_b).
	newRoot, sendChain, err := rootKDF(rootKey, sendingRatchet.Priv, p.RemoteSignedPre)
	if err != nil {
		return nil, err
	}
	st.RootKey = newRoot
	st.SendingChain = sendChain
	st.HasSendingChain = true
	return st, nil
}

// RespondSession builds the initial SessionState for the responder (bob) from an
// incoming PreKeyWhisperMessage's X3DH inputs. After X3DH, bob's "current"
// ratchet key is SPK_b (the key alice ran rootKDF against), so bob can derive
// the receiving chain when alice's WhisperMessage carries her sending ratchet
// key.
func RespondSession(p ResponderParams, spk keys.KeyPair) (*SessionState, error) {
	master, err := x3dhResponder(p)
	if err != nil {
		return nil, err
	}
	rootKey, _ := deriveInitialRootChain(master)

	st := &SessionState{
		LocalIdentityPub:  pubKeyOf(p.LocalIdentity),
		RemoteIdentityPub: p.RemoteIdentity,
		RootKey:           rootKey,
		SendingRatchet:    spk, // bob's current ratchet = SPK_b
		IsInitiator:       false,
		Skipped:           map[skippedKey]messageKeys{},
	}
	return st, nil
}

var errNoSession = errors.New("signal: no session state")

// ProcessPreKeyMessage is the responder entry point: it parses an incoming
// PreKeyWhisperMessage, runs X3DH (responder), builds the session, and decrypts
// the embedded WhisperMessage. The caller supplies the local identity, the
// signed prekey pair referenced by SignedPreKeyId, and (if HasPreKeyID) the
// one-time prekey pair referenced by PreKeyId.
//
// On success it returns the plaintext and the established SessionState (to be
// persisted and reused for subsequent "msg" exchanges).
func ProcessPreKeyMessage(
	pkmsg []byte,
	localIdentity keys.KeyPair,
	signedPre keys.KeyPair,
	preKey *keys.KeyPair,
	localRegID uint32,
	opts ...SessionOption,
) ([]byte, *SessionState, error) {
	pm, err := ParsePreKeyWhisperMessage(pkmsg)
	if err != nil {
		return nil, nil, err
	}

	rp := ResponderParams{
		LocalIdentity:  localIdentity,
		LocalSignedPre: signedPre,
		RemoteIdentity: pm.IdentityKey,
		RemoteBaseKey:  pm.BaseKey,
		HasPreKey:      pm.HasPreKeyID,
	}
	if pm.HasPreKeyID {
		if preKey == nil {
			return nil, nil, errors.New("signal: prekey message references a one-time prekey but none provided")
		}
		rp.LocalPreKey = *preKey
	}

	st, err := RespondSession(rp, signedPre)
	if err != nil {
		return nil, nil, err
	}
	st.LocalRegID = localRegID
	for _, o := range opts {
		o(st)
	}

	cipher := NewSessionCipher(st)
	wm, signed, mac, err := parseWhisperMessage(pm.Message)
	if err != nil {
		return nil, nil, err
	}
	// MAC sender is the remote peer (alice), receiver is us (bob).
	pt, err := cipher.decryptWhisper(wm, signed, mac, st.RemoteIdentityPub, st.LocalIdentityPub)
	if err != nil {
		return nil, nil, err
	}
	return pt, st, nil
}
