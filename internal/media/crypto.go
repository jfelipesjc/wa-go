package media

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
)

// macLen is the length the HMAC-SHA256 tag is truncated to before being appended
// to the ciphertext, matching Baileys (hmac.digest().slice(0, 10)).
const macLen = 10

// ErrBadMAC is returned by Decrypt when the appended MAC does not match the
// recomputed HMAC over iv||ciphertext (tampering or wrong key/type).
var ErrBadMAC = errors.New("media: MAC verification failed")

// ErrBadPadding is returned by Decrypt when the decrypted plaintext does not
// carry valid PKCS#7 padding.
var ErrBadPadding = errors.New("media: invalid PKCS#7 padding")

// ErrShortBlob is returned when an encrypted blob is too small to contain a MAC
// and at least one AES block.
var ErrShortBlob = errors.New("media: encrypted blob too short")

// Encrypt encrypts plaintext for the given media type using mediaKey.
//
// It derives iv/cipherKey/macKey via ExpandMediaKey, AES-256-CBC encrypts the
// PKCS#7-padded plaintext, computes mac = HMAC-SHA256(macKey, iv||ciphertext)
// truncated to 10 bytes, and returns enc = ciphertext || mac. It also returns
// fileSha256 = SHA256(plaintext) and fileEncSha256 = SHA256(enc), the two
// digests that go in the protobuf Message. The output is deterministic for a
// fixed mediaKey (the IV is HKDF-derived, not random).
func Encrypt(plaintext []byte, mediaKey [32]byte, mediaType MediaType) (enc []byte, fileSha256, fileEncSha256 [32]byte, err error) {
	iv, cipherKey, macKey, _, err := ExpandMediaKey(mediaKey, mediaType)
	if err != nil {
		return nil, fileSha256, fileEncSha256, err
	}

	block, err := aes.NewCipher(cipherKey[:])
	if err != nil {
		return nil, fileSha256, fileEncSha256, fmt.Errorf("media: aes cipher: %w", err)
	}

	padded := pkcs7Pad(plaintext, aes.BlockSize)
	ciphertext := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv[:]).CryptBlocks(ciphertext, padded)

	mac := computeMAC(macKey, iv[:], ciphertext)

	enc = make([]byte, 0, len(ciphertext)+macLen)
	enc = append(enc, ciphertext...)
	enc = append(enc, mac...)

	fileSha256 = sha256.Sum256(plaintext)
	fileEncSha256 = sha256.Sum256(enc)
	return enc, fileSha256, fileEncSha256, nil
}

// Decrypt reverses Encrypt: it splits enc into ciphertext||mac, verifies the MAC
// in constant time, AES-256-CBC decrypts the ciphertext, and strips PKCS#7
// padding. It returns ErrBadMAC on tampering, ErrBadPadding on malformed
// padding, and ErrShortBlob if enc is too small.
func Decrypt(enc []byte, mediaKey [32]byte, mediaType MediaType) (plaintext []byte, err error) {
	if len(enc) < macLen+aes.BlockSize {
		return nil, ErrShortBlob
	}

	iv, cipherKey, macKey, _, err := ExpandMediaKey(mediaKey, mediaType)
	if err != nil {
		return nil, err
	}

	ciphertext := enc[:len(enc)-macLen]
	gotMAC := enc[len(enc)-macLen:]

	if len(ciphertext)%aes.BlockSize != 0 {
		return nil, ErrShortBlob
	}

	wantMAC := computeMAC(macKey, iv[:], ciphertext)
	if subtle.ConstantTimeCompare(gotMAC, wantMAC) != 1 {
		return nil, ErrBadMAC
	}

	block, err := aes.NewCipher(cipherKey[:])
	if err != nil {
		return nil, fmt.Errorf("media: aes cipher: %w", err)
	}
	padded := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv[:]).CryptBlocks(padded, ciphertext)

	return pkcs7Unpad(padded, aes.BlockSize)
}

// computeMAC returns HMAC-SHA256(macKey, iv || ciphertext) truncated to macLen
// bytes, matching Baileys' Crypto.createHmac('sha256', macKey).update(iv) then
// update(ciphertext), digest().slice(0,10).
func computeMAC(macKey [32]byte, iv, ciphertext []byte) []byte {
	h := hmac.New(sha256.New, macKey[:])
	h.Write(iv)
	h.Write(ciphertext)
	return h.Sum(nil)[:macLen]
}

// pkcs7Pad appends PKCS#7 padding so the result is a multiple of blockSize. A
// full block of padding is added when the input is already aligned.
func pkcs7Pad(data []byte, blockSize int) []byte {
	pad := blockSize - len(data)%blockSize
	return append(data, bytes.Repeat([]byte{byte(pad)}, pad)...)
}

// pkcs7Unpad removes and validates PKCS#7 padding. It returns ErrBadPadding if
// the length is not block-aligned or the padding bytes are inconsistent.
func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	n := len(data)
	if n == 0 || n%blockSize != 0 {
		return nil, ErrBadPadding
	}
	pad := int(data[n-1])
	if pad == 0 || pad > blockSize || pad > n {
		return nil, ErrBadPadding
	}
	for _, b := range data[n-pad:] {
		if int(b) != pad {
			return nil, ErrBadPadding
		}
	}
	return data[:n-pad], nil
}
