/**
 * capture.mjs — Golden trace capture for wa-go project
 *
 * Uses global hooks installed in patched Baileys source files (node_modules).
 * Patches applied (documented in harness/README.md):
 *   - lib/WABinary/encode.js: calls global.__WA_GO_ENCODE_HOOK(node, encoded)
 *   - lib/WABinary/decode.js: calls global.__WA_GO_DECODE_HOOK(rawBuf, node)
 *   - lib/Utils/noise-handler.js: calls global.__WA_GO_NOISE_KEYPAIR_HOOK,
 *       __WA_GO_SERVER_HELLO_HOOK, __WA_GO_ENCODE_FRAME_HOOK, __WA_GO_DECODE_FRAME_HOOK
 *
 * Writes to testdata/traces/connect_pair/:
 *   frames_raw.jsonl  — raw WebSocket frames {dir,t,hex}
 *   nodes.jsonl       — binary nodes {dir,t,tree,encoded_hex}
 *   noise.json        — Noise handshake material
 *   manifest.json     — scenario metadata
 *
 * Exits after QR is generated (or 60s timeout). Does NOT pair any number.
 */

import { createWriteStream, mkdirSync, writeFileSync } from 'fs';
import path from 'path';
import { fileURLToPath } from 'url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const TRACES_DIR = path.resolve(__dirname, '../testdata/traces/connect_pair');
mkdirSync(TRACES_DIR, { recursive: true });

const framesStream = createWriteStream(path.join(TRACES_DIR, 'frames_raw.jsonl'));
const nodesStream  = createWriteStream(path.join(TRACES_DIR, 'nodes.jsonl'));

const startTime = Date.now();
const writeJsonl = (stream, obj) => stream.write(JSON.stringify(obj) + '\n');

// ─── Noise material ────────────────────────────────────────────────────────
import { NOISE_MODE, NOISE_WA_HEADER } from '@whiskeysockets/baileys/lib/Defaults/index.js';

const noiseMaterial = {
  noiseMode:   NOISE_MODE,
  noiseHeader: NOISE_WA_HEADER.toString('hex'),   // 57410603 = "WA" + version [6,3]
  // Ephemeral key pair generated fresh per connection (what Baileys calls keyPair in socket.js)
  ephemeralKeyPriv: null,
  ephemeralKeyPub:  null,
  // ServerHello fields (encrypted)
  serverEphemeral:  null,
  serverStaticEnc:  null,
  serverPayloadEnc: null,
  // Static auth key pair (noiseKey in processHandshake — the long-term identity key)
  authStaticKeyPriv: null,
  authStaticKeyPub:  null,
  // Frame snapshots for first handshake messages
  clientHelloFrameHex:  null,   // first outgoing frame (intro header + ClientHello)
  serverHelloFrameHex:  null,   // first incoming raw data chunk
  clientFinishFrameHex: null,   // second outgoing frame (ClientFinish)
  transportKeysNote: 'Transport keys derived inside TransportState closure; not directly accessible without deeper patching.',
};

// ─── Global hooks (installed before importing Baileys) ───────────────────────

let outFrameIdx = 0;
let inFrameIdx  = 0;

global.__WA_GO_ENCODE_HOOK = (node, encoded) => {
  writeJsonl(nodesStream, {
    dir: 'out',
    t: Date.now() - startTime,
    tree: nodeToJSON(node),
    encoded_hex: encoded.toString('hex')
  });
};

global.__WA_GO_DECODE_HOOK = (rawBuf, node) => {
  writeJsonl(nodesStream, {
    dir: 'in',
    t: Date.now() - startTime,
    tree: nodeToJSON(node),
    // rawBuf: the 0x00/0x02-prefixed buffer that was passed to decodeBinaryNode
    encoded_hex: Buffer.from(rawBuf).toString('hex')
  });
};

global.__WA_GO_NOISE_KEYPAIR_HOOK = ({ privateKey, publicKey, NOISE_HEADER }) => {
  noiseMaterial.ephemeralKeyPriv = Buffer.from(privateKey).toString('hex');
  noiseMaterial.ephemeralKeyPub  = Buffer.from(publicKey).toString('hex');
};

global.__WA_GO_SERVER_HELLO_HOOK = ({ serverHello, noiseKey }) => {
  noiseMaterial.serverEphemeral  = Buffer.from(serverHello.ephemeral).toString('hex');
  noiseMaterial.serverStaticEnc  = Buffer.from(serverHello.static).toString('hex');
  noiseMaterial.serverPayloadEnc = Buffer.from(serverHello.payload).toString('hex');
  if (noiseKey) {
    noiseMaterial.authStaticKeyPriv = Buffer.from(noiseKey.private).toString('hex');
    noiseMaterial.authStaticKeyPub  = Buffer.from(noiseKey.public).toString('hex');
  }
};

global.__WA_GO_ENCODE_FRAME_HOOK = (frame) => {
  const hex = Buffer.from(frame).toString('hex');
  const t   = Date.now() - startTime;
  if (outFrameIdx === 0) noiseMaterial.clientHelloFrameHex  = hex;
  if (outFrameIdx === 1) noiseMaterial.clientFinishFrameHex = hex;
  outFrameIdx++;
  writeJsonl(framesStream, { dir: 'out', t, hex });
};

global.__WA_GO_DECODE_FRAME_HOOK = (data) => {
  const hex = Buffer.from(data).toString('hex');
  const t   = Date.now() - startTime;
  if (inFrameIdx === 0) noiseMaterial.serverHelloFrameHex = hex;
  inFrameIdx++;
  writeJsonl(framesStream, { dir: 'in', t, hex });
};

// ─── Helpers ─────────────────────────────────────────────────────────────────

function nodeToJSON(node) {
  if (!node) return null;
  return {
    tag: node.tag,
    attrs: node.attrs || {},
    content: serializeContent(node.content)
  };
}

function serializeContent(c) {
  if (c === null || c === undefined) return null;
  if (typeof c === 'string') return c;
  if (Buffer.isBuffer(c) || c instanceof Uint8Array) return Buffer.from(c).toString('hex');
  if (Array.isArray(c)) return c.map(nodeToJSON);
  return String(c);
}

// ─── Start Baileys socket ─────────────────────────────────────────────────────

import makeWASocket, { useMultiFileAuthState, DisconnectReason } from '@whiskeysockets/baileys';
import pino from 'pino';

const AUTH_DIR = path.join(__dirname, '.auth_capture');
mkdirSync(AUTH_DIR, { recursive: true });

const { state, saveCreds } = await useMultiFileAuthState(AUTH_DIR);

console.log('[capture] Starting Baileys socket (hooks installed via global.__WA_GO_*)...');

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

// ─── Wait for QR or connection, then exit ────────────────────────────────────

let resolved = false;

const finish = async (reason) => {
  if (resolved) return;
  resolved = true;
  console.log('[capture] Finishing:', reason);

  await new Promise(r => setTimeout(r, 300));
  framesStream.end();
  nodesStream.end();
  await new Promise(r => setTimeout(r, 200));

  writeFileSync(
    path.join(TRACES_DIR, 'noise.json'),
    JSON.stringify(noiseMaterial, null, 2)
  );

  writeFileSync(
    path.join(TRACES_DIR, 'manifest.json'),
    JSON.stringify({
      scenario: 'connect_pair',
      baileysPackage: '@whiskeysockets/baileys',
      baileysVersion: '6.7.18',
      waVersion: [2, 3000, 1035194821],
      waHeader: NOISE_WA_HEADER.toString('hex'),
      noiseMode: NOISE_MODE,
      capturedAt: new Date().toISOString(),
      patchStrategy: 'global hooks injected into node_modules source files',
      patchedFiles: [
        'node_modules/@whiskeysockets/baileys/lib/WABinary/encode.js',
        'node_modules/@whiskeysockets/baileys/lib/WABinary/decode.js',
        'node_modules/@whiskeysockets/baileys/lib/Utils/noise-handler.js',
      ],
      notes: [
        'connect_pair: run until QR generated, no pairing done',
        'frames_raw.jsonl: raw bytes from encodeFrame/decodeFrame (outgoing=full WS message, incoming=raw WS chunks)',
        'nodes.jsonl out: encoded_hex is the raw binary node bytes (no prefix); in: encoded_hex is 0x00/0x02-prefixed compressed buffer',
        reason
      ]
    }, null, 2)
  );

  console.log('[capture] Done. Files:', TRACES_DIR);
  console.log('[capture] frames_raw lines:', outFrameIdx + inFrameIdx);
  setTimeout(() => process.exit(0), 300);
};

const timeout = setTimeout(() => finish('timeout after 60s'), 60000);

sock.ev.on('connection.update', (update) => {
  const { connection, lastDisconnect, qr } = update;
  if (qr) {
    console.log('[capture] QR received — capture complete');
    clearTimeout(timeout);
    setTimeout(() => finish('qr received'), 1000);
  }
  if (connection === 'open') {
    console.log('[capture] Connection opened (pre-authenticated session)');
    clearTimeout(timeout);
    setTimeout(() => finish('connection opened'), 2000);
  }
  if (connection === 'close' && !resolved) {
    clearTimeout(timeout);
    setTimeout(() => finish('connection closed: ' + (lastDisconnect?.error?.message || 'unknown')), 500);
  }
});
