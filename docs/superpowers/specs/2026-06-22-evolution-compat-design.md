# Evolution-compatible HTTP service (#8)

Wraps the multi-session `internal/manager` + `internal/client` + `internal/store`
stack in an HTTP/JSON service that mirrors the **Evolution API v2** contract the
user's Chatwoot/workers already speak. Pure `net/http` (`http.ServeMux`), no
router dependency.

## Goals

- Multi-instance lifecycle (create / connect / list / delete / logout) over HTTP.
- Send text / media / reaction; query findMessages / whatsappNumbers / groups.
- Per-instance **webhook** dispatch: a goroutine drains `Manager.Events()` and
  POSTs each relevant event to the instance's `webhookUrl` in Evolution shape
  (`{event, instance, data}`).
- Global `apikey` header auth on every route.
- Fully testable offline (httptest + a fake backend + a fake event source),
  reusing the dialer/session injection pattern from `internal/manager`.

## Architecture

```
cmd/wa-server/main.go        flags -addr -apikey -dir; wires the real Manager
internal/api/
  types.go                   Evolution-shaped request/response DTOs
  server.go                  Server, mux, auth middleware, JSON helpers, QR PNG
  backend.go                 Backend interface (lifecycle+send) + Manager adapter
  instances.go               instance routes
  messages.go                message + chat + group routes
  webhook.go                 event -> Evolution webhook dispatcher
```

### Backend seam (testability)

Handlers never touch the Manager directly; they call a `Backend` interface:

```go
type Backend interface {
    Create(name string) error
    Connect(ctx, name) (qr string, err error)   // returns last QR code string
    Delete(name) error
    Logout(name) error
    Status() map[string]string                   // name -> open|connecting|close
    Exists(name) bool
    SendText(ctx, name, jid, text) (id string, err error)
    SendMedia(ctx, name, jid string, m MediaArg) (id string, err error)
    SendReaction(ctx, name, jid, msgID string, fromMe bool, emoji string) (id, err error)
    FindMessages(name, jid string, limit int) ([]StoredMsg, error)
    WhatsAppNumbers(ctx, name, numbers []string) ([]NumberStatus, error)
    Groups(ctx, name) ([]GroupArg, error)
    GroupMetadata(ctx, name, jid string) (GroupArg, error)
}
```

The production `managerBackend` adapts `*manager.Manager` + a map of
`store.Store` + a `*client.ChatStore` per instance (fed by the webhook pump's
event drain). The Connect path starts the instance under the Manager and waits
(bounded) for the first `QREvent` captured by the event pump, returning its
`Code`. Tests inject a `fakeBackend` — no network, no Noise handshake.

Manager `State` maps to Evolution `connectionStatus`:
`LoggedIn -> open`, `Connecting/Connected/Backoff -> connecting`, else `close`.

### Routes (apikey header required on all)

| Method | Path | Body | Response |
|--------|------|------|----------|
| POST | `/instance/create` | `{instanceName, webhookUrl?}` | `{instance:{instanceName,status}}` |
| GET  | `/instance/connect/{instance}` | — | `{code:"<base64 PNG>", base64:"data:image/png;base64,..."}` |
| GET  | `/instance/fetchInstances` | — | `[{instanceName, connectionStatus}]` |
| DELETE | `/instance/delete/{instance}` | — | `{status:"SUCCESS"}` |
| GET  | `/instance/logout/{instance}` | — | `{status:"SUCCESS"}` |
| POST | `/webhook/set/{instance}` | `{url}` | `{webhook:{url}}` |
| POST | `/message/sendText/{instance}` | `{number,text}` | `{key:{remoteJid,id},status}` |
| POST | `/message/sendMedia/{instance}` | `{number,mediatype,media,caption?,fileName?,mimetype?}` | `{key:{...},status}` |
| POST | `/message/sendReaction/{instance}` | `{key:{remoteJid,id,fromMe},reaction}` | `{key:{...},status}` |
| POST | `/chat/findMessages/{instance}` | `{where:{key:{remoteJid}},limit?}` | `{messages:{records:[...]}}` |
| POST | `/chat/whatsappNumbers/{instance}` | `{numbers:[...]}` | `[{exists,jid,number}]` |
| GET  | `/group/fetchAllGroups/{instance}` | — | `[{id,subject,...}]` |
| GET  | `/group/groupMetadata/{instance}?groupJid=` | — | `{id,subject,participants}` |

`number` accepts a bare phone (E.164, no `@`) or a full JID; the server appends
`@s.whatsapp.net` when no domain is present.

### Webhook dispatch (event -> Evolution event name)

A single goroutine drains `Manager.Events()`. For each `InstanceEvent` it looks
up the instance's `webhookUrl` and POSTs `{event, instance, data}` (Evolution
shape) when the event maps to a webhook. Mapping:

| client event | Evolution `event` | `data` shape |
|--------------|-------------------|--------------|
| `MessageEvent` | `messages.upsert` | `{key:{remoteJid,fromMe,id}, pushName, message:{conversation/…}, messageType, messageTimestamp}` |
| `ReceiptUpdateEvent` / `ReceiptEvent` | `messages.update` | `{keyId, remoteJid, status}` |
| `QREvent` | `qrcode.updated` | `{qrcode:{code, base64}}` |
| `LoggedInEvent` / `PairSuccessEvent` | `connection.update` | `{state:"open"}` |
| `DisconnectedEvent` | `connection.update` | `{state:"close", statusReason}` |
| `PresenceEvent` | `presence.update` | `{id, presences}` |
| `GroupParticipantsUpdateEvent` | `group-participants.update` | `{id, action, participants}` |

Other events are ignored (no webhook). The pump also feeds each `MessageEvent`
into the instance's `ChatStore` so `findMessages` has data. Delivery is
best-effort: a short-timeout `http.Client`, failures logged and dropped (a
slow Chatwoot must not wedge the manager).

### WS

**Skipped.** `net/http` has no built-in WebSocket server (only the client-side
`x/net/websocket` is non-stdlib, and the repo's `coder/websocket` is a dep we
were told not to expand usage of for the router). The webhook HTTP push covers
the Chatwoot/worker ingestion path, which is what Evolution integrations use.
A `/ws/{instance}` upgrade can be added later with `coder/websocket`.

## Testing (offline)

- **auth**: missing/empty `apikey` -> 401; correct key -> 200.
- **create + fetchInstances**: create registers; fetch lists it with
  `connectionStatus`.
- **sendText**: POST returns `{key:{id},status:"PENDING"}`; fake backend records
  the call (no wire).
- **sendMedia / sendReaction / findMessages / whatsappNumbers**: response shape.
- **webhook**: inject a `MessageEvent` into the event source; an `httptest.Server`
  destination receives a POST whose body is `{event:"messages.upsert",
  instance, data:{...}}`.

All tests use the `fakeBackend` + an in-process event channel, mirroring the
manager's fake-session approach. No test dials the network.
