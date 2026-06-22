// gen_media_vectors.mjs
//
// Generate golden WhatsApp media-encryption vectors using Baileys' PUBLIC API
// (getMediaKeys / hkdf / hkdfInfoKey from @whiskeysockets/baileys) plus node's
// built-in crypto for the AES-256-CBC + HMAC steps — which is byte-for-byte what
// Baileys' encryptedStream() does internally
// (harness/node_modules/@whiskeysockets/baileys/lib/Utils/messages-media.js):
//
//   mediaKey (32 random bytes) --HKDF-SHA256(112, info="WhatsApp <Type> Keys")-->
//     iv(16) || cipherKey(32) || macKey(32) || refKey(32)
//   enc = AES-256-CBC(cipherKey, iv, PKCS7(plaintext))
//   mac = HMAC-SHA256(macKey, iv || enc)[:10]
//   blob = enc || mac
//   fileSha256    = SHA256(plaintext)
//   fileEncSha256 = SHA256(blob)            (== SHA256(enc || mac))
//
// We also confirm a full roundtrip: re-deriving the keys and decrypting the blob
// reproduces the original plaintext. The mediaKey is FIXED (not random) so the
// Go side can reproduce the ciphertext byte-for-byte deterministically.
//
// Offline only: no network, no phone. Synthetic test data.

import { createRequire } from 'module';
import { writeFileSync, mkdirSync } from 'fs';
import { dirname, resolve } from 'path';
import { fileURLToPath } from 'url';
import * as Crypto from 'crypto';

const require = createRequire(import.meta.url);
const baileys = require('@whiskeysockets/baileys');
const { getMediaKeys, hkdf, hkdfInfoKey } = baileys;

const __dirname = dirname(fileURLToPath(import.meta.url));
const OUT = resolve(__dirname, '..', 'testdata', 'media', 'media_vectors.json');

// Fixed 32-byte mediaKey: 0x00,0x01,...,0x1f. Deterministic on purpose.
const mediaKey = Buffer.from(
  Array.from({ length: 32 }, (_, i) => i)
);

// Two plaintexts: a small known string and a buffer > 64 KiB (multi-block,
// spans more than one AES chunk and exercises non-trivial PKCS7 padding).
const small = Buffer.from('conteudo de teste de midia wa-go', 'utf8');

// Deterministic large buffer (70000 bytes) via a simple PRNG so it is
// reproducible and not all-zeros (which would hide padding/IV bugs).
function lcgBuffer(n, seed) {
  const out = Buffer.alloc(n);
  let s = seed >>> 0;
  for (let i = 0; i < n; i++) {
    s = (Math.imul(s, 1664525) + 1013904223) >>> 0;
    out[i] = (s >>> 24) & 0xff;
  }
  return out;
}
const large = lcgBuffer(70000, 0xC0FFEE);

const plaintexts = {
  small: { label: 'small_string', buf: small },
  large: { label: 'large_70000', buf: large },
};

// Baileys MEDIA_HKDF_KEY_MAPPING: image->Image, audio->Audio, document->Document,
// video->Video. info string = `WhatsApp <X> Keys`. We cover the four core types
// requested (image/audio/document) plus video for good measure.
const mediaTypes = ['image', 'audio', 'document', 'video'];

async function encryptOne(plaintext, mediaType) {
  // Public-API derivation (iv/cipher/mac). Wrap in Buffer: the rust-bridge
  // returns plain Uint8Arrays whose .toString('hex') would not hex-encode.
  const km = await getMediaKeys(mediaKey, mediaType);
  const iv = Buffer.from(km.iv);
  const cipherKey = Buffer.from(km.cipherKey);
  const macKey = Buffer.from(km.macKey);
  // Full 112-byte expansion to also capture refKey (last 32 bytes) and prove
  // the layout. Same hkdf Baileys uses internally.
  const expanded = hkdf(mediaKey, 112, { info: hkdfInfoKey(mediaType) });
  const refKey = Buffer.from(expanded).slice(80, 112);

  const aes = Crypto.createCipheriv('aes-256-cbc', cipherKey, iv);
  const enc = Buffer.concat([aes.update(plaintext), aes.final()]);

  const hmac = Crypto.createHmac('sha256', macKey).update(iv).update(enc);
  const mac = hmac.digest().slice(0, 10);

  const blob = Buffer.concat([enc, mac]);
  const fileSha256 = Crypto.createHash('sha256').update(plaintext).digest();
  const fileEncSha256 = Crypto.createHash('sha256').update(blob).digest();

  // Roundtrip: re-derive + decrypt the blob -> must equal plaintext, and the
  // mac must verify.
  const ct = blob.slice(0, blob.length - 10);
  const gotMac = blob.slice(blob.length - 10);
  const expectMac = Crypto.createHmac('sha256', macKey)
    .update(iv).update(ct).digest().slice(0, 10);
  if (!gotMac.equals(expectMac)) {
    throw new Error(`mac mismatch on roundtrip for ${mediaType}`);
  }
  const dec = Crypto.createDecipheriv('aes-256-cbc', cipherKey, iv);
  const back = Buffer.concat([dec.update(ct), dec.final()]);
  if (!back.equals(plaintext)) {
    throw new Error(`roundtrip plaintext mismatch for ${mediaType}`);
  }
  // Cross-check: hkdf expansion matches getMediaKeys slices.
  const ivExp = Buffer.from(expanded).slice(0, 16);
  const cipherExp = Buffer.from(expanded).slice(16, 48);
  const macExp = Buffer.from(expanded).slice(48, 80);
  if (!ivExp.equals(iv) || !cipherExp.equals(cipherKey) || !macExp.equals(macKey)) {
    throw new Error(`hkdf layout mismatch vs getMediaKeys for ${mediaType}`);
  }

  return {
    iv: iv.toString('hex'),
    cipherKey: cipherKey.toString('hex'),
    macKey: macKey.toString('hex'),
    refKey: refKey.toString('hex'),
    info: hkdfInfoKey(mediaType),
    ciphertextHex: blob.toString('hex'), // ct || mac
    macHex: mac.toString('hex'),
    fileSha256: fileSha256.toString('hex'),
    fileEncSha256: fileEncSha256.toString('hex'),
  };
}

const vectors = {
  _comment:
    'WhatsApp media encryption golden vectors. mediaKey fixed (0x00..0x1f). ' +
    'ciphertextHex = AES-256-CBC(cipherKey,iv,PKCS7(plaintext)) || HMAC-SHA256(macKey, iv||ct)[:10]. ' +
    'Generated by harness/gen_media_vectors.mjs via Baileys getMediaKeys/hkdf.',
  mediaKeyHex: mediaKey.toString('hex'),
  plaintexts: {},
  cases: [],
};

for (const [pk, p] of Object.entries(plaintexts)) {
  vectors.plaintexts[pk] = {
    label: p.label,
    len: p.buf.length,
    plaintextHex: p.buf.toString('hex'),
  };
  for (const mt of mediaTypes) {
    vectors.cases.push({
      plaintext: pk,
      mediaType: mt,
      ...(await encryptOne(p.buf, mt)),
    });
  }
}

mkdirSync(dirname(OUT), { recursive: true });
writeFileSync(OUT, JSON.stringify(vectors, null, 2));
console.log(
  `wrote ${vectors.cases.length} cases (${mediaTypes.length} types x ` +
  `${Object.keys(plaintexts).length} plaintexts) -> ${OUT}`
);
