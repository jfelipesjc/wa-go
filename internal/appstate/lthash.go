// Package appstate implements WhatsApp Web "app state" sync: decoding the
// encrypted SyncdPatch blobs the server pushes (contacts, chat names, mute,
// read, pin, archive, ...) and maintaining the per-collection state with an
// LTHash integrity check.
//
// LTHash ("WhatsApp Patch Integrity") is a homomorphic, summation-based hash.
// Each mutation contributes a 32-byte valueMac which is expanded with
// HKDF-SHA256 to 128 bytes; those 128 bytes are read as 64 little-endian uint16
// components. The running 128-byte state hash is the component-wise sum (mod
// 2^16) of every live mutation's expansion. Because addition is commutative and
// invertible, a SET adds its expansion and a REMOVE subtracts the previously
// added expansion — so the same final hash is reached regardless of mutation
// order, and individual mutations can be undone. This mirrors
// LTHashAntiTampering.subtractThenAdd in whatsapp-rust-bridge (the WASM crypto
// backing @whiskeysockets/baileys v7).
package appstate

import (
	"crypto/sha256"
	"encoding/binary"

	"golang.org/x/crypto/hkdf"
)

const (
	// ltHashLen is the byte length of an LTHash state (64 LE uint16 components).
	ltHashLen = 128
	// ltHashInfo is the HKDF info string used to expand a valueMac.
	ltHashInfo = "WhatsApp Patch Integrity"
)

// expandValueMac expands a 32-byte valueMac to a 128-byte LTHash contribution
// using HKDF-SHA256 with an empty salt and the "WhatsApp Patch Integrity" info.
func expandValueMac(valueMac []byte) []byte {
	r := hkdf.New(sha256.New, valueMac, nil, []byte(ltHashInfo))
	out := make([]byte, ltHashLen)
	// hkdf.Read on the standard reader fills exactly len(out) bytes or errors;
	// 128 bytes is far under the SHA-256 HKDF limit (255*32), so it never errors.
	if _, err := readFull(r, out); err != nil {
		panic("appstate: hkdf expand failed: " + err.Error())
	}
	return out
}

// readFull is io.ReadFull specialised to avoid an extra import surface; it reads
// len(buf) bytes from r.
func readFull(r interface{ Read([]byte) (int, error) }, buf []byte) (int, error) {
	n := 0
	for n < len(buf) {
		m, err := r.Read(buf[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// ltHashAddInto adds a 128-byte expansion into the 64 LE-uint16 accumulator.
func ltHashAddInto(acc []byte, expansion []byte) {
	for i := 0; i < ltHashLen; i += 2 {
		a := binary.LittleEndian.Uint16(acc[i:])
		b := binary.LittleEndian.Uint16(expansion[i:])
		binary.LittleEndian.PutUint16(acc[i:], a+b) // mod 2^16 via uint16 wrap
	}
}

// ltHashSubInto subtracts a 128-byte expansion from the accumulator.
func ltHashSubInto(acc []byte, expansion []byte) {
	for i := 0; i < ltHashLen; i += 2 {
		a := binary.LittleEndian.Uint16(acc[i:])
		b := binary.LittleEndian.Uint16(expansion[i:])
		binary.LittleEndian.PutUint16(acc[i:], a-b) // mod 2^16 via uint16 wrap
	}
}

// subtractThenAdd reproduces LTHashAntiTampering.subtractThenAdd: starting from
// base (128 bytes), subtract every valueMac in sub then add every valueMac in
// add (each expanded to 128 bytes), component-wise mod 2^16. It returns a new
// 128-byte slice and does not mutate base.
func subtractThenAdd(base []byte, sub, add [][]byte) []byte {
	acc := make([]byte, ltHashLen)
	copy(acc, base)
	for _, v := range sub {
		ltHashSubInto(acc, expandValueMac(v))
	}
	for _, v := range add {
		ltHashAddInto(acc, expandValueMac(v))
	}
	return acc
}
