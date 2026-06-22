# Instance Manager (#7) — Design

Date: 2026-06-22
Package: `internal/manager`
Command: `cmd/wa-manager`

## Goal

Run N WhatsApp sessions (each a `client.Client` with its own `store`)
concurrently in one process, with per-instance supervision, exponential-backoff
reconnection with jitter, and an aggregated event stream tagged by instance name.

## Step 0 — injectable transport

`client.Client` hard-wired `c.dial = dialWebSocket`. Added
`client.NewWithDialer(s store.Store, dial func(ctx)(io.ReadWriteCloser,error)) *Client`;
`New` now delegates to it with `dialWebSocket`. A nil dialer falls back to
`dialWebSocket`. This makes the transport injectable for offline tests without
changing any existing call site or behavior (all existing tests stay green).

## Abstraction choice: `Session` interface, not a mock transport

The Manager depends on a minimal interface that `*client.Client` satisfies:

```go
type Session interface {
    Connect(ctx context.Context) error
    Events() <-chan client.Event
    SendText(ctx context.Context, to, text string) (string, error)
}
var _ Session = (*client.Client)(nil) // compile-time guarantee
```

Rationale: faithfully mocking the Noise XX handshake + a stateful WhatsApp server
over an in-memory `io.ReadWriteCloser` is high-effort and brittle. The Manager's
job is *orchestration* (supervision, backoff, aggregation, state derivation), which
is fully exercisable through `Session`. Tests inject a trivial `fakeSession` that
emits scripted `client.Event`s and returns from `Connect` to simulate drops.
`NewWithDialer` remains available for higher-fidelity client-level offline tests.

## Per-attempt session factory

`*client.Client.Connect` **closes `Events()` when it returns**. A reconnect therefore
needs a *fresh* client. So an instance is registered with a **factory** producing a
new `Session` per attempt:

- `Add(name, store.Store)` — production: factory builds `client.New(st)` each attempt.
- `AddSession(name, Session)` — fixed session (tests / one-shot, no real reconnect).
- `AddFactory(name, func() Session)` — explicit factory (tests for reconnection).

## Manager structure

- `Start(ctx)` derives a root context and launches one **supervisor goroutine**
  per instance. Late `Add*` after `Start` joins the running root context.
- **Supervisor loop** per instance:
  1. state -> `Connecting`; acquire a concurrency slot.
  2. `factory()` -> `Connect(ctx)` (blocks until drop/cancel).
  3. a **pump** goroutine forwards that attempt's events to the aggregated channel
     (tagged with the name) and derives `State`.
  4. on return, if ctx still live: state -> `Backoff`, sleep `backoff(attempt)`, retry.
- **Concurrency cap** (`WithConcurrency`, default 16): a buffered-channel semaphore
  bounds the *connecting window only*. The slot is released once the instance
  **settles** (Connected/LoggedIn) or the attempt ends — so steady-state sessions
  don't hold slots and starve others (this was the key fix for the 50-instance test).
- **Backoff** (`WithBackoff`, default `DefaultBackoff`): `1s,2s,4s…` capped at 60s
  with full jitter `rand[0,d]`. Injectable so tests run instantly/deterministically.
- **State derivation** (`stateFromEvent`): QR/PairSuccess -> Connected,
  LoggedIn -> LoggedIn, Disconnected -> Disconnected, else unchanged.
  `Status() map[string]State` is a concurrency-safe snapshot.

## Events aggregation & no-leak Stop

- `Events() <-chan InstanceEvent` where `InstanceEvent{Name, Event}`. A single
  buffered channel (cap 64) multiplexes all instances.
- `forward` is non-blocking on shutdown: it selects on `ctx.Done()`, so a slow or
  absent consumer can never wedge a supervisor.
- `Stop()` cancels the root context, `wg.Wait()`s every supervisor (each waits its
  pump via `pumpDone`), then closes the aggregated channel exactly once
  (`sync.Once`). No goroutine outlives `Stop`.

## Tests (`manager_test.go`, offline)

1. N instances all reach `LoggedIn`.
2. Aggregated events carry the correct instance `Name`.
3. Reconnect: attempt 1 emits `Disconnected` and returns; manager retries
   (connect count >= 2, backoff consulted) and reaches `LoggedIn`. Backoff is
   injected as `0` so it's instant.
4. `Stop()` closes the aggregated channel and reclaims goroutines (NumGoroutine
   returns near baseline).
5. 50 instances with concurrent `Status`/`Events` readers, under `-race`.
6. `SendText` delegates to the live session via `ManagedClient`.
7. Duplicate instance names are rejected.

`go test -race ./... -count=1` green; manager suite green across `-count=10 -race`.
`go vet ./...` clean. `go build ./...` (incl. `cmd/wa-manager`) clean.

## Files

- modified: `internal/client/client.go` (NewWithDialer), `internal/client/client_test.go`
- added: `internal/manager/manager.go`, `internal/manager/manager_test.go`
- added: `cmd/wa-manager/main.go`
- added: this spec
