/**
 * capture_pair.mjs — Sub-project #2 (Pairing) fixtures.
 *
 * Connects to real WhatsApp ONLY up to the QR. Does NOT pair, does NOT use a number.
 * Reuses the global-hook patches already injected into node_modules sources, plus a
 * new hook on generateRegistrationNode (lib/Utils/validate-connection.js).
 *
 * Writes 2 fixtures to testdata/traces/connect_pair/:
 *   client_payload.json — { variant, payloadHex, fields }  (the REGISTRATION ClientPayload)
 *   qr.json             — { qr, prefix, parts, keys }       (the emitted QR string)
 *
 * Exits after QR (or 60s timeout). NOT committed by this script.
 */

import { mkdirSync, writeFileSync } from 'fs';
import path from 'path';
import { fileURLToPath } from 'url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const TRACES_DIR = path.resolve(__dirname, '../testdata/traces/connect_pair');
mkdirSync(TRACES_DIR, { recursive: true });

// ─── ClientPayload hook ───────────────────────────────────────────────────────
let clientPayload = null;

global.__WA_GO_CLIENT_PAYLOAD_HOOK = ({ variant, node, payloadHex }) => {
  clientPayload = {
    variant,
    payloadHex,
    fields: protoToJSON(node),
  };
  console.log('[capture_pair] Captured ClientPayload (', variant, '),', payloadHex.length / 2, 'bytes');
};

// Recursively convert a protobufjs message / plain object into JSON with
// Buffers / Uint8Arrays rendered as hex, and Longs as strings.
function protoToJSON(obj) {
  if (obj === null || obj === undefined) return null;
  if (Buffer.isBuffer(obj) || obj instanceof Uint8Array) {
    return { __bytes_hex: Buffer.from(obj).toString('hex') };
  }
  // protobufjs Long
  if (typeof obj === 'object' && obj.constructor && obj.constructor.name === 'Long') {
    return { __long: obj.toString() };
  }
  if (Array.isArray(obj)) return obj.map(protoToJSON);
  if (typeof obj === 'object') {
    const out = {};
    for (const k of Object.keys(obj)) {
      // skip protobufjs internal fields
      if (k.startsWith('$') || k === 'constructor') continue;
      const v = obj[k];
      if (typeof v === 'function') continue;
      out[k] = protoToJSON(v);
    }
    return out;
  }
  return obj; // primitive
}

// ─── Start Baileys socket ─────────────────────────────────────────────────────

import makeWASocket, { useMultiFileAuthState, DisconnectReason } from '@whiskeysockets/baileys';
import pino from 'pino';

// Fresh auth dir so we get a REGISTRATION (first pairing) payload, not login.
const AUTH_DIR = path.join(__dirname, '.auth_capture_pair');
mkdirSync(AUTH_DIR, { recursive: true });

const { state, saveCreds } = await useMultiFileAuthState(AUTH_DIR);

console.log('[capture_pair] Starting Baileys socket (registration scenario)...');

const sock = makeWASocket({
  auth: state,
  browser: ['wa-go-capture', 'Chrome', '120.0.0'],
  printQRInTerminal: false,
  logger: pino({ level: 'warn' }),
  connectTimeoutMs: 30000,
  keepAliveIntervalMs: 30000,
  defaultQueryTimeoutMs: 20000,
  generateHighQualityLinkPreview: false,
  syncFullHistory: false,
  markOnlineOnConnect: false,
  fireInitQueries: false,
  shouldSyncHistoryMessage: () => false,
});

sock.ev.on('creds.update', saveCreds);

// ─── QR capture + exit ────────────────────────────────────────────────────────

let qrData = null;
let resolved = false;

const QR_PREFIX = 'https://wa.me/settings/linked_devices#';

const captureQR = (qr) => {
  // qr = prefix + ref,noiseKeyB64,identityKeyB64,advB64,platformId
  let body = qr;
  let prefix = '';
  if (qr.startsWith(QR_PREFIX)) {
    prefix = QR_PREFIX;
    body = qr.slice(QR_PREFIX.length);
  } else {
    // older baileys may emit the raw comma string with no URL prefix
    const hashIdx = qr.indexOf('#');
    if (hashIdx >= 0) {
      prefix = qr.slice(0, hashIdx + 1);
      body = qr.slice(hashIdx + 1);
    }
  }
  const parts = body.split(',');

  const keys = {};
  // Expected layout: [ref, noiseKeyB64, identityKeyB64, advSecretB64, platformId]
  if (parts.length >= 4) {
    keys.ref = parts[0];
    keys.noiseKeyPubB64 = parts[1];
    keys.signedIdentityKeyPubB64 = parts[2];
    keys.advSecretB64 = parts[3];
    if (parts.length >= 5) keys.platformId = parts[4];
    // also include raw bytes (hex) of the base64-decoded keys where it makes sense
    try { keys.noiseKeyPubHex = Buffer.from(parts[1], 'base64').toString('hex'); } catch(e) {}
    try { keys.signedIdentityKeyPubHex = Buffer.from(parts[2], 'base64').toString('hex'); } catch(e) {}
    try { keys.advSecretHex = Buffer.from(parts[3], 'base64').toString('hex'); } catch(e) {}
  }

  qrData = {
    qr,
    prefix,
    partsCount: parts.length,
    parts,
    keys,
    note: 'QR body = ref,noiseKeyPubB64,signedIdentityKeyPubB64,advSecretB64,platformId',
  };
  console.log('[capture_pair] QR captured —', parts.length, 'parts');
};

const finish = async (reason) => {
  if (resolved) return;
  resolved = true;
  console.log('[capture_pair] Finishing:', reason);

  await new Promise(r => setTimeout(r, 200));

  if (clientPayload) {
    writeFileSync(path.join(TRACES_DIR, 'client_payload.json'), JSON.stringify(clientPayload, null, 2));
    console.log('[capture_pair] Wrote client_payload.json');
  } else {
    console.warn('[capture_pair] WARNING: ClientPayload not captured!');
  }

  if (qrData) {
    writeFileSync(path.join(TRACES_DIR, 'qr.json'), JSON.stringify(qrData, null, 2));
    console.log('[capture_pair] Wrote qr.json');
  } else {
    console.warn('[capture_pair] WARNING: QR not captured!');
  }

  try { sock.end?.(undefined); } catch(e) {}
  try { sock.ws?.close?.(); } catch(e) {}

  setTimeout(() => process.exit(0), 300);
};

const timeout = setTimeout(() => finish('timeout after 60s'), 60000);

sock.ev.on('connection.update', (update) => {
  const { connection, lastDisconnect, qr } = update;
  if (qr) {
    captureQR(qr);
    // Wait a beat to make sure clientPayload (sent in ClientFinish before QR) is set
    clearTimeout(timeout);
    setTimeout(() => finish('qr received'), 1000);
  }
  if (connection === 'open') {
    clearTimeout(timeout);
    setTimeout(() => finish('connection opened (unexpected — should not pair)'), 1000);
  }
  if (connection === 'close' && !resolved) {
    clearTimeout(timeout);
    setTimeout(() => finish('connection closed: ' + (lastDisconnect?.error?.message || 'unknown')), 500);
  }
});
