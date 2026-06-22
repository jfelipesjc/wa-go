// Package media implements WhatsApp's media payload encryption: the symmetric
// crypto layer that cifrar/decifrar image/audio/video/document blobs. It mirrors
// Baileys' getMediaKeys + encryptedStream / downloadEncryptedContent
// (harness/node_modules/@whiskeysockets/baileys/lib/Utils/messages-media.js) but
// covers ONLY the crypto — no network upload/download.
//
// Given a 32-byte mediaKey and a MediaType, HKDF-SHA256 expands the key to 112
// bytes laid out as:
//
//	iv (16) || cipherKey (32) || macKey (32) || refKey (32)
//
// The payload is AES-256-CBC(cipherKey, iv) with PKCS#7 padding; the integrity
// tag is HMAC-SHA256(macKey, iv || ciphertext) truncated to the first 10 bytes.
// The on-wire blob is ciphertext || mac.
package media

import (
	"crypto/sha256"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

// MediaType identifies a WhatsApp media category. Each type maps to a distinct
// HKDF info string, so the same mediaKey derives different keys per type.
type MediaType int

const (
	// Image is a photo payload ("WhatsApp Image Keys").
	Image MediaType = iota
	// Video is a video payload ("WhatsApp Video Keys").
	Video
	// Audio is an audio / voice-note payload ("WhatsApp Audio Keys").
	Audio
	// Document is a generic file payload ("WhatsApp Document Keys").
	Document
	// History is a history-sync payload ("WhatsApp History Keys"), the encrypted
	// + zlib-compressed HistorySync blob referenced by a HistorySyncNotification
	// (Baileys mediaType "md-msg-hist", HKDF mapping "History").
	History
	// AppState is an app-state snapshot/patch external blob ("WhatsApp App State
	// Keys", Baileys mediaType "md-app-state", HKDF mapping "App State").
	AppState
)

// info returns the exact HKDF info string for the media type, matching Baileys'
// hkdfInfoKey(type) = `WhatsApp ${MEDIA_HKDF_KEY_MAPPING[type]} Keys` with the
// mapping image->Image, video->Video, audio->Audio, document->Document
// (harness/node_modules/@whiskeysockets/baileys/lib/Defaults/index.js).
func (t MediaType) info() (string, error) {
	switch t {
	case Image:
		return "WhatsApp Image Keys", nil
	case Video:
		return "WhatsApp Video Keys", nil
	case Audio:
		return "WhatsApp Audio Keys", nil
	case Document:
		return "WhatsApp Document Keys", nil
	case History:
		return "WhatsApp History Keys", nil
	case AppState:
		return "WhatsApp App State Keys", nil
	default:
		return "", fmt.Errorf("media: unknown media type %d", int(t))
	}
}

// String returns the lowercase Baileys-style name of the media type, useful for
// logging and selecting the upload path. Unknown values format as "media(N)".
func (t MediaType) String() string {
	switch t {
	case Image:
		return "image"
	case Video:
		return "video"
	case Audio:
		return "audio"
	case Document:
		return "document"
	case History:
		return "history"
	case AppState:
		return "app-state"
	default:
		return fmt.Sprintf("media(%d)", int(t))
	}
}

// Derived layout sizes (bytes) inside the 112-byte HKDF expansion.
const (
	ivLen        = 16
	cipherKeyLen = 32
	macKeyLen    = 32
	refKeyLen    = 32
	expandedLen  = ivLen + cipherKeyLen + macKeyLen + refKeyLen // 112
)

// ExpandMediaKey derives the four media sub-keys from a 32-byte mediaKey for the
// given media type via HKDF-SHA256 (empty salt) expanded to 112 bytes, sliced as
// iv(16) || cipherKey(32) || macKey(32) || refKey(32). This matches Baileys'
// getMediaKeys (which exposes iv/cipherKey/macKey) plus the trailing refKey.
//
// The empty salt mirrors the WhatsApp/rust-bridge hkdf default: per RFC 5869,
// HKDF-Extract with no salt uses a HashLen-byte zero string, which
// golang.org/x/crypto/hkdf produces when salt is nil.
func ExpandMediaKey(mediaKey [32]byte, mediaType MediaType) (iv [ivLen]byte, cipherKey [cipherKeyLen]byte, macKey [macKeyLen]byte, refKey [refKeyLen]byte, err error) {
	info, err := mediaType.info()
	if err != nil {
		return iv, cipherKey, macKey, refKey, err
	}

	r := hkdf.New(sha256.New, mediaKey[:], nil, []byte(info))
	var expanded [expandedLen]byte
	if _, err = io.ReadFull(r, expanded[:]); err != nil {
		// HKDF over a fixed 112-byte output cannot fail in practice.
		return iv, cipherKey, macKey, refKey, fmt.Errorf("media: hkdf expand: %w", err)
	}

	copy(iv[:], expanded[0:16])
	copy(cipherKey[:], expanded[16:48])
	copy(macKey[:], expanded[48:80])
	copy(refKey[:], expanded[80:112])
	return iv, cipherKey, macKey, refKey, nil
}
