# Group (Sender Keys) — Signal group protocol for wa-go

Date: 2026-06-22
Extends: `internal/signal/` (1:1 X3DH + Double Ratchet already present)

## Goal

Decrypt and encrypt WhatsApp **group** messages using the Signal Sender Key
protocol: `SenderKeyDistributionMessage` (SKDM) to share a sender's group key,
and `SenderKeyMessage` for the actual group ciphertexts. Validated byte-for-byte
against the same libsignal / Baileys group code WhatsApp uses.

## Protocol summary

Each sender has, per group, a *sender key*: a 32-byte chain key seed + a
Curve25519 *signing* key pair. The sender fans out an SKDM (encrypted 1:1 to each
member via the existing pkmsg/msg path) so members can decrypt that sender's
group messages. Group messages are `SenderKeyMessage`s: a symmetric chain ratchet
produces per-message AES-256-CBC keys, and the whole frame is signed with the
sender's signing private key (no per-message HMAC — the signature is the
integrity check, because a group has many readers but one writer).

## Wire formats (confirmed against `testdata/signal/group_ab.json`)

`versionByte = (3<<4)|3 = 0x33` (same as 1:1).

### SenderKeyMessage
```
0x33 || protobuf{ id=1 uint32, iteration=2 uint32, ciphertext=3 bytes } || signature[64]
```
- `signature` = XEdDSA Curve25519 signature over `(0x33 || protobuf)` with the
  signing **private** key; verified with the signing **public** key carried in
  the receiver's SenderKeyState (NOT in the message).
- The signature is **non-randomized** (libsignal calls `curveJs.sign` with no
  random arg), so it is deterministic and reproducible byte-for-byte. The
  existing `keys.Sign`/`keys.Verify` (XEdDSA, nonce `r = SHA512(a‖M)`) is
  bit-identical to curve25519-js's `crypto_sign_direct`.

### SenderKeyDistributionMessage
```
0x33 || protobuf{ id=1 uint32, iteration=2 uint32, chainKey=3 bytes(32), signingKey=4 bytes(33) }
```
- `signingKey` is the 33-byte `0x05`-prefixed signing public key. No signature.

## Key derivation

### Chain ratchet (`sender-chain-key.js`)
```
messageKeySeed = HMAC-SHA256(chainKey, 0x01)
nextChainKey   = HMAC-SHA256(chainKey, 0x02)
```

### Message keys (`sender-message-key.js`, via libsignal `deriveSecrets`)
`deriveSecrets` is libsignal's HKDF variant: salt is the HMAC key, input is the
data, info has a trailing counter byte:
```
PRK = HMAC-SHA256(salt=zeros[32], messageKeySeed)
T1  = HMAC-SHA256(PRK, "WhisperGroup" || 0x01)
T2  = HMAC-SHA256(PRK, T1 || "WhisperGroup" || 0x02)

iv        = T1[0:16]
cipherKey = T1[16:32] || T2[0:16]      // 32-byte AES-256-CBC key
```
There is no MAC key in a group message key — distinct from the 1:1
`WhisperMessageKeys` (which yields cipher‖mac‖iv).

### Iteration quirk
libsignal `GroupCipher.encrypt` uses the key for
`iteration === 0 ? 0 : iteration + 1`, so on-wire iterations are **0, 2, 4, …**
for consecutive messages (each step retains the skipped intermediate key). We
reproduce this exactly, so ciphertexts match the golden vectors.

## Go components (in `internal/signal/`)

- `senderkey.go` — `senderChainKey` (ratchet), `senderMessageKey` (iv+cipherKey),
  `SenderKeyState` (id, chain, signing pub/priv, retained skipped keys),
  `SenderKeyRecord` (≤5 states, newest last; JSON serializable). Reuses
  `pubKeyOf`, `aesCBC*`, the protobuf helpers from the 1:1 code.
- `senderkey_message.go` — parse/serialize `SenderKeyMessage` (incl. signature
  via `keys.Sign`) and `SenderKeyDistributionMessage`.
- `group_cipher.go` — `GroupCipher` with `CreateSenderKeyDistribution`,
  `ProcessSenderKeyDistribution`, `EncryptGroup`, `DecryptGroup`. Iteration
  forward/rewind logic mirrors libsignal `getSenderKey`.

Sender key id + chain seed + signing key are injected into
`CreateSenderKeyDistribution` so production uses random values while tests replay
the golden vector deterministically (no hidden randomness in the cipher).

## Validation (`group_test.go`, offline against `group_ab.json`)

- `ProcessSenderKeyDistribution` + `DecryptGroup` of alice's 3 real messages →
  plaintexts match; SKDM fields (id/iteration/chainKey/signingKey) match.
- `EncryptGroup` reproduces all 3 golden ciphertexts **byte-for-byte**, and the
  minted SKDM reproduces the golden SKDM bytes (deterministic signature).
- Iterations parse as 0/2/4 (the libsignal quirk).
- Serialize→restore the record mid-conversation, then keep decrypting.
- Negative: corrupted signature, tampered ciphertext, and a message signed by the
  wrong key are all rejected.

## libsignal functions mirrored

`GroupSessionBuilder.{create,process}`, `GroupCipher.{encrypt,decrypt,getSenderKey}`,
`SenderKeyRecord.{addSenderKeyState,setSenderKeyState,getSenderKeyState}`,
`SenderKeyState`, `SenderChainKey.{getSenderMessageKey,getNext}`,
`SenderMessageKey` (deriveSecrets layout), `SenderKeyMessage` (proto + sign/verify
via `curve.calculateSignature`/`verifySignature`), `SenderKeyDistributionMessage`,
`keyhelper.{generateSenderKey,generateSenderKeyId,generateSenderSigningKey}`.

## Harness

`harness/gen_group_vectors.mjs` drives `@whiskeysockets/baileys/lib/Signal/Group`
(which is the libsignal group impl Baileys ships): alice `create`s a sender key,
bob `process`es the SKDM, alice encrypts 3 messages, bob decrypts — all asserted
inside libsignal before dumping `testdata/signal/group_ab.json`. Same
`__WA_GO_KP_HOOK` keypair-capture style as `gen_signal_vectors.mjs`; used here as
a cross-check that the signing private read from state matches the minted one.
