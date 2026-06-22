// verify_sig.mjs — validates an XEdDSA signature using Baileys' own Curve.verify,
// so the Go xeddsaSign construction can be cross-checked against the real client.
//
// Usage: node verify_sig.mjs <harnessDir> <identityPubHex(32)> <messageHex> <signatureHex(64)>
// Prints "OK" and exits 0 if valid, "FAIL" + exit 1 otherwise. ESM module
// resolution is anchored at <harnessDir>/node_modules via an absolute import.
import { pathToFileURL } from 'url';
import path from 'path';

const [, , harnessDir, pubHex, msgHex, sigHex] = process.argv;

const cryptoPath = path.join(
	harnessDir,
	'node_modules',
	'@whiskeysockets',
	'baileys',
	'lib',
	'Utils',
	'crypto.js',
);

const { Curve } = await import(pathToFileURL(cryptoPath).href);

const pub = Buffer.from(pubHex, 'hex');
const msg = Buffer.from(msgHex, 'hex');
const sig = Buffer.from(sigHex, 'hex');

// Curve.verify prepends the 0x05 type byte internally (generateSignalPubKey),
// so pass the raw 32-byte public key.
const ok = Curve.verify(pub, msg, sig);
if (ok) {
	console.log('OK');
	process.exit(0);
} else {
	console.log('FAIL');
	process.exit(1);
}
