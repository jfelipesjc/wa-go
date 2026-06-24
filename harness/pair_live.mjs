/**
 * pair_live.mjs — Real Baileys pairing-by-code, kept alive until paired.
 * Diagnostic: if Baileys pairs by code on this exact phone/number where wa-go
 * fails, the bug is in wa-go. If Baileys ALSO fails, it's environmental.
 *
 * Usage: node harness/pair_live.mjs <phoneNumber>
 * Prints the pairing code, then waits up to 180s for connection 'open'.
 */
import { mkdirSync, rmSync } from 'fs';
import path from 'path';
import { fileURLToPath } from 'url';
import makeWASocket, { useMultiFileAuthState } from '@whiskeysockets/baileys';
import pino from 'pino';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const PHONE = process.argv[2] || '5512991272281';
const AUTH_DIR = path.join(__dirname, '.auth_pairlive');

try { rmSync(AUTH_DIR, { recursive: true, force: true }); } catch {}
mkdirSync(AUTH_DIR, { recursive: true });

const { state, saveCreds } = await useMultiFileAuthState(AUTH_DIR);
const BROWSER = ['wa-go-diag', 'Chrome', '120.0.0'];

const sock = makeWASocket({
  auth: state,
  browser: BROWSER,
  printQRInTerminal: false,
  logger: pino({ level: 'warn' }),
  connectTimeoutMs: 30000,
  keepAliveIntervalMs: 25000,
  defaultQueryTimeoutMs: 20000,
  markOnlineOnConnect: false,
  fireInitQueries: false,
  syncFullHistory: false,
  shouldSyncHistoryMessage: () => false,
});

sock.ev.on('creds.update', saveCreds);

let requested = false;
async function doRequest() {
  if (requested) return;
  requested = true;
  try {
    const code = await sock.requestPairingCode(PHONE);
    console.log('PAIRING_CODE=' + code);
  } catch (e) {
    console.error('REQUEST_ERROR=' + (e?.message || String(e)));
  }
}

sock.ev.on('connection.update', (u) => {
  const { connection, lastDisconnect, qr, isNewLogin } = u;
  if (qr || connection === 'connecting') doRequest();
  if (connection) console.log('CONN=' + connection + (isNewLogin ? ' NEW_LOGIN' : ''));
  if (connection === 'open') {
    console.log('*** PAIRED OK *** registered=' + JSON.stringify(state.creds.registered) +
      ' me=' + JSON.stringify(state.creds.me));
    setTimeout(() => process.exit(0), 1000);
  }
  if (connection === 'close') {
    const code = lastDisconnect?.error?.output?.statusCode;
    console.log('CLOSE statusCode=' + code + ' msg=' + (lastDisconnect?.error?.message || ''));
  }
});

// give it a beat to connect, then ensure the request fired
setTimeout(doRequest, 4000);
setTimeout(() => { console.log('TIMEOUT 180s'); process.exit(0); }, 180000);
