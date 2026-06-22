# Control Layer (#6) — Design

Date: 2026-06-22
Status: implemented

The Control Layer is wa-go's anti-ban core. It makes an automated, headless
client present and behave like a real human's linked WhatsApp device. It has
three independent pieces, all living in `internal/control` (plus thin wiring in
`internal/client` and `internal/waproto`):

- **(A) DeviceProfile** — per-instance device fingerprint.
- **(B) HumanPacer** — human-like send cadence + rate limiting.
- **(C) Frame hooks** — raw node inspection/manipulation on send/receive.

## (A) DeviceProfile — `internal/control/device_profile.go`

`DeviceProfile` holds the fingerprint fields that flow into the registration /
login `ClientPayload`: the browser tuple `{os, browser, osVersion}`, the
UserAgent `osVersion` / `device` / `osBuildNumber`, the WhatsApp Web client
version triple, the resolved `DeviceProps.PlatformType`, and the locale bundle
(`localeLanguageIso6391`, `localeCountryIso31661Alpha2`, `mcc`, `mnc`).

- `DefaultProfile()` returns the historical hardcoded values
  (`Browsers.ubuntu('Chrome')`, version `2.3000.1035194821`, locale `US`,
  `osVersion`/`build` = `0.1`, device `Desktop`).
- `RandomDesktopProfile(seed int64)` is **deterministic** (seed in → profile
  out) and uses a *local* `math/rand` source, never the global one. It picks
  from a curated `desktopPool` of **valid OS × browser combinations** (e.g. no
  Safari on Windows), a `localePool` of coherent language/country/carrier
  bundles, and a small `clientVersionPool`. The injectable core
  `randomDesktopProfileFrom(*rand.Rand)` lets callers supply their own source.
- `(DeviceProfile).RegInput(base)` / `.LoginInput(base)` merge the fingerprint
  into a `waproto.RegInput` / `waproto.LoginInput`, leaving key material / JID
  parts untouched.

### Fixture preservation (critical regression)

`waproto.RegInput` / `LoginInput` gained optional fingerprint fields
(`OSVersion`, `Device`/`DeviceName`, `OSBuildNumber`, `LocaleLang`, `MCC`,
`MNC`). The payload builder substitutes the original Baileys default for any
**empty** field via `uaOrDefault`, so a zero-valued input still produces the
exact historical UserAgent. `DefaultProfile()` sets those fields to the same
values explicitly. Result: `RegistrationPayload(DefaultProfile().RegInput(...))`
reproduces `testdata/traces/connect_pair/client_payload.json` field-for-field —
proven by `TestDefaultProfileReproducesFixture` and the unchanged
`waproto.TestRegistrationReproducesFixture`.

### Integration

The `client.Client` carries a `profile control.DeviceProfile` (default
`DefaultProfile()`), set via the new `WithDeviceProfile` option.
`registrationInput` / `loginPayloadBytes` became `*Client` methods that thread
the profile into the payload. The old package-level `waVersion` / `browser` /
`countryCode` constants were removed.

## (B) HumanPacer — `internal/control/pacer.go`

`Pacer` interface: `Wait(ctx, textLen) (delay, err)` + `Allow() bool`.
`HumanPacer` is the concrete, fully testable implementation.

- **Delay model**: gaussian base reaction time
  (`NormFloat64()*BaseStdDev + BaseDelay`) **plus** a per-character typing
  component (`textLen * PerCharDelay`), **truncated** to `[MinDelay, MaxDelay]`.
  A periodic longer "breather" (`LongPause`) is added every `LongPauseEvery`
  messages.
- **Rate limit**: `Allow()` is a sliding-window log limiter — at most
  `RateLimit` sends per `RateWindow`; expired timestamps are pruned on each call.
- **Determinism / testability**: randomness comes from an **injected**
  `*math/rand.Rand` (nil → fixed seed). Time and sleeping go through injectable
  `now` / `sleep` funcs; tests use a fake clock and run instantly. `Wait` honors
  `ctx` cancellation (production `realSleep` selects on `ctx.Done()` vs a timer).
- **TypingPresence (modeled only)**: `PlanTyping(textLen)` returns a `TypingPlan`
  (composing duration ∝ length, plus a "send paused after" flag). The actual
  wire presence (`composing` / `paused`) integration is deliberately future
  work; only the timing logic lives here.

### Integration

`Client` carries `pacer control.Pacer` (nil = original zero-delay behavior), set
via `WithPacer`. `SendText` calls `pacer.Allow()` (rejecting over-limit sends)
then `pacer.Wait(ctx, len(text))` **before** building/sending the stanza.

## (C) Frame hooks — `internal/client`

`Client.OnOutgoingNode(func(*wire.Node))` and `OnIncomingNode(func(wire.Node))`
register callbacks (slices, guarded by an `RWMutex`).

- **Outgoing** hooks fire inside the `send` closure of both `pairingLoop` and
  `loginLoop`, **pre-encrypt** (before `conn.SendNode`), with a pointer so a hook
  can inspect or mutate the node in place.
- **Incoming** hooks fire right after a successful `conn.ReadNode()` in both
  loops, **post-decode**.
- Every hook runs under `recover` (`safeNodeHook` / `safeNodePtrHook`): a
  panicking hook is swallowed and the loop continues. Default = no hooks.

`NewWithOptions(store, dial, ...Option)` is the new variadic constructor for the
Control Layer; `New` / `NewWithDialer` remain the zero-config paths.

## Files

- `internal/control/device_profile.go` (new)
- `internal/control/device_profile_test.go` (new)
- `internal/control/pacer.go` (new)
- `internal/control/pacer_test.go` (new)
- `internal/client/hooks_test.go` (new)
- `internal/waproto/payload.go` (extended: fingerprint fields + `uaOrDefault`)
- `internal/client/client.go` (profile/pacer/hook fields, options, hook runners)
- `internal/client/pairing.go` (profile-threaded payload methods, hook wiring)
- `internal/client/send.go` (pacer consulted before send)

## Verification

`go build ./...`, `go vet ./...`, and `go test -race ./... -count=1` all pass.
The byte-for-byte fixture regression (`waproto` + `control`) is green.
```
