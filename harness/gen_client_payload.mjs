/**
 * gen_client_payload.mjs — Fixture 1: ClientPayload (registration), OFFLINE, no network.
 *
 * Generates fresh creds via initAuthCreds(), builds the ClientPayload registration
 * protobuf via generateRegistrationNode(), and writes:
 *   testdata/traces/connect_pair/client_payload.json
 *
 * Fields:
 *   payloadHex  — wire-encoded ClientPayload as hex string
 *   fields      — decoded ClientPayload tree (Buffers → hex/base64)
 *   creds       — the generated creds (keys in base64/hex) for reproducibility
 */

import { mkdirSync, writeFileSync } from 'fs';
import path from 'path';
import { fileURLToPath } from 'url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const TRACES_DIR = path.resolve(__dirname, '../testdata/traces/connect_pair');
mkdirSync(TRACES_DIR, { recursive: true });

// ─── Imports from Baileys (no network, pure computation) ─────────────────────

import { initAuthCreds } from '@whiskeysockets/baileys/lib/Utils/auth-utils.js';
import { generateRegistrationNode } from '@whiskeysockets/baileys/lib/Utils/validate-connection.js';
import { proto } from '@whiskeysockets/baileys/WAProto/index.js';
import { Browsers } from '@whiskeysockets/baileys/lib/Utils/browser-utils.js';

// ─── Step 1: generate fresh creds ────────────────────────────────────────────

const creds = initAuthCreds();

// ─── Step 2: build minimal config matching what socket.js passes ──────────────
// From Defaults/index.js DEFAULT_CONNECTION_CONFIG + socket.js usage:
//   version, browser, countryCode, syncFullHistory, pushName

const config = {
  version: [2, 3000, 1035194821],
  browser: Browsers.ubuntu('Chrome'),     // ['Ubuntu', 'Chrome', '22.04.4']
  countryCode: 'US',
  syncFullHistory: false,
  pushName: undefined,
};

// ─── Step 3: generate ClientPayload via Baileys function ──────────────────────

const payloadObj = generateRegistrationNode(creds, config);

// ─── Step 4: encode to protobuf bytes, then decode back ──────────────────────

const bytes = proto.ClientPayload.encode(payloadObj).finish();
const decoded = proto.ClientPayload.decode(bytes);

// ─── Helpers to make Buffers/Uint8Arrays JSON-friendly ────────────────────────

function deepSerialize(val, depth = 0) {
  if (val === null || val === undefined) return val;
  if (typeof val === 'bigint') return val.toString();
  if (Buffer.isBuffer(val) || val instanceof Uint8Array) {
    return { $type: 'bytes', hex: Buffer.from(val).toString('hex'), base64: Buffer.from(val).toString('base64') };
  }
  if (Array.isArray(val)) return val.map(v => deepSerialize(v, depth + 1));
  if (typeof val === 'object') {
    const out = {};
    for (const [k, v] of Object.entries(val)) {
      // skip internal protobufjs fields
      if (k.startsWith('$') || k === 'toJSON') continue;
      out[k] = deepSerialize(v, depth + 1);
    }
    return out;
  }
  return val;
}

const fieldsRaw = decoded.toJSON ? decoded.toJSON() : Object.assign({}, decoded);
const fields = deepSerialize(fieldsRaw);

// ─── Step 5: serialize creds for reproducibility ──────────────────────────────

function serializeKeyPair(kp) {
  if (!kp) return null;
  return {
    public:  Buffer.from(kp.public).toString('base64'),
    private: Buffer.from(kp.private).toString('base64'),
  };
}

function serializeSignedPreKey(spk) {
  if (!spk) return null;
  return {
    keyId:     spk.keyId,
    keyPair:   serializeKeyPair(spk.keyPair),
    signature: Buffer.from(spk.signature).toString('base64'),
  };
}

const credsOut = {
  noiseKey:              serializeKeyPair(creds.noiseKey),
  pairingEphemeralKeyPair: serializeKeyPair(creds.pairingEphemeralKeyPair),
  signedIdentityKey:     serializeKeyPair(creds.signedIdentityKey),
  signedPreKey:          serializeSignedPreKey(creds.signedPreKey),
  registrationId:        creds.registrationId,
  advSecretKey:          creds.advSecretKey,  // already base64 from initAuthCreds
  nextPreKeyId:          creds.nextPreKeyId,
  firstUnuploadedPreKeyId: creds.firstUnuploadedPreKeyId,
  registered:            creds.registered,
};

// ─── Step 6: write fixture ────────────────────────────────────────────────────

const fixture = {
  _note: 'Generated offline by gen_client_payload.mjs — no network required',
  config: {
    version: config.version,
    browser: config.browser,
    countryCode: config.countryCode,
    syncFullHistory: config.syncFullHistory,
  },
  payloadHex: Buffer.from(bytes).toString('hex'),
  fields,
  creds: credsOut,
};

const outPath = path.join(TRACES_DIR, 'client_payload.json');
writeFileSync(outPath, JSON.stringify(fixture, null, 2));

console.log('[gen_client_payload] Written:', outPath);
console.log('[gen_client_payload] payloadHex length (bytes):', bytes.length);
console.log('[gen_client_payload] Top-level fields in ClientPayload:', Object.keys(fieldsRaw).join(', '));
console.log('[gen_client_payload] registrationId:', creds.registrationId);
console.log('[gen_client_payload] browser:', config.browser);
