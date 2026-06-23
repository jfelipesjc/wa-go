/**
 * gen_paircode_vectors.mjs — Golden vectors for pairing-by-code crypto. OFFLINE.
 *
 * No network, no Baileys socket. Imports the pure crypto helpers from Baileys'
 * Utils and runs them over FIXED inputs so the Go port can be verified
 * byte-for-byte:
 *   - bytesToCrockford(5 fixed bytes) -> 8-char code string
 *   - derivePairingCodeKey(code, salt) -> 32-byte key (PBKDF2-SHA256, 131072 iters)
 *   - aesEncryptCTR(ephemeralPub, key, iv) -> ciphertext
 *   - generatePairingKey blob = salt || iv || aesCTR(ephemeralPub, key, iv)
 *
 * Writes testdata/paircode/vectors.json.
 *
 * Run: node harness/gen_paircode_vectors.mjs
 */

import { mkdirSync, writeFileSync } from 'fs';
import path from 'path';
import { fileURLToPath } from 'url';

import {
  bytesToCrockford,
  derivePairingCodeKey,
  aesEncryptCTR,
} from '@whiskeysockets/baileys/lib/Utils/index.js';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const OUT_DIR = path.resolve(__dirname, '../testdata/paircode');
mkdirSync(OUT_DIR, { recursive: true });

const hex = (b) => Buffer.from(b).toString('hex');

// ─── FIXED inputs ─────────────────────────────────────────────────────────────

// 5 fixed bytes -> Crockford base32 -> exactly 8 chars (40 bits / 5 = 8).
const crockfordInput = Buffer.from([0x12, 0x34, 0x56, 0x78, 0x9a]);

// The pairing code the user types on their phone. Baileys derives this from
// bytesToCrockford(randomBytes(5)); here we pin it to a known string so the
// PBKDF2/AES vectors are reproducible. (Independent of crockfordInput above.)
const code = 'WAGOTEST';

// 32-byte salt, 16-byte IV, 32-byte ephemeral public key — all fixed.
const salt = Buffer.alloc(32);
for (let i = 0; i < salt.length; i++) salt[i] = i; // 00 01 02 ... 1f
const iv = Buffer.alloc(16);
for (let i = 0; i < iv.length; i++) iv[i] = 0xa0 + i; // a0 a1 ... af
const ephemeralPub = Buffer.alloc(32);
for (let i = 0; i < ephemeralPub.length; i++) ephemeralPub[i] = 0xff - i; // ff fe ... e0

// ─── Run the pure crypto ──────────────────────────────────────────────────────

const crockford = bytesToCrockford(crockfordInput);
if (crockford.length !== 8) {
  throw new Error(`expected 8-char crockford, got ${crockford.length}: ${crockford}`);
}

const key = await derivePairingCodeKey(code, salt); // Buffer (32)
const ciphertext = aesEncryptCTR(ephemeralPub, key, iv); // Buffer (32, CTR = no pad)

// generatePairingKey blob: salt || iv || ciphertext.
const blob = Buffer.concat([salt, iv, ciphertext]);

// ─── Roundtrip sanity: AES-CTR is symmetric, so decrypt == plaintext. ─────────
// We re-run aesEncryptCTR on the ciphertext (CTR encrypt == decrypt) and confirm
// we recover the ephemeral public key.
const roundtrip = aesEncryptCTR(ciphertext, key, iv);
if (!roundtrip.equals(ephemeralPub)) {
  throw new Error('AES-CTR roundtrip failed: did not recover ephemeral pub');
}

const vectors = {
  _comment:
    'Golden vectors for pairing-by-code crypto, generated OFFLINE by harness/gen_paircode_vectors.mjs from Baileys Utils. All byte fields are hex.',
  crockford: {
    _comment: 'bytesToCrockford(input). Alphabet "123456789ABCDEFGHJKLMNPQRSTVWXYZ" (no 0,I,L,O,U).',
    input_hex: hex(crockfordInput),
    expected: crockford,
  },
  derivePairingCodeKey: {
    _comment: 'PBKDF2-HMAC-SHA256, 131072 (2<<16) iterations, 32-byte output.',
    code,
    salt_hex: hex(salt),
    iterations: 2 << 16,
    key_hex: hex(key),
  },
  aesEncryptCTR: {
    _comment: 'AES-256-CTR(plaintext=ephemeralPub, key=derivePairingCodeKey, iv).',
    ephemeral_pub_hex: hex(ephemeralPub),
    iv_hex: hex(iv),
    ciphertext_hex: hex(ciphertext),
  },
  generatePairingKey: {
    _comment: 'salt || iv || aesEncryptCTR(ephemeralPub, derivePairingCodeKey(code,salt), iv).',
    code,
    salt_hex: hex(salt),
    iv_hex: hex(iv),
    ephemeral_pub_hex: hex(ephemeralPub),
    blob_hex: hex(blob),
  },
};

const outPath = path.join(OUT_DIR, 'vectors.json');
writeFileSync(outPath, JSON.stringify(vectors, null, 2) + '\n');
console.log(`[gen_paircode_vectors] wrote ${outPath}`);
console.log(`  crockford(${hex(crockfordInput)}) = ${crockford}`);
console.log(`  key  = ${hex(key)}`);
console.log(`  blob = ${hex(blob)} (${blob.length} bytes)`);
