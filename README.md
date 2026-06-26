# wa-go

[![CI](https://github.com/jfelipesjc/wa-go/actions/workflows/ci.yml/badge.svg)](https://github.com/jfelipesjc/wa-go/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/jfelipesjc/wa-go/wa.svg)](https://pkg.go.dev/github.com/jfelipesjc/wa-go/wa)
[![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?logo=go)](https://go.dev)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

A **WhatsApp Web (multi-device) protocol stack written from scratch in Go** —
**no whatsmeow, no Baileys**. The wire framing, the Noise handshake, the Signal
E2E layer (X3DH + Double Ratchet), group sender keys, app-state (LTHash), media
crypto — all reimplemented to get **full control**: device fingerprint, human
send cadence, lightweight multi-account, and access to raw frames.

Think of it as **Baileys, in Go, built from the bytes up**. The `wa/` package is
the public entry point (the equivalent of Baileys' `index.ts`).

```go
import "github.com/jfelipesjc/wa-go/wa"
```

## Why build it from scratch?

Existing Go options wrap a fixed feature set and a fixed fingerprint. Rebuilding
the protocol — validated **byte-for-byte against golden traces captured from real
Baileys** — buys things a wrapper can't:

- **Fingerprint control** — own the device props, client payload, and version.
- **Cadence control** — a send-pacer models human timing instead of bursting.
- **Raw frame hooks** — inspect/modify nodes pre-encrypt and post-decrypt.
- **Lightweight multi-session** — many numbers in one process, supervised.
- **Zero-CGO storage** — SQLite via `modernc.org/sqlite`, so static builds.

## Status

Decomposed into 9 sub-projects (specs in `docs/superpowers/`):

| # | Sub-project | Status |
|---|-------------|--------|
| 0 | Capture harness (golden traces from real Baileys) | ✅ |
| 1 | Wire layer (framing · Noise XX · binary-node codec) | ✅ |
| 2 | Pairing/Auth (multi-device, QR **+ pairing-code**) | ✅ **proven live** |
| 3 | Signal/E2E (X3DH · Double Ratchet) from scratch | ✅ proven live (golden vectors + real msgs decrypted) |
| 4 | Messaging 1:1 (send + receive) | ✅ **proven live** — text + media + reaction |
| 4+ | Groups (sender keys), media crypto+transfer, all msg types | ✅ media proven live; groups offline |
| 5 | App-state sync (LTHash) — decode/encode/resync | ✅ offline |
| 6 | Control layer (fingerprint · SendPacer · frame hooks) | ✅ offline |
| 7 | Instance manager (multi-session) | ✅ offline (`-race`, 50 instances) |
| 8 | Evolution-compat HTTP/WS | ✅ separate repo → [**wa-evolution**](https://github.com/jfelipesjc/wa-evolution) |

### Proven *live* vs *offline*

This distinction is tracked honestly:

- **Proven live** = exercised end-to-end against **real WhatsApp**: pairing
  (QR + code), receiving, and sending **text + image + reaction**.
- **Offline** = code is complete and **passes its tests**, but those tests are
  golden-vector / round-trip, **not yet smoke-tested against a live account**:
  groups (sender keys), app-state resync, profile/privacy, status, newsletters,
  business, calls.

> "Offline" does **not** mean untested — it means tested without the network.
> See the test suite: **440+ tests, `-race` green.**

## Feature coverage

**Messages:** text, reply, mention, image/video/audio/document/sticker (crypto +
HTTP up/download), location, contact, reaction, edit, delete, poll, view-once,
buttons/list/template/interactive; receive parses every type into rich events.
**Groups & communities:** sender-key E2E send/receive, create, add/remove/
promote/demote, subject/description, invites, settings, ephemeral, sub-groups.
**App-state:** archive/pin/mute/read/star/clear/delete chat, labels, resync.
**Profile/privacy:** name/status/picture, fetch status/picture, privacy
settings, block/unblock/blocklist. **Status/Newsletters/Business:** text status,
channel create/follow/admin, business profile/catalog/orders. **Other:**
presence/typing/receipts, calls (offer/reject/terminate), onWhatsApp, history
sync. **Infra:** multi-session manager, per-instance fingerprint, send pacer,
raw frame hooks. → Full API on [pkg.go.dev](https://pkg.go.dev/github.com/jfelipesjc/wa-go/wa).

## Install

```sh
go get github.com/jfelipesjc/wa-go@latest
```

## Quick start

```go
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/jfelipesjc/wa-go/wa"
)

func main() {
	store, err := wa.OpenStore("./creds.db") // SQLite; reused on next run
	if err != nil {
		panic(err)
	}
	c := wa.NewClient(store)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	go c.Connect(ctx) // pairs via QR (if needed), then logs in

	for ev := range c.Events() {
		switch ev := ev.(type) {
		case wa.QREvent:
			fmt.Println("scan in WhatsApp > Linked devices:\n", ev.Code)
		case wa.LoggedInEvent:
			fmt.Println("logged in")
			c.SendText(ctx, "5512999999999@s.whatsapp.net", "hello from wa-go")
		case wa.MessageEvent:
			if !ev.IsGroup {
				fmt.Printf("← %s: %s\n", ev.From, ev.Text)
			}
		case wa.DisconnectedEvent:
			fmt.Println("disconnected:", ev.Reason)
		}
	}
}
```

## Pairing

Two ready-to-run commands (use an **isolated, sacrificial number** — see below):

```sh
# QR — renders a QR in the terminal to scan
go run ./cmd/wa-pair -db ./wa-pair.creds.db -timeout 120s

# Pairing code — prints an 8-char code to type in WhatsApp > Linked devices
go run ./cmd/wa-paircode -phone 5512999998888 -db ./paircode.creds.db
```

Both flows (`companion_hello → primary_hello → companion_finish → pair-success →
login`) are validated end-to-end against real WhatsApp.

## Multi-session

```sh
go run ./cmd/wa-manager -dir ./sessions -concurrency 8
```

`wa.NewManager()` supervises many `Client`s with auto-reconnect and exponential
backoff, aggregating every instance's events into one stream.

## Architecture

```
wa/                 public facade (type aliases over internal/)
internal/
  wire/             3-byte framing, token dictionary, node codec, Noise XX
  signal/           X3DH, Double Ratchet, sender keys
  keys/  store/     key material + SQLite persistence
  client/           connection, send/receive, groups, app-state, profile…
  appstate/         LTHash decode/encode/resync
  media/            media crypto + HTTP transfer
  control/          fingerprint, SendPacer, frame hooks
  manager/          multi-session supervisor
  waproto/          protobuf schema subset (regenerate via `go generate`)
```

## Development

```sh
go test ./...            # offline suite (unit + golden vectors + round-trips)
go test -race ./...      # what CI runs
go run ./cmd/wiredump    # replay a trace, decode the pair-device frame (no net)
```

Regenerate the protobuf after editing `internal/waproto/waproto.proto`:

```sh
go generate ./internal/waproto/    # needs protoc + protoc-gen-go v1.36.x
```

Re-capture golden traces (optional, needs Node): `cd harness && npm i &&
node harness/capture.mjs` connects to real WhatsApp up to the QR (no number
needed) and rewrites `testdata/traces/`.

## ⚠️ Operational note

Connecting from Go talks to **real WhatsApp**. Use an **isolated, sacrificial
number**, and **do not re-pair / remove the same account in a loop** — that burns
the account's device-management and the server stops relaying your sends.

## Relationship to wa-evolution

This is **the library**. An Evolution-API-style multi-instance HTTP service that
imports it lives in [**wa-evolution**](https://github.com/jfelipesjc/wa-evolution).

## License

[MIT](LICENSE) © José Felipe Leal
