# App-State Sync (#5): LTHash + SyncdPatch decoding

Decode the encrypted `SyncdPatch` blobs WhatsApp pushes after login
(`<notification type="server_sync">` → `<iq><sync><collection>`), surface the
mutations (contact pushName, mute, pin, star, archive, ...), and maintain a
per-collection LTHash state that is verified for integrity.

## Package layout (`internal/appstate/`)

- `lthash.go` — LTHash homomorphic hash (value expansion + add/subtract).
- `keys.go`   — `DeriveMutationKeys`: appStateSyncKey → 5 mutation keys.
- `state.go`  — `HashState`, and all MAC helpers (content/index/snapshot/patch).
- `patch.go`  — `DecodePatch`: decrypt + verify + fold one `SyncdPatch`.

Protobufs added to `internal/waproto/waproto.proto` (regenerated with `protoc`):
`KeyId`, `ExternalBlobReference`, `SyncdVersion/Index/Value/Record/Mutation/
Mutations/Patch/Snapshot`, `SyncActionData`, `SyncActionValue` (+ nested
`ContactAction`, `MuteAction`, `PinAction`, `StarAction`, `PushNameSetting`,
`ArchiveChatAction`). Upstream field numbers preserved so real traffic
round-trips and unknown actions are simply skipped.

## Crypto schemes (all verified against the WASM bridge)

### mutationKeys (`DeriveMutationKeys`)
`HKDF-SHA256(ikm = appStateSyncKey, salt = empty, info = "WhatsApp Mutation
Keys")` → 160 bytes, split into five contiguous 32-byte keys **in this order**:
1. `indexKey` — HMAC-SHA256 over the JSON index
2. `valueEncryptionKey` — AES-256-CBC key for the value blob
3. `valueMacKey` — HMAC-SHA512 key for the value MAC
4. `snapshotMacKey` — HMAC-SHA256 key for the snapshot MAC
5. `patchMacKey` — HMAC-SHA256 key for the patch MAC

### LTHash (`lthash.go`)
Each mutation's 32-byte `valueMac` is expanded:
`HKDF-SHA256(key = valueMac, salt = empty, info = "WhatsApp Patch Integrity")`
→ 128 bytes, read as **64 little-endian uint16** components. The running
128-byte state hash is the component-wise sum **mod 2^16**. A `SET` adds its
expansion; a `SET`/`REMOVE` on an index that already contributed first
subtracts the previously-folded `valueMac`. This is homomorphic and
order-independent (`subtractThenAdd(base, subBuffs, addBuffs)`), mirroring
`LTHashAntiTampering.subtractThenAdd`.

### MACs (`state.go`)
- **value MAC** (`generateContentMac`): `HMAC-SHA512(keyData || encValue ||
  last)[:32]`, where `keyData = [opByte] || keyId` and `last` is 8 bytes with
  `last[7] = len(keyData)`. **`opByte` is the raw `SyncdOperation` enum value
  (SET = 0x00, REMOVE = 0x01)** — this is what the v7 WASM bridge uses and
  differs from older Baileys JS (which used 0x01 / 0x02).
- **index MAC**: `HMAC-SHA256(indexBytes, indexKey)`.
- **snapshot MAC**: `HMAC-SHA256(ltHash || u64be(version) || name)`.
- **patch MAC**: `HMAC-SHA256(snapshotMac || concat(valueMacs) || u64be(version)
  || name)`.

### Value blob
`record.value.blob = encValue || valueMac(32)` where
`encValue = IV(16) || AES-256-CBC(PKCS7(SyncActionData))` (IV prefixed). The
decrypted `SyncActionData` carries the JSON `index` and the `SyncActionValue`.

## DecodePatch flow
1. Resolve appStateSyncKey by `keyId` (else `ErrMissingKey`); derive mutation keys.
2. Verify the **patch MAC** over all value MACs.
3. Per mutation: verify **value MAC**, AES-CBC decrypt, unmarshal
   `SyncActionData`, verify **index MAC**, parse JSON index, fold into LTHash.
4. Verify the resulting **snapshot MAC** (LTHash integrity); else `ErrSnapshotMac`.
5. Return new `HashState` (untouched on any failure) + decoded `Mutation`s.

## Golden vectors
`harness/gen_appstate_vectors.mjs` → `testdata/appstate/patch_contact_mute.json`,
produced **offline** via the public `whatsapp-rust-bridge` API (the WASM crypto
backing `@whiskeysockets/baileys` v7) plus Baileys' WAProto for protobuf
encoding. Functions used: `expandAppStateKeys`, `generateContentMac`,
`generateIndexMac`, `generateSnapshotMac`, `generatePatchMac`,
`LTHashAntiTampering.subtractThenAdd`, `hkdf` (KAT cross-check), and
`proto.SyncActionData/SyncActionValue.encode`. Deterministic IVs make the bytes
reproducible. No node_modules were patched.

## Tests (`internal/appstate/appstate_test.go`)
- `TestDeriveMutationKeys` — 5 keys match the vector.
- `TestLTHashExpandKAT` — 128-byte HKDF expansion matches.
- `TestLTHashSubtractAddConsistent` — add order-independence + subtract inverts add.
- `TestDecodePatch` — full decode → final hash, version, contact + mute actions.
- `TestDecodePatchMissingKey` — unresolved key → error.
- `TestDecodePatchTamperedValueMac` / `TestDecodePatchTamperedCiphertext` —
  integrity failures (`ErrValueMac` for ciphertext tamper).
