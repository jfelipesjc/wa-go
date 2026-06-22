/**
 * gen_qr.mjs — Fixture 2: QR string capture (LIVE, no pairing, no number).
 *
 * Connects to WhatsApp WebSocket with makeWASocket (new identity, no pre-existing session),
 * waits for the first QR code in connection.update, then:
 *   1. Writes testdata/traces/connect_pair/qr.json with the QR string, its parts,
 *      and the corresponding creds key material for validation.
 *   2. Closes the socket.
 *   3. Deletes the temporary auth directory.
 *
 * Does NOT pair any number. Safe to run repeatedly.
 * Timeout: 60 seconds.
 */

import { mkdirSync, writeFileSync, rmSync } from 'fs';
import path from 'path';
import { fileURLToPath } from 'url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const TRACES_DIR = path.resolve(__dirname, '../testdata/traces/connect_pair');
mkdirSync(TRACES_DIR, { recursive: true });

// Temporary auth directory — gitignored via harness/.auth_*/
const AUTH_DIR = path.join(__dirname, '.auth_gen_qr');
mkdirSync(AUTH_DIR, { recursive: true });

// ─── Baileys imports ──────────────────────────────────────────────────────────

import makeWASocket, { useMultiFileAuthState, DisconnectReason } from '@whiskeysockets/baileys';
import { Browsers } from '@whiskeysockets/baileys/lib/Utils/browser-utils.js';
import pino from 'pino';

// ─── Setup socket ─────────────────────────────────────────────────────────────

const { state, saveCreds } = await useMultiFileAuthState(AUTH_DIR);

console.log('[gen_qr] Starting Baileys socket (will wait for QR, no pairing)...');

const sock = makeWASocket({
  auth: state,
  browser: Browsers.ubuntu('Chrome'),   // ['Ubuntu', 'Chrome', '22.04.4']
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

// ─── Wait for QR, write fixture, exit ────────────────────────────────────────

let resolved = false;

const cleanup = () => {
  try {
    rmSync(AUTH_DIR, { recursive: true, force: true });
    console.log('[gen_qr] Removed temp auth dir:', AUTH_DIR);
  } catch (e) {
    console.warn('[gen_qr] Could not remove auth dir:', e.message);
  }
};

const finish = async (reason) => {
  if (resolved) return;
  resolved = true;
  console.log('[gen_qr] Finishing:', reason);

  // Give the socket a moment to settle before closing
  await new Promise(r => setTimeout(r, 300));
  try { sock.end(undefined); } catch {}
  await new Promise(r => setTimeout(r, 300));

  cleanup();
  setTimeout(() => process.exit(0), 200);
};

const timeout = setTimeout(() => {
  console.error('[gen_qr] TIMEOUT: no QR received within 60s');
  finish('timeout after 60s');
}, 60000);

sock.ev.on('connection.update', async (update) => {
  const { connection, lastDisconnect, qr } = update;

  if (qr) {
    clearTimeout(timeout);
    console.log('[gen_qr] QR received!');

    // ── Parse QR parts ──
    // buildPairingQRData builds:
    //   https://wa.me/settings/linked_devices#ref,noiseKeyB64,identityKeyB64,advB64,platformId
    // The raw qr string from connection.update is the part after '#' (the comma-joined fields).
    // Confirm actual format:
    const parts = qr.split(',');
    console.log('[gen_qr] QR parts count:', parts.length);
    console.log('[gen_qr] QR parts[0] (ref, first 20 chars):', parts[0]?.slice(0, 20));

    // Extract keys from creds to cross-validate
    const c = sock.authState.creds;
    const noiseKeyPub     = c.noiseKey?.public     ? Buffer.from(c.noiseKey.public).toString('base64')     : null;
    const identityKeyPub  = c.signedIdentityKey?.public ? Buffer.from(c.signedIdentityKey.public).toString('base64') : null;
    const advSecretKey    = c.advSecretKey || null;   // already base64

    // parts layout (5 parts confirmed from buildPairingQRData source):
    //   [0] ref
    //   [1] noiseKeyB64
    //   [2] identityKeyB64
    //   [3] advB64
    //   [4] platformId (companion web client type)
    const fixture = {
      _note: 'Captured live by gen_qr.mjs — QR only, no pairing',
      qr,
      parts,
      partsCount: parts.length,
      partsLayout: ['ref', 'noiseKeyB64', 'identityKeyB64', 'advB64', 'platformId'].slice(0, parts.length),
      // The values the Go side needs to verify against:
      credsKeys: {
        noiseKeyPub_base64:    noiseKeyPub,
        identityKeyPub_base64: identityKeyPub,
        advSecretKey_base64:   advSecretKey,
      },
      // Cross-check: do the QR parts match creds keys?
      validation: {
        noiseKeyMatch:    parts[1] === noiseKeyPub,
        identityKeyMatch: parts[2] === identityKeyPub,
        advKeyMatch:      parts[3] === advSecretKey,
      },
    };

    const outPath = path.join(TRACES_DIR, 'qr.json');
    writeFileSync(outPath, JSON.stringify(fixture, null, 2));
    console.log('[gen_qr] Written:', outPath);
    console.log('[gen_qr] Validation:', fixture.validation);

    await finish('qr received and fixture written');
    return;
  }

  if (connection === 'open') {
    // Should not happen (no pre-existing session), but handle gracefully
    clearTimeout(timeout);
    console.log('[gen_qr] Connection opened (unexpected — pre-authenticated session?)');
    await finish('connection opened (pre-auth)');
    return;
  }

  if (connection === 'close' && !resolved) {
    clearTimeout(timeout);
    const err = lastDisconnect?.error;
    console.error('[gen_qr] Connection closed:', err?.message || 'unknown');
    await finish('connection closed: ' + (err?.message || 'unknown'));
  }
});
