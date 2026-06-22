// gen_signal_vectors.mjs
//
// Generate golden Signal session vectors using the SAME libsignal that Baileys
// uses (harness/node_modules/libsignal, v6.0.0 = WhiskeySockets/libsignal-node).
//
// Builds two identities (alice = initiator, bob = responder), establishes an
// X3DH session, and exchanges 4 messages exercising the symmetric chain and the
// DH ratchet in both directions. Dumps everything to
// testdata/signal/session_ab.json so the Go implementation can validate by
// decrypting / reproducing the real ciphertexts.
//
// Offline only: no network, no real phone number. Keys are synthetic test keys.

import { createRequire } from 'module';
import { writeFileSync, mkdirSync } from 'fs';
import { dirname, resolve } from 'path';
import { fileURLToPath } from 'url';

const require = createRequire(import.meta.url);
const libsignal = require('libsignal');
const curve = require('libsignal/src/curve');
const keyhelper = require('libsignal/src/keyhelper');

const { SessionBuilder, SessionCipher, ProtocolAddress } = libsignal;

const __dirname = dirname(fileURLToPath(import.meta.url));
const OUT = resolve(__dirname, '..', 'testdata', 'signal', 'session_ab.json');

const hex = (b) => Buffer.from(b).toString('hex');

// -------- In-memory SignalProtocolStore --------
// Mirrors the storage interface libsignal expects (see baileys signalStorage()):
//   loadSession, storeSession, isTrustedIdentity, loadIdentityKey, saveIdentity,
//   loadPreKey, removePreKey, loadSignedPreKey, getOurRegistrationId, getOurIdentity
function makeStore(identity, registrationId) {
    // identity: { pubKey(33b,0x05), privKey(32b) }
    const sessions = new Map();       // addrString -> serialized session (object)
    const preKeys = new Map();        // id -> { privKey, pubKey }
    const signedPreKeys = new Map();  // id -> { privKey, pubKey }
    const identities = new Map();     // addrString -> pubKey Buffer

    return {
        // expose maps for setup
        _preKeys: preKeys,
        _signedPreKeys: signedPreKeys,
        _sessions: sessions,

        getOurIdentity: () => ({ pubKey: identity.pubKey, privKey: identity.privKey }),
        getOurRegistrationId: () => registrationId,

        isTrustedIdentity: () => true, // TOFU, like WhatsApp Web
        loadIdentityKey: (id) => identities.get(id),
        saveIdentity: (id, key) => {
            const existing = identities.get(id);
            identities.set(id, Buffer.from(key));
            return !existing || !existing.equals(Buffer.from(key));
        },

        loadSession: async (id) => {
            const data = sessions.get(id);
            if (!data) return undefined;
            return libsignal.SessionRecord.deserialize(data);
        },
        storeSession: async (id, record) => {
            sessions.set(id, record.serialize());
        },

        loadPreKey: async (id) => {
            const k = preKeys.get(id.toString ? id.toString() : id) || preKeys.get(Number(id));
            if (!k) return undefined;
            return { privKey: Buffer.from(k.privKey), pubKey: Buffer.from(k.pubKey) };
        },
        removePreKey: async (id) => { preKeys.delete(Number(id)); },

        loadSignedPreKey: async (id) => {
            // Baileys ignores id and returns creds.signedPreKey; we honor id too.
            const k = signedPreKeys.get(Number(id)) || [...signedPreKeys.values()][0];
            if (!k) return undefined;
            return { privKey: Buffer.from(k.privKey), pubKey: Buffer.from(k.pubKey) };
        },
    };
}

async function main() {
    // ----- bob (responder): identity, regId, signedPreKey, one-time preKey -----
    const bobIdentity = curve.generateKeyPair();               // {pubKey:33b, privKey:32b}
    const bobRegId = keyhelper.generateRegistrationId();
    const bobSignedPreKeyId = 1;
    const bobSignedPreKey = keyhelper.generateSignedPreKey(
        { pubKey: bobIdentity.pubKey, privKey: bobIdentity.privKey },
        bobSignedPreKeyId
    ); // { keyId, keyPair:{pubKey,privKey}, signature(64b) }
    const bobPreKeyId = 31337;
    const bobPreKey = keyhelper.generatePreKey(bobPreKeyId); // { keyId, keyPair }

    const bobStore = makeStore(
        { pubKey: bobIdentity.pubKey, privKey: bobIdentity.privKey },
        bobRegId
    );
    bobStore._preKeys.set(bobPreKeyId, {
        privKey: bobPreKey.keyPair.privKey, pubKey: bobPreKey.keyPair.pubKey
    });
    bobStore._signedPreKeys.set(bobSignedPreKeyId, {
        privKey: bobSignedPreKey.keyPair.privKey, pubKey: bobSignedPreKey.keyPair.pubKey
    });

    // ----- alice (initiator): identity, regId -----
    const aliceIdentity = curve.generateKeyPair();
    const aliceRegId = keyhelper.generateRegistrationId();
    const aliceStore = makeStore(
        { pubKey: aliceIdentity.pubKey, privKey: aliceIdentity.privKey },
        aliceRegId
    );

    // Addresses
    const bobAddr = new ProtocolAddress('bob', 1);
    const aliceAddr = new ProtocolAddress('alice', 1);

    // ----- alice builds session from bob's bundle (X3DH initiator) -----
    // libsignal v6 public entry: SessionBuilder.initOutgoing(device)
    const bundle = {
        identityKey: bobIdentity.pubKey,
        registrationId: bobRegId,
        signedPreKey: {
            keyId: bobSignedPreKey.keyId,
            publicKey: bobSignedPreKey.keyPair.pubKey,
            signature: bobSignedPreKey.signature,
        },
        preKey: {
            keyId: bobPreKey.keyId,
            publicKey: bobPreKey.keyPair.pubKey,
        },
    };
    await new SessionBuilder(aliceStore, bobAddr).initOutgoing(bundle);

    // Capture alice's base/ephemeral key used for X3DH from the stored session.
    const aliceSessionRec = await aliceStore.loadSession(bobAddr.toString());
    const aliceOpen = aliceSessionRec.getOpenSession();
    const aliceBaseKeyPub = aliceOpen.pendingPreKey.baseKey; // 33b (0x05 + pub)

    // libsignal keeps the initiator's `pendingPreKey` set (so alice keeps
    // sending type-3 pkmsg) until alice *receives* a reply from bob, which
    // clears it (`delete session.pendingPreKey` in doDecryptWhisperMessage).
    // So the realistic ordering that exercises both message types + DH ratchet
    // in both directions is:
    //   msg1 a->b pkmsg  (X3DH establish)
    //   msg2 b->a msg    (bob replies -> DH ratchet, alice clears pendingPreKey)
    //   msg3 a->b msg    (now a normal WhisperMessage / type 1)
    //   msg4 a->b msg    (same chain, counter++)
    const aliceCipher = () => new SessionCipher(aliceStore, bobAddr);
    const bobCipher = () => new SessionCipher(bobStore, aliceAddr);

    const exchanges = [];
    let n = 0;

    const recordExchange = (dir, ct, pt) => {
        exchanges.push({
            n: ++n, dir,
            type: ct.type === 3 ? 'pkmsg' : 'msg',
            sigType: ct.type,
            ciphertextHex: hex(ct.body),
            plaintextHex: hex(pt),
            plaintext: pt.toString('utf8'),
        });
    };

    // ----- msg1: alice -> bob (PreKeySignalMessage / type 3 / pkmsg) -----
    const msg1pt = Buffer.from('ola bob 1', 'utf8');
    const ct1 = await aliceCipher().encrypt(msg1pt);
    const dec1 = await bobCipher().decryptPreKeyWhisperMessage(Buffer.from(ct1.body));
    assertEq(dec1, msg1pt, 'msg1');
    if (ct1.type !== 3) throw new Error(`msg1 expected type 3, got ${ct1.type}`);
    recordExchange('a->b', ct1, msg1pt);

    // ----- msg2: bob -> alice (DH ratchet reverse; clears alice pendingPreKey) -----
    const msg2pt = Buffer.from('oi alice 2', 'utf8');
    const ct2 = await bobCipher().encrypt(msg2pt);
    const dec2 = await aliceCipher().decryptWhisperMessage(Buffer.from(ct2.body));
    assertEq(dec2, msg2pt, 'msg2');
    if (ct2.type !== 1) throw new Error(`msg2 expected type 1, got ${ct2.type}`);
    recordExchange('b->a', ct2, msg2pt);

    // ----- msg3: alice -> bob (now WhisperMessage / type 1 / msg) -----
    const msg3pt = Buffer.from('ola bob 3', 'utf8');
    const ct3 = await aliceCipher().encrypt(msg3pt);
    const dec3 = await bobCipher().decryptWhisperMessage(Buffer.from(ct3.body));
    assertEq(dec3, msg3pt, 'msg3');
    if (ct3.type !== 1) throw new Error(`msg3 expected type 1, got ${ct3.type}`);
    recordExchange('a->b', ct3, msg3pt);

    // ----- msg4: alice -> bob (same sending chain, counter advances) -----
    const msg4pt = Buffer.from('ola bob 4', 'utf8');
    const ct4 = await aliceCipher().encrypt(msg4pt);
    const dec4 = await bobCipher().decryptWhisperMessage(Buffer.from(ct4.body));
    assertEq(dec4, msg4pt, 'msg4');
    if (ct4.type !== 1) throw new Error(`msg4 expected type 1, got ${ct4.type}`);
    recordExchange('a->b', ct4, msg4pt);

    // ----- dump -----
    const out = {
        _meta: {
            lib: 'libsignal',
            libVersion: require('libsignal/package.json').version,
            note: 'Golden Signal vectors from Baileys libsignal (WhiskeySockets/libsignal-node v6).',
            pubkeyFormat: '0x05-prefixed, 33 bytes (priv keys are 32 bytes raw)',
            versionByte: '0x33 = (3<<4)|3 for v3; first byte of every ciphertext',
            macTrailer: 'last 8 bytes = HMAC-SHA256(macKey, version||serialized)[:8]',
            protoFields: {
                WhisperMessage: { ephemeralKey: 1, counter: 2, previousCounter: 3, ciphertext: 4 },
                PreKeyWhisperMessage: { preKeyId: 1, baseKey: 2, identityKey: 3, message: 4, registrationId: 5, signedPreKeyId: 6 },
            },
        },
        bob: {
            identityKeyPair: { priv: hex(bobIdentity.privKey), pub: hex(bobIdentity.pubKey) },
            registrationId: bobRegId,
            signedPreKey: {
                id: bobSignedPreKey.keyId,
                keyPair: { priv: hex(bobSignedPreKey.keyPair.privKey), pub: hex(bobSignedPreKey.keyPair.pubKey) },
                signature: hex(bobSignedPreKey.signature),
            },
            preKey: {
                id: bobPreKey.keyId,
                keyPair: { priv: hex(bobPreKey.keyPair.privKey), pub: hex(bobPreKey.keyPair.pubKey) },
            },
        },
        alice: {
            identityKeyPair: { priv: hex(aliceIdentity.privKey), pub: hex(aliceIdentity.pubKey) },
            registrationId: aliceRegId,
            // base/ephemeral key alice used in X3DH (initOutgoing); pub only is recoverable.
            baseKey: { pub: hex(aliceBaseKeyPub) },
        },
        exchanges,
    };

    mkdirSync(dirname(OUT), { recursive: true });
    writeFileSync(OUT, JSON.stringify(out, null, 2));

    console.log(`wrote ${OUT}`);
    console.log(`roundtrip OK ${exchanges.length}/${exchanges.length}`);
    console.log('types:', exchanges.map((e) => `${e.dir}:${e.type}(v${e.ciphertextHex.slice(0, 2)})`).join(' '));
}

function assertEq(got, want, label) {
    const g = Buffer.from(got);
    if (!g.equals(want)) {
        throw new Error(`${label} roundtrip mismatch: got ${g.toString('utf8')} want ${want.toString('utf8')}`);
    }
}

main().catch((e) => { console.error(e); process.exit(1); });
