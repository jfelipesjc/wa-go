// Package signal implements the subset of the Signal protocol (X3DH + Double
// Ratchet) that WhatsApp multi-device uses for 1:1 encrypted messages. It is
// validated against golden vectors produced by the libsignal that Baileys uses
// (testdata/signal/session_ab.json).
//
// Wire format (confirmed against the golden vectors):
//
//	ciphertext = versionByte || protobuf || mac[8]
//
// where versionByte = (3<<4)|3 = 0x33 for v3, the protobuf is either a
// WhisperMessage (type "msg") or a PreKeyWhisperMessage (type "pkmsg"), and the
// 8-byte MAC is HMAC-SHA256 truncated. For a PreKeyWhisperMessage the inner
// WhisperMessage (field 4) is itself a complete versionByte||protobuf||mac blob.
package signal

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

// CurrentVersion is the libsignal message version WhatsApp uses (v3). The wire
// version byte is (current<<4)|previous; for v3 both nibbles are 3 -> 0x33.
const CurrentVersion = 3

// versionByte returns the (current<<4)|previous byte. WhatsApp always uses
// current==previous==3.
func versionByte() byte { return (CurrentVersion << 4) | CurrentVersion }

// macLength is the number of MAC bytes appended to every Signal ciphertext.
const macLength = 8

// signalKeyLen is the length of a wire-format public key: 0x05 type byte plus 32
// raw Curve25519 bytes.
const signalKeyLen = 33

// WhisperMessage is the libsignal WhisperMessage (a.k.a. SignalMessage). Field
// numbers per WhisperTextProtocol.proto:
//
//	ratchetKey      = 1 (bytes, 33-byte 0x05-prefixed pub)
//	counter         = 2 (uint32)
//	previousCounter = 3 (uint32)
//	ciphertext      = 4 (bytes)
type WhisperMessage struct {
	RatchetKey      [signalKeyLen]byte
	Counter         uint32
	PreviousCounter uint32
	Ciphertext      []byte
}

// marshalBody serializes only the protobuf body (no version byte, no MAC). Field
// order matches libsignal exactly: 1,2,3,4.
func (m *WhisperMessage) marshalBody() []byte {
	var out []byte
	out = appendTagLen(out, 1, m.RatchetKey[:])
	out = appendVarintField(out, 2, uint64(m.Counter))
	out = appendVarintField(out, 3, uint64(m.PreviousCounter))
	out = appendTagLen(out, 4, m.Ciphertext)
	return out
}

// Serialize builds the full on-wire WhisperMessage: version || body || mac[8],
// where mac is computed by macFn over (version || body). macFn receives the
// bytes to be MAC'd and returns the full HMAC; only the first macLength bytes
// are appended.
func (m *WhisperMessage) Serialize(macFn func([]byte) []byte) []byte {
	body := m.marshalBody()
	signed := make([]byte, 0, 1+len(body))
	signed = append(signed, versionByte())
	signed = append(signed, body...)
	mac := macFn(signed)
	out := make([]byte, 0, len(signed)+macLength)
	out = append(out, signed...)
	out = append(out, mac[:macLength]...)
	return out
}

// parseWhisperMessage splits data into (version, body, mac) and decodes the
// protobuf body. It does not verify the MAC; the caller must do that with the
// derived macKey because the MAC input includes the identity keys.
func parseWhisperMessage(data []byte) (msg *WhisperMessage, signed, mac []byte, err error) {
	if len(data) < 1+macLength {
		return nil, nil, nil, errors.New("signal: whisper message too short")
	}
	if data[0] != versionByte() {
		return nil, nil, nil, fmt.Errorf("signal: unexpected version byte 0x%02x", data[0])
	}
	signed = data[:len(data)-macLength]
	mac = data[len(data)-macLength:]
	body := data[1 : len(data)-macLength]

	m := &WhisperMessage{}
	for len(body) > 0 {
		field, wire, n := readTag(body)
		if n == 0 {
			return nil, nil, nil, errors.New("signal: bad whisper field tag")
		}
		body = body[n:]
		switch {
		case field == 1 && wire == wireBytes:
			v, n2 := readBytes(body)
			if n2 == 0 || len(v) != signalKeyLen {
				return nil, nil, nil, errors.New("signal: bad ratchetKey")
			}
			copy(m.RatchetKey[:], v)
			body = body[n2:]
		case field == 2 && wire == wireVarint:
			v, n2 := readVarint(body)
			if n2 == 0 {
				return nil, nil, nil, errors.New("signal: bad counter")
			}
			m.Counter = uint32(v)
			body = body[n2:]
		case field == 3 && wire == wireVarint:
			v, n2 := readVarint(body)
			if n2 == 0 {
				return nil, nil, nil, errors.New("signal: bad previousCounter")
			}
			m.PreviousCounter = uint32(v)
			body = body[n2:]
		case field == 4 && wire == wireBytes:
			v, n2 := readBytes(body)
			if n2 == 0 {
				return nil, nil, nil, errors.New("signal: bad ciphertext")
			}
			m.Ciphertext = append([]byte(nil), v...)
			body = body[n2:]
		default:
			nn := skipField(body, wire)
			if nn == 0 {
				return nil, nil, nil, errors.New("signal: cannot skip whisper field")
			}
			body = body[nn:]
		}
	}
	return m, signed, mac, nil
}

// PreKeyWhisperMessage is the libsignal PreKeyWhisperMessage (a.k.a.
// PreKeySignalMessage). Field numbers per WhisperTextProtocol.proto:
//
//	preKeyId        = 1 (uint32)
//	baseKey         = 2 (bytes, 33-byte pub)
//	identityKey     = 3 (bytes, 33-byte pub)
//	message         = 4 (bytes, a full serialized WhisperMessage incl. version+mac)
//	registrationId  = 5 (uint32)
//	signedPreKeyId  = 6 (uint32)
//
// HasPreKeyID indicates whether a one-time preKeyId was present (libsignal omits
// the field when no one-time prekey is used).
type PreKeyWhisperMessage struct {
	RegistrationID uint32
	PreKeyID       uint32
	HasPreKeyID    bool
	SignedPreKeyID uint32
	BaseKey        [signalKeyLen]byte
	IdentityKey    [signalKeyLen]byte
	Message        []byte
}

// Serialize builds the full on-wire PreKeyWhisperMessage: version || protobuf.
// There is no outer MAC; the integrity of a PreKeyWhisperMessage is the MAC of
// the embedded WhisperMessage. Field order matches libsignal: 1,2,3,4,5,6.
func (m *PreKeyWhisperMessage) Serialize() []byte {
	var body []byte
	if m.HasPreKeyID {
		body = appendVarintField(body, 1, uint64(m.PreKeyID))
	}
	body = appendTagLen(body, 2, m.BaseKey[:])
	body = appendTagLen(body, 3, m.IdentityKey[:])
	body = appendTagLen(body, 4, m.Message)
	body = appendVarintField(body, 5, uint64(m.RegistrationID))
	body = appendVarintField(body, 6, uint64(m.SignedPreKeyID))

	out := make([]byte, 0, 1+len(body))
	out = append(out, versionByte())
	out = append(out, body...)
	return out
}

// ParsePreKeyWhisperMessage decodes a full on-wire PreKeyWhisperMessage.
func ParsePreKeyWhisperMessage(data []byte) (*PreKeyWhisperMessage, error) {
	if len(data) < 1 {
		return nil, errors.New("signal: prekey message empty")
	}
	if data[0] != versionByte() {
		return nil, fmt.Errorf("signal: unexpected prekey version byte 0x%02x", data[0])
	}
	body := data[1:]
	m := &PreKeyWhisperMessage{}
	for len(body) > 0 {
		field, wire, n := readTag(body)
		if n == 0 {
			return nil, errors.New("signal: bad prekey field tag")
		}
		body = body[n:]
		switch {
		case field == 1 && wire == wireVarint:
			v, n2 := readVarint(body)
			if n2 == 0 {
				return nil, errors.New("signal: bad preKeyId")
			}
			m.PreKeyID = uint32(v)
			m.HasPreKeyID = true
			body = body[n2:]
		case field == 2 && wire == wireBytes:
			v, n2 := readBytes(body)
			if n2 == 0 || len(v) != signalKeyLen {
				return nil, errors.New("signal: bad baseKey")
			}
			copy(m.BaseKey[:], v)
			body = body[n2:]
		case field == 3 && wire == wireBytes:
			v, n2 := readBytes(body)
			if n2 == 0 || len(v) != signalKeyLen {
				return nil, errors.New("signal: bad identityKey")
			}
			copy(m.IdentityKey[:], v)
			body = body[n2:]
		case field == 4 && wire == wireBytes:
			v, n2 := readBytes(body)
			if n2 == 0 {
				return nil, errors.New("signal: bad embedded message")
			}
			m.Message = append([]byte(nil), v...)
			body = body[n2:]
		case field == 5 && wire == wireVarint:
			v, n2 := readVarint(body)
			if n2 == 0 {
				return nil, errors.New("signal: bad registrationId")
			}
			m.RegistrationID = uint32(v)
			body = body[n2:]
		case field == 6 && wire == wireVarint:
			v, n2 := readVarint(body)
			if n2 == 0 {
				return nil, errors.New("signal: bad signedPreKeyId")
			}
			m.SignedPreKeyID = uint32(v)
			body = body[n2:]
		default:
			nn := skipField(body, wire)
			if nn == 0 {
				return nil, errors.New("signal: cannot skip prekey field")
			}
			body = body[nn:]
		}
	}
	return m, nil
}

// computeMAC returns HMAC-SHA256(macKey, senderIdentity(33) || receiverIdentity(33)
// || data) — the libsignal/WhatsApp message MAC input. The caller passes the
// already-version-prefixed body as data. The full 32-byte HMAC is returned;
// callers truncate to macLength.
//
// senderIdentity/receiverIdentity are the 33-byte 0x05-prefixed identity public
// keys. For an a->b message the sender is alice and the receiver is bob.
func computeMAC(macKey, senderIdentity, receiverIdentity, data []byte) []byte {
	h := hmac.New(sha256.New, macKey)
	h.Write(senderIdentity)
	h.Write(receiverIdentity)
	h.Write(data)
	return h.Sum(nil)
}

// verifyMAC recomputes the MAC over signed (version||body) and compares it to the
// truncated mac in constant time.
func verifyMAC(macKey, senderIdentity, receiverIdentity, signed, mac []byte) bool {
	full := computeMAC(macKey, senderIdentity, receiverIdentity, signed)
	return hmac.Equal(full[:macLength], mac)
}

// --- minimal protobuf wire helpers (hand-rolled to avoid codegen) ---

const (
	wireVarint = 0
	wireBytes  = 2
)

func appendVarint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

func appendVarintField(b []byte, field int, v uint64) []byte {
	b = appendVarint(b, uint64(field)<<3|wireVarint)
	return appendVarint(b, v)
}

func appendTagLen(b []byte, field int, val []byte) []byte {
	b = appendVarint(b, uint64(field)<<3|wireBytes)
	b = appendVarint(b, uint64(len(val)))
	return append(b, val...)
}

// readTag reads a protobuf tag, returning field number, wire type, and bytes
// consumed (0 on error).
func readTag(b []byte) (field, wire, n int) {
	v, nn := readVarint(b)
	if nn == 0 {
		return 0, 0, 0
	}
	return int(v >> 3), int(v & 7), nn
}

func readVarint(b []byte) (uint64, int) {
	v, n := binary.Uvarint(b)
	if n <= 0 {
		return 0, 0
	}
	return v, n
}

// readBytes reads a length-delimited field value (length varint already at b[0]).
func readBytes(b []byte) ([]byte, int) {
	l, n := readVarint(b)
	if n == 0 {
		return nil, 0
	}
	if uint64(len(b)-n) < l {
		return nil, 0
	}
	return b[n : n+int(l)], n + int(l)
}

func skipField(b []byte, wire int) int {
	switch wire {
	case wireVarint:
		_, n := readVarint(b)
		return n
	case wireBytes:
		_, n := readBytes(b)
		return n
	default:
		return 0
	}
}
