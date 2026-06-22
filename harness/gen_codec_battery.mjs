/**
 * gen_codec_battery.mjs — Synthetic hand-crafted binary-node battery for wa-go.
 *
 * OFFLINE (no network, no phone number). For each node:
 *   1. encoded = encodeBinaryNode(node)            (hex)
 *   2. re-decode encoded with decodeBinaryNode and confirm round-trip equality
 *      BEFORE writing
 *   3. write {tree, encoded_hex} to
 *      testdata/traces/codec_battery/nodes.jsonl
 *
 * The list deliberately exercises every encoder path (see CASES below).
 * Nodes that cannot be encoded/round-tripped are SKIPPED (logged), not fatal.
 */

import { createWriteStream, mkdirSync } from 'fs';
import path from 'path';
import { fileURLToPath } from 'url';
import {
  encodeBinaryNode,
  decodeBinaryNode,
} from '@whiskeysockets/baileys/lib/WABinary/index.js';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const OUT_DIR = path.resolve(__dirname, '../testdata/traces/codec_battery');
mkdirSync(OUT_DIR, { recursive: true });
const OUT_FILE = path.join(OUT_DIR, 'nodes.jsonl');
const out = createWriteStream(OUT_FILE);

// ─── Helpers ────────────────────────────────────────────────────────────────

const longString = 'A'.repeat(300) + '-long-tail';            // >256 → 20-bit length
const hexPackable = 'ABCDEF0123456789';                        // all hex chars, even length
const nibblePackable = '123456789012345';                     // 15 digits → nibble pack (odd)

// build a big child list (>256) to force LIST_16
const bigChildList = [];
for (let i = 0; i < 300; i++) {
  bigChildList.push({ tag: 'item', attrs: { i: String(i) }, content: undefined });
}

// ─── Battery of cases ─────────────────────────────────────────────────────────
// Each entry: { name, node }
const CASES = [
  // 1. Single-byte tokens for tag + attrs + values
  {
    name: '1-single-byte-tokens',
    node: { tag: 'iq', attrs: { to: 's.whatsapp.net', type: 'get' }, content: undefined },
  },
  // 2. Double-byte tokens (DICTIONARY_0..3). 'America/Sao_Paulo' is in dict 0;
  //    'WhatsApp' in dict 1; 'google' in dict 3; 'voip' in dict 1.
  {
    name: '2-double-byte-tokens',
    node: {
      tag: 'iq',
      attrs: { timezone: 'America/Sao_Paulo', platform: 'WhatsApp' },
      content: [
        { tag: 'item', attrs: { value: 'google' }, content: undefined },
        { tag: 'item', attrs: { value: 'voip' }, content: undefined },
      ],
    },
  },
  // 3. Ad-hoc short non-token string → string-with-length path
  {
    name: '3-adhoc-short-string',
    node: { tag: 'iq', attrs: { id: 'wa-go-test-123' }, content: 'wa-go-test-123' },
  },
  // 4. Long string (>256) → 20-bit length (BINARY_20)
  {
    name: '4-long-string',
    node: { tag: 'message', attrs: {}, content: longString },
  },
  // 5. Full user JID → JID packing (JID_PAIR)
  {
    name: '5-user-jid',
    node: { tag: 'iq', attrs: { to: '5512999999999@s.whatsapp.net' }, content: undefined },
  },
  // 6a. Group JID
  {
    name: '6a-group-jid',
    node: { tag: 'iq', attrs: { to: '120363000000000000@g.us' }, content: undefined },
  },
  // 6b. LID JID
  {
    name: '6b-lid-jid',
    node: { tag: 'iq', attrs: { to: '99999999999999@lid' }, content: undefined },
  },
  // 7. Number that becomes nibble packing (long digit run, odd length)
  {
    name: '7-nibble-pack',
    node: { tag: 'iq', attrs: { id: nibblePackable }, content: undefined },
  },
  // 8. Hex-packable string (A-F + digits, even length)
  {
    name: '8-hex-pack',
    node: { tag: 'iq', attrs: { hash: hexPackable }, content: undefined },
  },
  // 9. Binary content (Buffer) → bytes path
  {
    name: '9-binary-content-buffer',
    node: { tag: 'enc', attrs: { type: 'msg' }, content: Buffer.from([0, 1, 2, 3, 255, 254, 128, 64]) },
  },
  // 9b. Binary content (Uint8Array)
  {
    name: '9b-binary-content-u8',
    node: { tag: 'enc', attrs: { v: '2' }, content: new Uint8Array([10, 20, 30, 40, 50]) },
  },
  // 9c. Large binary content (>256) → BINARY_20 length for bytes
  {
    name: '9c-binary-large',
    node: { tag: 'enc', attrs: {}, content: Buffer.alloc(500, 0xab) },
  },
  // 10a. Node with null content
  {
    name: '10a-null-content',
    node: { tag: 'ping', attrs: {}, content: undefined },
  },
  // 10b. Node with string content
  {
    name: '10b-string-content',
    node: { tag: 'text', attrs: {}, content: 'hello-wa-go' },
  },
  // 10c. Node with list-of-children content
  {
    name: '10c-children-list',
    node: {
      tag: 'iq',
      attrs: { type: 'set' },
      content: [
        { tag: 'add', attrs: {}, content: undefined },
        { tag: 'remove', attrs: {}, content: undefined },
      ],
    },
  },
  // 11. Large child list (>256) → LIST_16
  {
    name: '11-big-list-list16',
    node: { tag: 'list', attrs: {}, content: bigChildList },
  },
  // 11b. Small list (<256) → LIST_8
  {
    name: '11b-small-list-list8',
    node: { tag: 'list', attrs: {}, content: [
      { tag: 'item', attrs: {}, content: undefined },
      { tag: 'item', attrs: {}, content: undefined },
      { tag: 'item', attrs: {}, content: undefined },
    ] },
  },
  // 12. Many attr pairs incl. numeric nibble-packable value + token mix
  {
    name: '12-many-attrs-mixed',
    node: {
      tag: 'iq',
      attrs: {
        to: 's.whatsapp.net',          // token value
        type: 'result',                // token value
        id: '123456789012345',         // nibble-packable value
        xmlns: 'urn:xmpp:ping',        // token value
        count: '42',                   // short nibble-packable
        name: 'wa-go-custom-attr',     // ad-hoc string value
      },
      content: undefined,
    },
  },
  // 12b. Empty-string content (special-case: writeStringRaw with length 0)
  {
    name: '12b-empty-string-content',
    node: { tag: 'val', attrs: {}, content: '' },
  },
];

// ─── Round-trip check (structural) ───────────────────────────────────────────

function normalizeForCompare(node) {
  // Convert a node tree into a canonical JSON-comparable shape.
  if (node === null || node === undefined) return null;
  if (Buffer.isBuffer(node) || node instanceof Uint8Array) {
    return { __bytes: Buffer.from(node).toString('hex') };
  }
  if (typeof node === 'string') return { __str: node };
  if (Array.isArray(node)) return node.map(normalizeForCompare);
  // it's a node
  const attrs = {};
  for (const k of Object.keys(node.attrs || {})) {
    if (node.attrs[k] !== undefined && node.attrs[k] !== null) attrs[k] = node.attrs[k];
  }
  let content;
  if (node.content === undefined || node.content === null) {
    content = null;
  } else {
    content = normalizeForCompare(node.content);
  }
  return { tag: node.tag, attrs, content };
}

function treeToJSON(node) {
  // serialize the ORIGINAL hand-crafted node for storage
  if (node === null || node === undefined) return null;
  return {
    tag: node.tag,
    attrs: node.attrs || {},
    content: serializeContent(node.content),
  };
}
function serializeContent(c) {
  if (c === null || c === undefined) return null;
  if (typeof c === 'string') return c;
  if (Buffer.isBuffer(c) || c instanceof Uint8Array) return Buffer.from(c).toString('hex');
  if (Array.isArray(c)) return c.map(treeToJSON);
  return String(c);
}

// ─── Run battery ──────────────────────────────────────────────────────────────

let written = 0;
let rtOk = 0;
const skipped = [];

for (const { name, node } of CASES) {
  let encoded;
  try {
    encoded = encodeBinaryNode(node);
  } catch (e) {
    console.warn(`[battery] SKIP ${name}: encode failed: ${e.message}`);
    skipped.push({ name, stage: 'encode', reason: e.message });
    continue;
  }

  // decodeBinaryNode expects a buffer that may have a compression-flag prefix.
  // encodeBinaryNode returns raw node bytes (it starts the buffer with [0]
  // which acts as the uncompressed 0x00 prefix), so we can feed it directly.
  let decoded;
  try {
    decoded = await decodeBinaryNode(encoded);
  } catch (e) {
    console.warn(`[battery] SKIP ${name}: decode failed: ${e.message}`);
    skipped.push({ name, stage: 'decode', reason: e.message });
    continue;
  }

  // Round-trip: re-encode the decoded node and require identical bytes.
  let reencoded;
  try {
    reencoded = encodeBinaryNode(decoded);
  } catch (e) {
    console.warn(`[battery] SKIP ${name}: re-encode failed: ${e.message}`);
    skipped.push({ name, stage: 're-encode', reason: e.message });
    continue;
  }

  const bytesMatch = reencoded.toString('hex') === encoded.toString('hex');
  if (!bytesMatch) {
    console.warn(`[battery] SKIP ${name}: round-trip byte mismatch`);
    console.warn(`    orig: ${encoded.toString('hex').slice(0, 80)}`);
    console.warn(`    re:   ${reencoded.toString('hex').slice(0, 80)}`);
    skipped.push({ name, stage: 'roundtrip', reason: 'byte mismatch' });
    continue;
  }

  rtOk++;
  out.write(JSON.stringify({
    name,
    tree: treeToJSON(node),
    decoded_tree: treeToJSON(decoded),
    encoded_hex: encoded.toString('hex'),
  }) + '\n');
  written++;
  console.log(`[battery] OK   ${name}  (${encoded.length} bytes)`);
}

await new Promise((r) => out.end(r));

console.log('');
console.log(`[battery] Total nodes generated: ${written}`);
console.log(`[battery] round-trip OK ${rtOk}/${CASES.length}`);
if (skipped.length) {
  console.log(`[battery] Skipped ${skipped.length}:`);
  for (const s of skipped) console.log(`    - ${s.name} (${s.stage}): ${s.reason}`);
}
console.log(`[battery] Wrote ${OUT_FILE}`);
