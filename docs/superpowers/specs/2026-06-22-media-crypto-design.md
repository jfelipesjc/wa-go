# Media Crypto Design — 2026-06-22

The cryptographic layer for WhatsApp media payloads (image/audio/video/document):
cifrar/decifrar blobs. **Crypto only** — upload/download de rede fica para depois
(precisa de sessão viva). Package `internal/media`.

## HKDF key expansion

Given a 32-byte `mediaKey` and a `MediaType`, derive 112 bytes via
HKDF-SHA256 with **empty salt** and a per-type `info` string:

```
expanded = HKDF-SHA256(ikm=mediaKey, salt=<empty>, info=<type string>, L=112)
iv        = expanded[0:16]
cipherKey = expanded[16:48]
macKey    = expanded[48:80]
refKey    = expanded[80:112]   // unused by encrypt/decrypt; carried for completeness
```

Empty salt = RFC 5869 HKDF-Extract with a HashLen (32) zero string, which
`golang.org/x/crypto/hkdf` produces for `salt=nil`. This matches the WhatsApp
rust-bridge `hkdf` default that Baileys' `getMediaKeys` uses
(`harness/node_modules/@whiskeysockets/baileys/lib/Utils/messages-media.js`).

### Exact info strings per type (confirmed in Baileys)

`hkdfInfoKey(type)` = `` `WhatsApp ${MEDIA_HKDF_KEY_MAPPING[type]} Keys` ``
(`lib/Defaults/index.js`):

| MediaType | info string             |
|-----------|-------------------------|
| Image     | `WhatsApp Image Keys`   |
| Video     | `WhatsApp Video Keys`   |
| Audio     | `WhatsApp Audio Keys`   |
| Document  | `WhatsApp Document Keys`|

## Encrypt / Decrypt

- `enc = AES-256-CBC(cipherKey, iv, PKCS#7(plaintext))`
- `mac = HMAC-SHA256(macKey, iv || ciphertext)[:10]` (first **10** bytes)
- blob on the wire = `ciphertext || mac`
- `fileSha256    = SHA256(plaintext)`
- `fileEncSha256 = SHA256(blob)` (i.e. `SHA256(ciphertext || mac)`)

`fileSha256`/`fileEncSha256` go into the protobuf Message. Decrypt splits off the
trailing 10-byte MAC, recomputes HMAC over `iv||ciphertext`, compares in constant
time (`crypto/subtle`), then AES-CBC-decrypts and strips PKCS#7.

The IV is HKDF-derived (not random), so `Encrypt` is deterministic for a fixed
`mediaKey` — that is what lets the golden vectors check the ciphertext
byte-for-byte.

## API (`internal/media`)

- `keys.go`: `MediaType` enum (`Image/Video/Audio/Document`) with `info()` /
  `String()`; `ExpandMediaKey(mediaKey [32]byte, mediaType) (iv[16], cipherKey[32], macKey[32], refKey[32], err)`.
- `crypto.go`: `Encrypt(plaintext, mediaKey, mediaType) (enc, fileSha256, fileEncSha256, err)`;
  `Decrypt(enc, mediaKey, mediaType) (plaintext, err)`. Errors: `ErrBadMAC`,
  `ErrBadPadding`, `ErrShortBlob`.

## Golden vectors

`harness/gen_media_vectors.mjs` uses Baileys' public API (`getMediaKeys`, `hkdf`,
`hkdfInfoKey`) + node `crypto` (the same AES/HMAC steps `encryptedStream` runs) to
produce `testdata/media/media_vectors.json`: fixed `mediaKey` (0x00..0x1f), two
plaintexts (a known string and a deterministic 70 000-byte buffer), four media
types. Each case has iv/cipher/mac/ref keys, `ciphertextHex` (ct||mac),
`fileSha256`, `fileEncSha256`. The generator self-checks a full roundtrip + that
the 112-byte layout matches `getMediaKeys`.

## Tests (offline)

- `ExpandMediaKey` matches iv/cipher/mac/ref of every vector; 4 types present.
- `Decrypt(vector.ct)` == expected plaintext.
- `Encrypt(plaintext)` reproduces `ciphertextHex` byte-for-byte + both SHAs match.
- Roundtrip across sizes 0..70000; tampered MAC/ciphertext → `ErrBadMAC`;
  wrong type → `ErrBadMAC`; short blob → `ErrShortBlob`; unknown type → error;
  distinct types derive distinct keys.

## Out of scope (later)

Network upload (`/mms/...` hosts, auth tokens) and download
(`downloadEncryptedContent` range/streaming), sidecar generation for streamable
audio/video, thumbnails. All require a live session.
