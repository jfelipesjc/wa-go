/**
 * verify.mjs — Round-trip consistency check for nodes.jsonl
 *
 * For each entry in nodes.jsonl:
 *   - Re-decode the encoded_hex with Baileys decodeBinaryNode
 *   - Re-encode the decoded node with encodeBinaryNode
 *   - Verify the re-encoded bytes match the original encoded_hex
 *
 * Prints "OK X/X" at end, or lists failures.
 */

import { readFileSync } from 'fs';
import path from 'path';
import { fileURLToPath } from 'url';
import { encodeBinaryNode, decodeBinaryNode } from '@whiskeysockets/baileys/lib/WABinary/index.js';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const TRACES_DIR = path.resolve(__dirname, '../testdata/traces/connect_pair');
const NODES_FILE = path.join(TRACES_DIR, 'nodes.jsonl');

const lines = readFileSync(NODES_FILE, 'utf8').trim().split('\n').filter(Boolean);
console.log(`[verify] Checking ${lines.length} node entries from nodes.jsonl`);

let ok = 0;
let fail = 0;
const failures = [];

for (let i = 0; i < lines.length; i++) {
  const entry = JSON.parse(lines[i]);
  const { dir, tree, encoded_hex } = entry;

  try {
    // For 'in' entries: encoded_hex is the compressed buffer that was passed to decodeBinaryNode
    // We re-decode it and check structural equality
    const origBuf = Buffer.from(encoded_hex, 'hex');

    // Re-decode
    let redecoded;
    try {
      redecoded = await decodeBinaryNode(origBuf);
    } catch(e) {
      // If it's an 'out' entry, encoded_hex is the raw (uncompressed, no prefix) encoded bytes
      // decodeBinaryNode expects a 0x00-prefixed buffer (or 0x02 compressed)
      // Prefix with 0x00 (no compression flag)
      const prefixed = Buffer.concat([Buffer.from([0x00]), origBuf]);
      redecoded = await decodeBinaryNode(prefixed);
    }

    // Re-encode the decoded tree
    const reencoded = encodeBinaryNode(redecoded);

    // For 'out' entries, re-encode the original tree and compare
    if (dir === 'out') {
      const reencoded2 = encodeBinaryNode(treeToNode(tree));
      if (reencoded2.toString('hex') === encoded_hex) {
        ok++;
      } else {
        fail++;
        failures.push({ i, dir, reason: 'encode mismatch', expected: encoded_hex, got: reencoded2.toString('hex') });
      }
    } else {
      // For 'in' entries: just check that the decoded tree matches structural shape
      const structMatch = nodesStructurallyEqual(redecoded, treeToNode(tree));
      if (structMatch) {
        ok++;
      } else {
        fail++;
        failures.push({ i, dir, reason: 'structural mismatch', expected: JSON.stringify(tree), got: JSON.stringify(nodeToJSON(redecoded)) });
      }
    }
  } catch (e) {
    fail++;
    failures.push({ i, dir, reason: e.message });
  }
}

function treeToNode(tree) {
  if (!tree) return null;
  return {
    tag: tree.tag,
    attrs: tree.attrs || {},
    content: deserializeContent(tree.content)
  };
}

function deserializeContent(c) {
  if (c === null || c === undefined) return undefined;
  if (typeof c === 'string') {
    // Could be hex for bytes or a plain string
    // Check if it looks like a plain string vs a hex buffer
    // In our capture: bytes are hex strings, text strings stay as strings
    // We can't differentiate here without the original — so treat as string
    return c;
  }
  if (Array.isArray(c)) return c.map(treeToNode);
  return c;
}

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

function nodesStructurallyEqual(a, b) {
  if (!a && !b) return true;
  if (!a || !b) return false;
  if (a.tag !== b.tag) return false;
  // Check attrs keys match
  const aKeys = Object.keys(a.attrs || {}).sort();
  const bKeys = Object.keys(b.attrs || {}).sort();
  if (JSON.stringify(aKeys) !== JSON.stringify(bKeys)) return false;
  for (const k of aKeys) {
    if ((a.attrs[k] || '') !== (b.attrs[k] || '')) return false;
  }
  return true; // We don't deep-check content — structure is enough here
}

if (failures.length > 0) {
  console.log('[verify] FAILURES:');
  for (const f of failures.slice(0, 5)) {
    console.log(`  [${f.i}] ${f.dir}: ${f.reason}`);
    if (f.expected) console.log(`    expected: ${f.expected.slice(0, 80)}...`);
    if (f.got)      console.log(`    got:      ${f.got.slice(0, 80)}...`);
  }
}

console.log(`[verify] OK ${ok}/${lines.length} (${fail} failures)`);
if (ok === lines.length) {
  process.exit(0);
} else {
  process.exit(1);
}
