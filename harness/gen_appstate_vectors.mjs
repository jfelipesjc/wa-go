// gen_appstate_vectors.mjs
//
// Generate golden App-State / LTHash vectors OFFLINE using the SAME primitives
// Baileys ships (harness/node_modules/whatsapp-rust-bridge — the WASM crypto
// bridge that backs @whiskeysockets/baileys v7) plus Baileys' own WAProto for
// protobuf encoding of SyncActionData / SyncActionValue.
//
// Nothing here touches the network or a real phone number: a fixed synthetic
// appStateSyncKey is used and every patch is built deterministically (IVs are
// fixed, not random) so the Go side can reproduce the exact bytes.
//
// === Public bridge / Baileys functions used (NO patching of node_modules) ===
//   whatsapp-rust-bridge:
//     - expandAppStateKeys(keyData)            -> ExpandedAppStateKeys
//                                                 (mutationKeys: index/valueEnc/
//                                                  valueMac/snapshot/patch)
//     - generateContentMac(op, data, keyId, k) -> valueMac (HMAC-SHA512[:32])
//     - generateIndexMac(indexBytes, key)      -> indexMac  (HMAC-SHA256)
//     - generateSnapshotMac(ltHash, ver, name, key)
//     - generatePatchMac(snapshotMac, [valueMacs], ver, name, key)
//     - LTHashAntiTampering.subtractThenAdd(base, subBuffs, addBuffs)
//     - hkdf(buffer, len, {info})              -> used to cross-check the LTHash
//                                                 value-expansion KAT
//   @whiskeysockets/baileys WAProto (proto.*):
//     - SyncActionValue / SyncActionData encode  (the cleartext payload)
//
// This mirrors `encodeSyncdPatch` in
//   node_modules/@whiskeysockets/baileys/lib/Utils/chat-utils.js
// (which itself delegates the crypto to the bridge), but with deterministic IVs
// and serialized to JSON for the Go test suite.

import { createRequire } from 'module';
import { writeFileSync, mkdirSync } from 'fs';
import { dirname, resolve } from 'path';
import { fileURLToPath } from 'url';
import { createCipheriv } from 'crypto';

const require = createRequire(import.meta.url);
const __dirname = dirname(fileURLToPath(import.meta.url));

const bridge = await import('whatsapp-rust-bridge');
const { proto } = await import('@whiskeysockets/baileys/WAProto/index.js');

const {
  expandAppStateKeys,
  generateContentMac,
  generateIndexMac,
  generateSnapshotMac,
  generatePatchMac,
  hkdf,
  LTHashAntiTampering,
} = bridge;

const OP_SET = proto.SyncdMutation.SyncdOperation.SET;     // 0
const OP_REMOVE = proto.SyncdMutation.SyncdOperation.REMOVE; // 1

const hex = (u8) => Buffer.from(u8).toString('hex');
const b64 = (u8) => Buffer.from(u8).toString('base64');

// AES-256-CBC with a *given* IV (deterministic), value blob = IV || ciphertext.
// Matches Baileys aesEncrypt() layout (random IV prefixed) but with fixed IV.
function aesEncryptWithIV(plaintext, key, iv) {
  const aes = createCipheriv('aes-256-cbc', key, iv);
  return Buffer.concat([iv, aes.update(plaintext), aes.final()]);
}

function to64BE(n) {
  const b = Buffer.alloc(8);
  b.writeUInt32BE(n, 4);
  return b;
}

// ---- fixed synthetic key material -------------------------------------------
const appStateSyncKey = Buffer.alloc(32);
for (let i = 0; i < 32; i++) appStateSyncKey[i] = (i * 7 + 3) & 0xff;

// keyId for the appStateSyncKey (arbitrary base64 id, as WA assigns)
const keyId = Buffer.from('AAAAAAE=', 'base64'); // 4 bytes 0x00000001

const keys = expandAppStateKeys(new Uint8Array(appStateSyncKey));
const mutKeys = {
  indexKey: Buffer.from(keys.indexKey),
  valueEncryptionKey: Buffer.from(keys.valueEncryptionKey),
  valueMacKey: Buffer.from(keys.valueMacKey),
  snapshotMacKey: Buffer.from(keys.snapshotMacKey),
  patchMacKey: Buffer.from(keys.patchMacKey),
};

// Build one mutation: returns the SyncdMutation pieces + bookkeeping for LT hash.
function buildMutation({ index, value, operation, iv, timestamp }) {
  const indexBuffer = Buffer.from(JSON.stringify(index));

  const sav = proto.SyncActionValue.fromObject({ timestamp, ...value });
  const dataProto = proto.SyncActionData.fromObject({
    index: indexBuffer,
    value: sav,
    padding: new Uint8Array(0),
    version: 1,
  });
  const encoded = Buffer.from(proto.SyncActionData.encode(dataProto).finish());

  const encValue = aesEncryptWithIV(encoded, mutKeys.valueEncryptionKey, iv);
  const valueMac = Buffer.from(
    generateContentMac(operation, new Uint8Array(encValue), new Uint8Array(keyId), new Uint8Array(mutKeys.valueMacKey)),
  );
  const indexMac = Buffer.from(generateIndexMac(new Uint8Array(indexBuffer), new Uint8Array(mutKeys.indexKey)));

  const valueBlob = Buffer.concat([encValue, valueMac]);

  return {
    operation,
    indexBuffer,
    indexMac,
    encValue,
    valueMac,
    valueBlob,
    encodedSyncActionData: encoded,
    expectedSyncActionValue: proto.SyncActionValue.toObject(sav, {
      longs: String,
      defaults: false,
    }),
  };
}

// Compose a full patch over an initial LT hash state (version-based).
function buildPatch({ name, version, mutations, prevHash }) {
  const lt = new LTHashAntiTampering();
  const addBuffs = mutations.filter((m) => m.operation === OP_SET).map((m) => new Uint8Array(m.valueMac));
  const subBuffs = []; // first patch, no prior values for these indices
  const newHash = Buffer.from(lt.subtractThenAdd(new Uint8Array(prevHash), subBuffs, addBuffs));

  const snapshotMac = Buffer.from(
    generateSnapshotMac(new Uint8Array(newHash), BigInt(version), name, new Uint8Array(mutKeys.snapshotMacKey)),
  );
  const valueMacs = mutations.map((m) => new Uint8Array(m.valueMac));
  const patchMac = Buffer.from(
    generatePatchMac(new Uint8Array(snapshotMac), valueMacs, BigInt(version), name, new Uint8Array(mutKeys.patchMacKey)),
  );

  // Wire-format SyncdPatch
  const patchProto = proto.SyncdPatch.fromObject({
    version: { version },
    snapshotMac,
    patchMac,
    keyId: { id: keyId },
    mutations: mutations.map((m) => ({
      operation: m.operation,
      record: {
        index: { blob: m.indexMac },
        value: { blob: m.valueBlob },
        keyId: { id: keyId },
      },
    })),
  });
  const patchBytes = Buffer.from(proto.SyncdPatch.encode(patchProto).finish());

  return { newHash, snapshotMac, patchMac, patchBytes };
}

// ---- Vector 1: a patch with a contact (pushName) SET + a mute SET ----------
const name = 'critical_unblock_low';
const version = 1;

const muteEnd = 1893456000; // fixed future ts
const contactMut = buildMutation({
  index: ['contact', '5512999999999@s.whatsapp.net'],
  value: { contactAction: { fullName: 'Alice Tester', firstName: 'Alice' } },
  operation: OP_SET,
  iv: Buffer.alloc(16, 0xa1),
  timestamp: 1700000000000,
});
const muteMut = buildMutation({
  index: ['mute', '5512888888888@s.whatsapp.net'],
  value: { muteAction: { muted: true, muteEndTimestamp: muteEnd } },
  operation: OP_SET,
  iv: Buffer.alloc(16, 0xb2),
  timestamp: 1700000001000,
});

const initialHash = Buffer.alloc(128);
const patch = buildPatch({
  name,
  version,
  mutations: [contactMut, muteMut],
  prevHash: initialHash,
});

// ---- LTHash KAT: expansion of a single valueMac to 128 bytes ----------------
const ltExpandKAT = {
  description: 'HKDF-SHA256(key=valueMac, salt=empty, info="WhatsApp Patch Integrity") -> 128 bytes, interpreted as 64 little-endian u16; LTHash add = component-wise (mod 2^16) sum.',
  valueMac: hex(contactMut.valueMac),
  expanded128: hex(hkdf(new Uint8Array(contactMut.valueMac), 128, { info: 'WhatsApp Patch Integrity' })),
};

const out = {
  note: 'Golden app-state / LTHash vectors. Generated OFFLINE via whatsapp-rust-bridge (the WASM crypto used by @whiskeysockets/baileys v7) + Baileys WAProto. See gen_appstate_vectors.mjs header for exact functions used.',
  appStateSyncKey: hex(appStateSyncKey),
  keyId: hex(keyId),
  keyIdBase64: b64(keyId),
  mutationKeys: {
    info: 'WhatsApp Mutation Keys',
    derivation: 'HKDF-SHA256(ikm=appStateSyncKey, salt=empty, info) -> 160 bytes split 5x32 in order: indexKey, valueEncryptionKey, valueMacKey, snapshotMacKey, patchMacKey',
    indexKey: hex(mutKeys.indexKey),
    valueEncryptionKey: hex(mutKeys.valueEncryptionKey),
    valueMacKey: hex(mutKeys.valueMacKey),
    snapshotMacKey: hex(mutKeys.snapshotMacKey),
    patchMacKey: hex(mutKeys.patchMacKey),
  },
  ltHashExpandKAT: ltExpandKAT,
  patch: {
    name,
    version,
    patchHex: hex(patch.patchBytes),
    snapshotMac: hex(patch.snapshotMac),
    patchMac: hex(patch.patchMac),
    finalHash: hex(patch.newHash),
    initialHash: hex(initialHash),
  },
  mutations: [
    {
      label: 'contact',
      operation: 'SET',
      index: ['contact', '5512999999999@s.whatsapp.net'],
      iv: hex(Buffer.alloc(16, 0xa1)),
      indexMac: hex(contactMut.indexMac),
      valueMac: hex(contactMut.valueMac),
      encValueHex: hex(contactMut.encValue),
      valueBlobHex: hex(contactMut.valueBlob),
      decodedSyncActionDataHex: hex(contactMut.encodedSyncActionData),
      expectedSyncActionValue: contactMut.expectedSyncActionValue,
    },
    {
      label: 'mute',
      operation: 'SET',
      index: ['mute', '5512888888888@s.whatsapp.net'],
      iv: hex(Buffer.alloc(16, 0xb2)),
      indexMac: hex(muteMut.indexMac),
      valueMac: hex(muteMut.valueMac),
      encValueHex: hex(muteMut.encValue),
      valueBlobHex: hex(muteMut.valueBlob),
      decodedSyncActionDataHex: hex(muteMut.encodedSyncActionData),
      expectedSyncActionValue: muteMut.expectedSyncActionValue,
    },
  ],
};

const outPath = resolve(__dirname, '../testdata/appstate/patch_contact_mute.json');
mkdirSync(dirname(outPath), { recursive: true });
writeFileSync(outPath, JSON.stringify(out, null, 2));
console.log('wrote', outPath);
console.log('patch bytes:', patch.patchBytes.length, 'finalHash[0:16]:', hex(patch.newHash).slice(0, 32));
