package wire

import (
	"encoding/binary"
	"fmt"
)

// Minimal protobuf helpers for the Noise handshake messages.
//
// We only need a handful of length-delimited (wire type 2) fields, so we
// hand-encode/decode instead of pulling in protoc-generated code. The field
// numbers below are taken verbatim from
// @whiskeysockets/baileys/WAProto/WAProto.proto:
//
//	message HandshakeMessage {
//	    optional ClientHello  clientHello  = 2;
//	    optional ServerHello  serverHello  = 3;
//	    optional ClientFinish clientFinish = 4;
//	}
//	message ClientHello  { bytes ephemeral = 1; bytes static = 2; bytes payload = 3; }
//	message ServerHello  { bytes ephemeral = 1; bytes static = 2; bytes payload = 3; }
//	message ClientFinish { bytes static    = 1; bytes payload  = 2; }
//
// All fields used here are wire type 2 (length-delimited).

const wireTypeLengthDelimited = 2

// pbTag builds the protobuf tag byte(s) for a field number with wire type 2.
func pbTag(field int) []byte {
	return appendVarint(nil, uint64(field<<3|wireTypeLengthDelimited))
}

func appendVarint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

// pbField appends a single length-delimited field (tag + length + value).
func pbField(b []byte, field int, value []byte) []byte {
	b = append(b, pbTag(field)...)
	b = appendVarint(b, uint64(len(value)))
	b = append(b, value...)
	return b
}

// pbHelloMessage builds a clientHello (HandshakeMessage field 2) carrying only
// the ephemeral public key (ClientHello field 1), matching Baileys exactly.
func encodeClientHello(ephemeral []byte) []byte {
	inner := pbField(nil, 1, ephemeral) // ClientHello.ephemeral = 1
	return pbField(nil, 2, inner)       // HandshakeMessage.clientHello = 2
}

// encodeClientFinish builds HandshakeMessage{ clientFinish: { static, payload } }.
func encodeClientFinish(static, payload []byte) []byte {
	inner := pbField(nil, 1, static)   // ClientFinish.static = 1
	inner = pbField(inner, 2, payload) // ClientFinish.payload = 2
	return pbField(nil, 4, inner)      // HandshakeMessage.clientFinish = 4
}

// hsFields holds the three length-delimited byte fields shared by the
// ClientHello / ServerHello / ClientFinish messages.
type hsFields struct {
	f1, f2, f3 []byte
}

// parseLengthDelimited reads the next length-delimited field at b, returning the
// field number, the value bytes, and the number of bytes consumed.
func parseLengthDelimited(b []byte) (field int, value []byte, n int, err error) {
	tag, tn := binary.Uvarint(b)
	if tn <= 0 {
		return 0, nil, 0, fmt.Errorf("hsproto: bad tag varint")
	}
	wt := int(tag & 0x7)
	field = int(tag >> 3)
	off := tn
	if wt != wireTypeLengthDelimited {
		return 0, nil, 0, fmt.Errorf("hsproto: unexpected wire type %d for field %d", wt, field)
	}
	ln, ln2 := binary.Uvarint(b[off:])
	if ln2 <= 0 {
		return 0, nil, 0, fmt.Errorf("hsproto: bad length varint")
	}
	off += ln2
	end := off + int(ln)
	if end > len(b) {
		return 0, nil, 0, fmt.Errorf("hsproto: field %d overruns buffer", field)
	}
	return field, b[off:end], end, nil
}

// parseHSFields decodes a message consisting only of length-delimited fields
// 1..3 into hsFields.
func parseHSFields(b []byte) (hsFields, error) {
	var out hsFields
	for len(b) > 0 {
		field, value, n, err := parseLengthDelimited(b)
		if err != nil {
			return hsFields{}, err
		}
		switch field {
		case 1:
			out.f1 = value
		case 2:
			out.f2 = value
		case 3:
			out.f3 = value
		}
		b = b[n:]
	}
	return out, nil
}

// parseHandshakeMessage extracts one of clientHello(2)/serverHello(3)/
// clientFinish(4) inner messages by field number.
func parseHandshakeMessage(b []byte, want int) (hsFields, error) {
	for len(b) > 0 {
		field, value, n, err := parseLengthDelimited(b)
		if err != nil {
			return hsFields{}, err
		}
		if field == want {
			return parseHSFields(value)
		}
		b = b[n:]
	}
	return hsFields{}, fmt.Errorf("hsproto: handshake field %d not found", want)
}
