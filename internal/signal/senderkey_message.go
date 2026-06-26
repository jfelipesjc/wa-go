package signal

import (
	"errors"
	"fmt"

	"github.com/jfelipesjc/wa-go/internal/keys"
)

// This file implements the wire formats for Signal group (Sender Key) messages,
// matching @whiskeysockets/baileys/lib/Signal/Group/{sender-key-message,
// sender-key-distribution-message}.js and validated against
// testdata/signal/group_ab.json.
//
// SenderKeyMessage wire:
//
//	versionByte (0x33) || protobuf{ id=1 uint32, iteration=2 uint32, ciphertext=3 bytes } || signature[64]
//
// The signature is a (non-randomized) XEdDSA Curve25519 signature over
// (versionByte || protobuf), made with the sender's signing private key and
// verified with the sender's signing public key. There is no HMAC.
//
// SenderKeyDistributionMessage wire:
//
//	versionByte (0x33) || protobuf{ id=1 uint32, iteration=2 uint32, chainKey=3 bytes, signingKey=4 bytes }
//
// signingKey is the 33-byte 0x05-prefixed Curve25519 public key.

const senderKeySignatureLen = 64

// SenderKeyMessage is a parsed group ciphertext.
type SenderKeyMessage struct {
	KeyID      uint32
	Iteration  uint32
	Ciphertext []byte
	Signature  [senderKeySignatureLen]byte
}

// marshalSenderKeyBody serializes the protobuf body (no version, no signature).
// Field order matches libsignal/protobufjs: 1,2,3.
func marshalSenderKeyBody(keyID, iteration uint32, ciphertext []byte) []byte {
	var out []byte
	out = appendVarintField(out, 1, uint64(keyID))
	out = appendVarintField(out, 2, uint64(iteration))
	out = appendTagLen(out, 3, ciphertext)
	return out
}

// SerializeSenderKeyMessage builds the full wire SenderKeyMessage and signs it.
// The signature is computed over (versionByte || body) with signingPriv.
func SerializeSenderKeyMessage(keyID, iteration uint32, ciphertext []byte, signingPriv [32]byte) ([]byte, error) {
	body := marshalSenderKeyBody(keyID, iteration, ciphertext)
	signed := make([]byte, 0, 1+len(body))
	signed = append(signed, versionByte())
	signed = append(signed, body...)

	sig, err := keys.Sign(signingPriv, signed)
	if err != nil {
		return nil, fmt.Errorf("signal: sender key sign: %w", err)
	}
	out := make([]byte, 0, len(signed)+senderKeySignatureLen)
	out = append(out, signed...)
	out = append(out, sig[:]...)
	return out, nil
}

// ParseSenderKeyMessage splits the wire bytes and decodes the protobuf. It does
// NOT verify the signature; the caller verifies with the sender's signing public
// key (which lives in the SenderKeyState, not in the message).
func ParseSenderKeyMessage(data []byte) (*SenderKeyMessage, error) {
	if len(data) < 1+senderKeySignatureLen {
		return nil, errors.New("signal: sender key message too short")
	}
	if data[0] != versionByte() {
		return nil, fmt.Errorf("signal: unexpected sender key version byte 0x%02x", data[0])
	}
	body := data[1 : len(data)-senderKeySignatureLen]

	m := &SenderKeyMessage{}
	copy(m.Signature[:], data[len(data)-senderKeySignatureLen:])

	for len(body) > 0 {
		field, wire, n := readTag(body)
		if n == 0 {
			return nil, errors.New("signal: bad sender key field tag")
		}
		body = body[n:]
		switch {
		case field == 1 && wire == wireVarint:
			v, n2 := readVarint(body)
			if n2 == 0 {
				return nil, errors.New("signal: bad sender key id")
			}
			m.KeyID = uint32(v)
			body = body[n2:]
		case field == 2 && wire == wireVarint:
			v, n2 := readVarint(body)
			if n2 == 0 {
				return nil, errors.New("signal: bad sender key iteration")
			}
			m.Iteration = uint32(v)
			body = body[n2:]
		case field == 3 && wire == wireBytes:
			v, n2 := readBytes(body)
			if n2 == 0 {
				return nil, errors.New("signal: bad sender key ciphertext")
			}
			m.Ciphertext = append([]byte(nil), v...)
			body = body[n2:]
		default:
			nn := skipField(body, wire)
			if nn == 0 {
				return nil, errors.New("signal: cannot skip sender key field")
			}
			body = body[nn:]
		}
	}
	return m, nil
}

// VerifySignature checks the message signature against the 33-byte 0x05-prefixed
// signing public key. The signed region is everything before the trailing 64
// signature bytes (version || protobuf).
func (m *SenderKeyMessage) VerifySignature(data []byte, signingPub [signalKeyLen]byte) bool {
	if len(data) < senderKeySignatureLen {
		return false
	}
	signed := data[:len(data)-senderKeySignatureLen]
	var rawPub [32]byte
	copy(rawPub[:], signingPub[1:])
	return keys.Verify(rawPub, signed, m.Signature)
}

// SenderKeyDistributionMessage carries a sender's initial chain key + signing
// public key so peers can decrypt that sender's group messages.
type SenderKeyDistributionMessage struct {
	KeyID      uint32
	Iteration  uint32
	ChainKey   [32]byte
	SigningPub [signalKeyLen]byte // 33-byte 0x05-prefixed
}

// SerializeSenderKeyDistributionMessage builds the full wire SKDM.
func SerializeSenderKeyDistributionMessage(keyID, iteration uint32, chainKey [32]byte, signingPub [signalKeyLen]byte) []byte {
	var body []byte
	body = appendVarintField(body, 1, uint64(keyID))
	body = appendVarintField(body, 2, uint64(iteration))
	body = appendTagLen(body, 3, chainKey[:])
	body = appendTagLen(body, 4, signingPub[:])

	out := make([]byte, 0, 1+len(body))
	out = append(out, versionByte())
	out = append(out, body...)
	return out
}

// ParseSenderKeyDistributionMessage decodes a full wire SKDM.
func ParseSenderKeyDistributionMessage(data []byte) (*SenderKeyDistributionMessage, error) {
	if len(data) < 1 {
		return nil, errors.New("signal: skdm empty")
	}
	if data[0] != versionByte() {
		return nil, fmt.Errorf("signal: unexpected skdm version byte 0x%02x", data[0])
	}
	body := data[1:]
	m := &SenderKeyDistributionMessage{}
	var gotChain, gotSigning bool
	for len(body) > 0 {
		field, wire, n := readTag(body)
		if n == 0 {
			return nil, errors.New("signal: bad skdm field tag")
		}
		body = body[n:]
		switch {
		case field == 1 && wire == wireVarint:
			v, n2 := readVarint(body)
			if n2 == 0 {
				return nil, errors.New("signal: bad skdm id")
			}
			m.KeyID = uint32(v)
			body = body[n2:]
		case field == 2 && wire == wireVarint:
			v, n2 := readVarint(body)
			if n2 == 0 {
				return nil, errors.New("signal: bad skdm iteration")
			}
			m.Iteration = uint32(v)
			body = body[n2:]
		case field == 3 && wire == wireBytes:
			v, n2 := readBytes(body)
			if n2 == 0 || len(v) != 32 {
				return nil, errors.New("signal: bad skdm chainKey")
			}
			copy(m.ChainKey[:], v)
			gotChain = true
			body = body[n2:]
		case field == 4 && wire == wireBytes:
			v, n2 := readBytes(body)
			if n2 == 0 || len(v) != signalKeyLen {
				return nil, errors.New("signal: bad skdm signingKey")
			}
			copy(m.SigningPub[:], v)
			gotSigning = true
			body = body[n2:]
		default:
			nn := skipField(body, wire)
			if nn == 0 {
				return nil, errors.New("signal: cannot skip skdm field")
			}
			body = body[nn:]
		}
	}
	if !gotChain || !gotSigning {
		return nil, errors.New("signal: skdm missing chainKey or signingKey")
	}
	return m, nil
}
