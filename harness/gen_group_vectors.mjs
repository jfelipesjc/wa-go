// gen_group_vectors.mjs
//
// Generate golden Signal *group* (Sender Key) vectors using the SAME libsignal /
// Baileys group implementation that WhatsApp uses
// (harness/node_modules/@whiskeysockets/baileys/lib/Signal/Group/*, which builds
// on harness/node_modules/libsignal/src/{crypto,curve}.js — the very modules the
// 1:1 vectors in gen_signal_vectors.mjs exercise).
//
// Scenario (mirrors a real group fan-out):
//   1. alice creates a sender key for group "g1" -> SenderKeyDistributionMessage
//   2. bob processes alice's SKDM (he can now decrypt alice's group msgs)
//   3. alice encrypts 3 group messages (SenderKeyMessage); bob decrypts each
//
// We capture, with private keys, everything the Go side needs to (a) process the
// SKDM and decrypt, and (b) re-encrypt byte-for-byte:
//   - senderKeyId, initial chainKey (seed) + iteration
//   - the signing key pair (Curve25519; private 32b, public 33b 0x05-prefixed)
//   - the raw SKDM bytes
//   - each {iteration, ciphertextHex, plaintextHex} plus the SenderKeyMessage bytes
//
// The signing key pair is minted by libsignal's curve.generateKeyPair, which the
// on-disk curve.js test patch reports via global.__WA_GO_KP_HOOK (same hook the
// 1:1 generator uses). That lets us recover the signing PRIVATE key (it is not
// otherwise serialized in a form we read here — we read it straight from state,
// but the hook is a belt-and-suspenders cross-check and matches the house style).
//
// Roundtrip is asserted inside libsignal itself (n/n) before dumping.
// Offline only; synthetic keys.

import { createRequire } from 'module';
import { writeFileSync, mkdirSync } from 'fs';
import { dirname, resolve } from 'path';
import { fileURLToPath } from 'url';

const require = createRequire(import.meta.url);

const {
    GroupSessionBuilder,
    GroupCipher,
    SenderKeyName,
    SenderKeyRecord,
    SenderKeyDistributionMessage,
} = await import(
    '@whiskeysockets/baileys/lib/Signal/Group/index.js'
);

// Capture hook: record every keypair curve.generateKeyPair mints while capturing.
const ephemeralsGenerated = [];
let capturing = false;
global.__WA_GO_KP_HOOK = function (kp) {
    if (capturing) ephemeralsGenerated.push({ priv: kp.priv, pub: kp.pub });
};

const __dirname = dirname(fileURLToPath(import.meta.url));
const OUT = resolve(__dirname, '..', 'testdata', 'signal', 'group_ab.json');
const hex = (b) => Buffer.from(b).toString('hex');

// In-memory SenderKeyStore: loadSenderKey / storeSenderKey keyed by
// SenderKeyName.toString(). Mirrors Baileys' makeSenderKeyStore shape.
function makeSenderKeyStore() {
    const store = new Map(); // name string -> serialized record bytes
    return {
        _store: store,
        loadSenderKey: async (name) => {
            const data = store.get(name.toString());
            if (!data) return new SenderKeyRecord();
            return SenderKeyRecord.deserialize(data);
        },
        storeSenderKey: async (name, record) => {
            store.set(name.toString(), Buffer.from(JSON.stringify(record.serialize())));
        },
    };
}

function assertEq(got, want, label) {
    const g = Buffer.from(got);
    if (!g.equals(Buffer.from(want))) {
        throw new Error(`${label} mismatch: got ${hex(g)} want ${hex(want)}`);
    }
}

async function main() {
    const groupId = 'g1';
    // sender addresses are {id, deviceId}; name.serialize uses sender.id/deviceId
    const aliceSender = { id: 'alice', deviceId: 1, toString: () => 'alice.1' };

    const aliceName = new SenderKeyName(groupId, aliceSender);
    const bobName = new SenderKeyName(groupId, aliceSender); // bob stores under ALICE's name

    const aliceStore = makeSenderKeyStore();
    const bobStore = makeSenderKeyStore();

    const aliceBuilder = new GroupSessionBuilder(aliceStore);
    const bobBuilder = new GroupSessionBuilder(bobStore);

    // ---- 1. alice creates her sender key for the group ----
    capturing = true; // signing keypair minted here
    const skdm = await aliceBuilder.create(aliceName);
    capturing = false;

    const skdmBytes = skdm.serialize();

    // Read alice's freshly created state to capture seed/keyId/signing keys.
    const aliceRec = await aliceStore.loadSenderKey(aliceName);
    const aliceState = aliceRec.getSenderKeyState();
    const initialChainKey = aliceState.getSenderChainKey().getSeed(); // 32b seed
    const initialIteration = aliceState.getSenderChainKey().getIteration(); // 0
    const senderKeyId = aliceState.getKeyId();
    const signingPub = aliceState.getSigningKeyPublic();   // 33b (0x05 + 32)
    const signingPriv = aliceState.getSigningKeyPrivate(); // 32b

    // SKDM fields (what bob will consume).
    if (skdm.getId() !== senderKeyId) throw new Error('skdm id mismatch');
    assertEq(skdm.getChainKey(), initialChainKey, 'skdm chainKey');
    assertEq(skdm.getSignatureKey(), signingPub, 'skdm signingKey');

    // ---- 2. bob processes alice's SKDM ----
    const skdmForBob = new SenderKeyDistributionMessage(null, null, null, null, Buffer.from(skdmBytes));
    await bobBuilder.process(bobName, skdmForBob);

    // ---- 3. alice encrypts group messages; bob decrypts ----
    const aliceCipher = new GroupCipher(aliceStore, aliceName);
    const bobCipher = new GroupCipher(bobStore, bobName);

    const plaintexts = [
        Buffer.from('grupo msg um', 'utf8'),
        Buffer.from('segunda mensagem', 'utf8'),
        Buffer.from('terceira no grupo!', 'utf8'),
    ];

    // NOTE on iterations: GroupCipher.encrypt uses
    //   getSenderKey(state, iteration === 0 ? 0 : iteration + 1)
    // so after the first message (wire iteration 0) the recorded chain iteration
    // is 1, and the next message derives the key for iteration 2 (skipping 1),
    // then 4, etc. The on-wire SenderKeyMessage.iteration is thus 0, 2, 4 — we
    // read it back from the produced bytes rather than assuming sequential.
    const { SenderKeyMessage } = await import(
        '@whiskeysockets/baileys/lib/Signal/Group/sender-key-message.js'
    );

    const messages = [];
    for (let i = 0; i < plaintexts.length; i++) {
        const pt = plaintexts[i];
        const ctBytes = await aliceCipher.encrypt(pt); // SenderKeyMessage.serialize()
        const dec = await bobCipher.decrypt(Buffer.from(ctBytes));
        assertEq(dec, pt, `group msg ${i + 1} roundtrip`);
        const parsed = new SenderKeyMessage(null, null, null, null, Buffer.from(ctBytes));
        messages.push({
            n: i + 1,
            iteration: parsed.getIteration(), // ACTUAL on-wire iteration
            plaintext: pt.toString('utf8'),
            plaintextHex: hex(pt),
            ciphertextHex: hex(ctBytes), // full SenderKeyMessage wire bytes
        });
    }

    // Recover signing private from capture hook as a cross-check (must match state).
    const signingPubHex = hex(signingPub);
    const sigEntry = ephemeralsGenerated.find((e) => '05' + e.pub.replace(/^05/, '') === signingPubHex || e.pub === signingPubHex || ('05' + e.pub) === signingPubHex);
    const capturedPrivOk = sigEntry ? (sigEntry.priv === hex(signingPriv)) : false;

    const out = {
        _meta: {
            lib: '@whiskeysockets/baileys (lib/Signal/Group) + libsignal',
            baileysVersion: require('@whiskeysockets/baileys/package.json').version,
            libsignalVersion: require('libsignal/package.json').version,
            note: 'Golden group Sender Key vectors. alice creates sender key, bob processes SKDM, alice encrypts, bob decrypts.',
            senderKeyMessageWire: 'version_byte(0x33) || protobuf{id=1 uint32, iteration=2 uint32, ciphertext=3 bytes} || signature[64] (XEdDSA over version||protobuf, deterministic)',
            skdmWire: 'version_byte(0x33) || protobuf{id=1 uint32, iteration=2 uint32, chainKey=3 bytes, signingKey=4 bytes(33b 0x05-prefixed)}',
            chainKeyDerivation: 'next chainKey = HMAC-SHA256(chainKey, 0x02); messageKey seed = HMAC-SHA256(chainKey, 0x01)',
            messageKeyDerivation: "deriveSecrets(seed, salt=zeros[32], info='WhisperGroup') -> T1,T2; iv=T1[0:16]; cipherKey=T1[16:32]||T2[0:16] (AES-256-CBC)",
            signingKeyFormat: 'Curve25519; public 33b 0x05-prefixed, private 32b raw; signature is non-randomized XEdDSA',
            capturedSigningPrivMatchesState: capturedPrivOk,
        },
        group: { groupId, senderName: aliceName.serialize() },
        senderKey: {
            keyId: senderKeyId,
            iteration: initialIteration,
            chainKey: hex(initialChainKey),
            signingKey: { priv: hex(signingPriv), pub: signingPubHex },
        },
        skdmBytesHex: hex(skdmBytes),
        messages,
    };

    mkdirSync(dirname(OUT), { recursive: true });
    writeFileSync(OUT, JSON.stringify(out, null, 2));

    console.log(`wrote ${OUT}`);
    console.log(`roundtrip OK ${messages.length}/${messages.length}`);
    console.log(`keyId=${senderKeyId} iteration0=${initialIteration} skdm=${skdmBytes.length}b`);
    console.log(`signing priv recovered via hook & matches state: ${capturedPrivOk}`);
}

main().catch((e) => { console.error(e); process.exit(1); });
