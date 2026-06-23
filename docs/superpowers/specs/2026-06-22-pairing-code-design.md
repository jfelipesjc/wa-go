# Pairing-by-code (link with phone number) — design

Status: REQUEST stage (companion_hello) + crypto implemented & golden-tested
offline. FINISH stage (companion_finish) is LIVE-PENDING.

Pairing-by-code is an alternative to QR scanning: the user types an 8-character
code on their phone instead of scanning a QR. This mirrors Baileys'
`requestPairingCode` (lib/Socket/socket.js) and the `link_code_companion_reg`
response handler (lib/Socket/messages-recv.js).

## Vocabulary / where things live

- Crypto + request stage: `internal/client/paircode.go`
- Golden vectors: `harness/gen_paircode_vectors.mjs` → `testdata/paircode/vectors.json`
- Tests: `internal/client/paircode_test.go`
- Persisted pairing ephemeral: `store.Creds.PairingEphemeral`

## Crypto primitives (validated by golden vector vs Baileys)

1. **Crockford base32** (`crockfordEncode`) — alphabet
   `123456789ABCDEFGHJKLMNPQRSTVWXYZ` (32 chars). Starts at `1` (no `0`); omits
   `I,O,U`; **includes `L`** (this differs from canonical Crockford, which also
   drops `L`). Bits consumed MSB-first; trailing partial group left-padded with
   zero bits. 5 bytes → exactly 8 chars. This is the pairing code itself
   (`bytesToCrockford(randomBytes(5))`).
2. **derivePairingCodeKey** — `PBKDF2-HMAC-SHA256(code, salt, iters=2<<16=131072,
   dkLen=32)`. `code` is the UTF-8 bytes of the 8-char code; `salt` is 32 random
   bytes. Go: `pbkdf2.Key([]byte(code), salt, 131072, 32, sha256.New)`.
3. **wrapCompanionEphemeral** (Baileys `generatePairingKey`) —
   `salt(32) || iv(16) || AES-256-CTR(ephemeralPub, derivePairingCodeKey(code,salt), iv)`.
   CTR is a stream cipher so the ciphertext is 32 bytes; the whole blob is 80
   bytes. Go: `crypto/aes` + `cipher.NewCTR`.

Golden blob (fixed code `WAGOTEST`, salt `00..1f`, iv `a0..af`, ephemeral
`ff..e0`):
`000102...1f a0a1...af 4fcf4335f595674b5674f2046dcb543a44f8c789894162fde881ca5f245b5abb`.

## REQUEST stage — companion_hello (implemented)

`(*Client).RequestPairingCode(ctx, phoneNumber)`:

1. Requires an active *pairing* session (Noise handshake done, pre-pair-success).
2. Generate 8-char code (`GeneratePairingCode`).
3. Generate a fresh pairing ephemeral Curve25519 key pair; **persist it +
   code's effects + `me` into creds** (so a later finish can derive from the
   private half, even across reconnect).
4. Generate random 32-byte salt + 16-byte iv.
5. Build & send the iq (`buildCompanionHelloIQ`, a pure testable builder):

```
<iq to=s.whatsapp.net type=set id=.. xmlns=md>
  <link_code_companion_reg jid=<phone>@s.whatsapp.net stage=companion_hello
                           should_show_push_notification=true>
    <link_code_pairing_wrapped_companion_ephemeral_pub>{salt||iv||ct}</...>
    <companion_server_auth_key_pub>{noiseKey.public}</...>
    <companion_platform_id>{platformId, e.g. "1"=CHROME}</...>
    <companion_platform_display>{"Chrome (Ubuntu)"}</...>   # `${browser[1]} (${browser[0]})`
    <link_code_pairing_nonce>0</...>
  </link_code_companion_reg>
</iq>
```

The user is shown `code` and types it on their phone.

## FINISH stage — companion_finish (LIVE-PENDING)

Implemented only as `(*Client).finishCompanionPairing()` returning a
"LIVE-PENDING" error + a detailed TODO. It cannot be unit-tested offline because
it requires a live server reply triggered by the phone accepting the code.

When the server replies with an `<iq>` carrying `<link_code_companion_reg>`
(Baileys' `messages-recv.js` "link_code_companion_reg" case), do:

1. Read `link_code_pairing_ref`, `primary_identity_pub`, and
   `link_code_pairing_wrapped_primary_ephemeral_pub`.
2. `codePairingPublicKey = decipherLinkPublicKey(wrapped)` — strip the
   `salt(32)||iv(16)` prefix, then **AES-CTR-decrypt** the rest with
   `derivePairingCodeKey(code, salt)`. (Inverse of `wrapCompanionEphemeral`;
   CTR decrypt == encrypt, so reuse the same primitive.)
3. `companionSharedKey = X25519(pairingEphemeral.priv, codePairingPublicKey)`
   (the persisted `creds.PairingEphemeral.Priv`).
4. `random = 32 rand bytes`; `linkCodeSalt = 32 rand bytes`.
5. `linkCodePairingExpanded = HKDF-SHA256(companionSharedKey, 32,
   salt=linkCodeSalt, info="link_code_pairing_key_bundle_encryption_key")`.
6. `payload = signedIdentityKey.pub || primary_identity_pub || random`.
7. `encrypted = AES-256-GCM(payload, linkCodePairingExpanded, iv=12 rand bytes,
   aad=empty)`; `wrappedKeyBundle = linkCodeSalt || iv(12) || encrypted`.
8. `identitySharedKey = X25519(signedIdentityKey.priv, primary_identity_pub)`.
9. `advSecretKey = HKDF-SHA256(companionSharedKey || identitySharedKey || random,
   32, info="adv_secret")`. **Persist it** — it replaces the random advSecret;
   the subsequent pair-success HMAC verifies against THIS value.
10. Reply:

```
<iq to=s.whatsapp.net type=set id=.. xmlns=md>
  <link_code_companion_reg jid=<me> stage=companion_finish>
    <link_code_pairing_wrapped_key_bundle>{wrappedKeyBundle}</...>
    <companion_identity_public>{signedIdentityKey.public}</...>
    <link_code_pairing_ref>{echoed ref}</...>
  </link_code_companion_reg>
</iq>
```

After companion_finish the server proceeds to the **normal pair-success**
exchange — `handlePairSuccess` (pairing.go) already implements it; the
advSecretKey from step 9 makes its HMAC verify.

### Wiring TODO (live)

- The QR `pairingLoop` is untouched. To drive code pairing we need either a
  pairing-mode flag so the loop sends companion_hello after the handshake (and
  routes the server's `link_code_companion_reg` reply into `finishCompanionPairing`
  instead of waiting on `pair-device`), or a dedicated pairing session exposed to
  `RequestPairingCode`. Decide when testing live.
- `signedIdentityKey` referenced in finish == `creds.IdentityKey` (the device
  identity key pair); confirm against the live trace.
- HKDF: use the existing project HKDF (appstate/keys uses HKDF-SHA256) — confirm
  it's parameterizable with salt+info; otherwise add a small helper.

## Obstacles / notes

- The Crockford alphabet `L`-inclusion is a real footgun; the test pins the
  exact string.
- Baileys' `derivePairingCodeKey` uses WebCrypto `2 << 16` iterations — that is
  131072, not 2^16. Confirmed in the golden vector (`iterations` field).
- `should_show_push_notification` and the platform display string must match the
  device profile fingerprint used at handshake, else the phone may show the
  wrong app name.
