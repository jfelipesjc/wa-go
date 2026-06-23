/**
 * capture_paircode.mjs — Capture the EXACT <iq><link_code_companion_reg stage=companion_hello>
 * node that Baileys sends in requestPairingCode, so we can diff it against the Go impl.
 *
 * Gentle real connection. Generates a pairing code that is NEVER used (no phone confirms it).
 * Reuses the global encode hook already patched into node_modules WABinary/encode.js:
 *   global.__WA_GO_ENCODE_HOOK(node, encoded)
 *
 * Flow:
 *   1. Fresh temp auth dir (.auth_paircode, gitignored) -> registration creds.
 *   2. Install encode hook, scan every outgoing node for one containing link_code_companion_reg.
 *   3. After connection 'open' (or QR — requestPairingCode works once ws is up), call
 *      sock.requestPairingCode('5512991433650').
 *   4. Dump full structure of the companion_hello node to /tmp/baileys_companion_hello.json.
 *   5. Close socket, remove temp auth dir. 40s timeout.
 *
 * Run: node harness/capture_paircode.mjs
 */

import { mkdirSync, writeFileSync, rmSync } from 'fs';
import path from 'path';
import { fileURLToPath } from 'url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const OUT_PATH = '/tmp/baileys_companion_hello.json';
const AUTH_DIR = path.join(__dirname, '.auth_paircode');

const PHONE = '5512991433650'; // chip2 — code generated but NEVER used / never paired

const hex = (b) => Buffer.from(b).toString('hex');

// ─── find link_code_companion_reg anywhere in a node tree ───────────────────────
function findCompanionReg(node) {
  if (!node || typeof node !== 'object') return null;
  if (node.tag === 'link_code_companion_reg') return node;
  if (Array.isArray(node.content)) {
    for (const child of node.content) {
      const hit = findCompanionReg(child);
      if (hit) return hit;
    }
  }
  return null;
}

// describe a child node: tag, attrs, content type + hex + length
function describeChild(child) {
  const out = { tag: child.tag, attrs: child.attrs || {} };
  const c = child.content;
  if (c === null || c === undefined) {
    out.content_kind = 'empty';
    out.length_bytes = 0;
  } else if (typeof c === 'string') {
    out.content_kind = 'string';
    out.value = c;
    out.content_hex = Buffer.from(c, 'utf8').toString('hex');
    out.length_bytes = Buffer.byteLength(c, 'utf8');
  } else if (Buffer.isBuffer(c) || c instanceof Uint8Array) {
    const buf = Buffer.from(c);
    out.content_kind = 'bytes';
    out.content_hex = hex(buf);
    out.length_bytes = buf.length;
    out.first_byte_hex = buf.length ? buf[0].toString(16).padStart(2, '0') : null;
    out.has_0x05_prefix = buf.length > 0 && buf[0] === 0x05;
  } else if (Array.isArray(c)) {
    out.content_kind = 'children';
    out.children = c.map(describeChild);
  } else {
    out.content_kind = typeof c;
    out.value = String(c);
  }
  return out;
}

let captured = null;

global.__WA_GO_ENCODE_HOOK = (node, encoded) => {
  if (captured) return;
  const reg = findCompanionReg(node);
  if (!reg) return;

  // node is the iq parent (the top-level encoded node)
  const iq = node;
  captured = {
    captured_at: new Date().toISOString(),
    iq: {
      tag: iq.tag,
      attrs: iq.attrs || {},
    },
    link_code_companion_reg: {
      tag: reg.tag,
      attrs: reg.attrs || {},
    },
    children_order: Array.isArray(reg.content) ? reg.content.map((c) => c.tag) : [],
    children: Array.isArray(reg.content) ? reg.content.map(describeChild) : [],
    iq_encoded_hex: Buffer.from(encoded).toString('hex'),
  };
  console.log('[capture_paircode] *** companion_hello node captured ***');
};

// ─── Start Baileys socket ───────────────────────────────────────────────────────

import makeWASocket, { useMultiFileAuthState } from '@whiskeysockets/baileys';
import pino from 'pino';

try { rmSync(AUTH_DIR, { recursive: true, force: true }); } catch {}
mkdirSync(AUTH_DIR, { recursive: true });

const { state, saveCreds } = await useMultiFileAuthState(AUTH_DIR);

const BROWSER = ['wa-go-capture', 'Chrome', '120.0.0'];

console.log('[capture_paircode] Starting Baileys socket (gentle, pairing-code scenario)...');

const sock = makeWASocket({
  auth: state,
  browser: BROWSER,
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

let resolved = false;
let requested = false;
let returnedPairingCode = null;
let requestError = null;

function cleanup() {
  try { sock.end?.(undefined); } catch {}
  try { sock.ws?.close?.(); } catch {}
  try { rmSync(AUTH_DIR, { recursive: true, force: true }); } catch {}
}

const finish = async (reason) => {
  if (resolved) return;
  resolved = true;
  console.log('[capture_paircode] Finishing:', reason);
  await new Promise((r) => setTimeout(r, 300));

  if (captured) {
    // attach returned pairing code + ephemeral keypair if accessible
    captured.returned_pairing_code = returnedPairingCode;
    captured.creds_pairing_code = state.creds.pairingCode ?? null;
    if (state.creds.pairingEphemeralKeyPair) {
      const kp = state.creds.pairingEphemeralKeyPair;
      captured.pairing_ephemeral_key_pair = {
        public_hex: kp.public ? hex(kp.public) : null,
        public_len: kp.public ? Buffer.from(kp.public).length : null,
        private_hex: kp.private ? hex(kp.private) : null,
        private_len: kp.private ? Buffer.from(kp.private).length : null,
      };
    }
    captured.noise_key_public_hex = state.creds.noiseKey?.public ? hex(state.creds.noiseKey.public) : null;
    captured.browser = BROWSER;
    captured.finish_reason = reason;
    captured.request_error = requestError;
    writeFileSync(OUT_PATH, JSON.stringify(captured, null, 2) + '\n');
    console.log('[capture_paircode] Wrote', OUT_PATH);
  } else {
    writeFileSync(OUT_PATH, JSON.stringify({
      error: 'companion_hello NOT captured',
      finish_reason: reason,
      request_error: requestError,
      returned_pairing_code: returnedPairingCode,
    }, null, 2) + '\n');
    console.warn('[capture_paircode] WARNING: companion_hello NOT captured. Wrote error stub.');
  }

  cleanup();
  setTimeout(() => process.exit(0), 300);
};

async function doRequest() {
  if (requested) return;
  requested = true;
  try {
    console.log('[capture_paircode] Calling requestPairingCode(', PHONE, ')...');
    returnedPairingCode = await sock.requestPairingCode(PHONE);
    console.log('[capture_paircode] requestPairingCode returned:', returnedPairingCode);
  } catch (err) {
    requestError = err?.message || String(err);
    console.error('[capture_paircode] requestPairingCode ERROR:', requestError);
  }
  // give the encode hook a moment, then finish
  setTimeout(() => finish('after requestPairingCode'), 800);
}

const timeout = setTimeout(() => finish('timeout after 40s'), 40000);

sock.ev.on('connection.update', (update) => {
  const { connection, lastDisconnect, qr } = update;
  // requestPairingCode only needs the ws open; QR is emitted right after ClientFinish.
  if (qr && !requested) {
    console.log('[capture_paircode] QR emitted -> ws is up, requesting pairing code');
    doRequest();
  }
  if (connection === 'open' && !requested) {
    console.log('[capture_paircode] connection open -> requesting pairing code');
    doRequest();
  }
  if (connection === 'close' && !resolved) {
    clearTimeout(timeout);
    setTimeout(() => finish('connection closed: ' + (lastDisconnect?.error?.message || 'unknown')), 500);
  }
});
