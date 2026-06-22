# Pairing/Auth Implementation Plan (#2)

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. Steps use `- [ ]`.

**Goal:** Da wire layer (#1) até uma sessão autenticada — identidade, ClientPayload, transporte WS real, fluxo de pareamento QR, persistência.

**Architecture:** Camadas novas sobre `internal/wire`: `keys` (identidade), `store` (SQLite), `waproto` (protobuf gerado), `ws` (transporte real), `client` (orquestração). Tudo offline-testável menos scan/login (gated no chip sacrificial).

**Tech Stack:** Go 1.22, protoc 3.21 + protoc-gen-go, modernc.org/sqlite (CGo-free), github.com/coder/websocket, x/crypto (pin v0.31.0).

## Global Constraints
- Pareamento real só com chip sacrificial. Teste ao vivo offline NÃO pareia (sem número).
- Não commitar `creds.json` real nem `.auth_*` (gitignore).
- TDD; commits frequentes. x/crypto não fazer upgrade.
- Validar protobuf/QR contra fixtures capturados (`client_payload.json`, `qr.json`).

---

### Task 1: waproto — gerar ClientPayload + ADV
**Files:** Create `internal/waproto/*.proto` (subset), `internal/waproto/*.pb.go` (gerado), `internal/waproto/gen.go` (//go:generate), `internal/waproto/build_test.go`
**Interfaces:** Produces tipos `waproto.ClientPayload`, `UserAgent`, `WebInfo`, `CompanionRegData`, `ADVSignedDeviceIdentity`, `ADVDeviceIdentity`, `ADVSignedDeviceIdentityHMAC`.
- [ ] Extrair do `harness/node_modules/@whiskeysockets/baileys/WAProto/*.proto` o subset necessário (ClientPayload + deps + ADV*). Manter os MESMOS números de campo.
- [ ] `protoc --go_out` gera os `.pb.go`.
- [ ] Teste: `proto.Unmarshal` do `payloadHex` de `testdata/traces/connect_pair/client_payload.json` num `ClientPayload` sem erro, e campos top-level batem com `fields`.
- [ ] Commit.

### Task 2: keys — identidade do device
**Files:** Create `internal/keys/keys.go`, `keys_test.go`
**Interfaces:** Produces `GenKeyPair()`, `type KeyPair`, `NewRegistrationID()`, `NewAdvSecret()`, `GenSignedPreKey(identity, id)`.
- [ ] Testes: KeyPair tem 32 bytes priv/pub; pub = curve25519 base point × priv; regId em [1, 2^14); advSecret 32 bytes; signed prekey tem assinatura presente (verificação cripto é #3).
- [ ] Implementar com x/crypto.
- [ ] Commit.

### Task 3: store — SQLite creds
**Files:** Create `internal/store/store.go` (interface + Creds), `internal/store/sqlite.go`, `store_test.go`
**Interfaces:** Produces `type Store interface`, `type Creds`, `OpenSQLite(path)`. Métodos signal store declarados (stub blob por enquanto).
- [ ] Teste: SaveCreds→LoadCreds round-trip (todos os campos), incluindo após "pareamento" (me JID, account). DB em arquivo temp.
- [ ] Implementar com modernc.org/sqlite.
- [ ] Commit.

### Task 4: ws — transporte real + adapter
**Files:** Create `internal/ws/ws.go`, `ws_test.go`
**Interfaces:** Produces `Dial(ctx) (io.ReadWriteCloser, error)` (wss://web.whatsapp.com/ws/chat, Origin/headers da Baileys). Adapta msgs WS binárias para o stream de frames do `wire.Conn`.
- [ ] Teste unit: o adapter ReadWriteCloser sobre um WS fake entrega bytes corretamente (fragmentação: 1 msg WS pode conter múltiplos frames; e 1 frame pode vir partido — bufferizar).
- [ ] Implementar Dial + adapter.
- [ ] Commit.

### Task 5: client — orquestração do pareamento
**Files:** Create `internal/client/client.go`, `client/pairing.go`, `client_test.go`
**Interfaces:** Consumes wire, keys, store, ws, waproto. Produces `type Client`, `Connect(ctx)`, canal de eventos (`QR`, `PairSuccess`, `LoggedIn`, `Disconnected`).
- [ ] Teste (replay, offline): Connect sobre transporte de replay do trace chega a emitir `QR` montado == `qr.json` e responde o `pair-device` iq.
- [ ] Montagem do QR (ref + noisePubB64 + identityPubB64 + advSecretB64) validada contra fixture.
- [ ] Handler de `pair-success`: HMAC com advSecret + assinatura — ESCREVER, com teste `t.Skip` aguardando `creds.json` (chip).
- [ ] Resume/login com LoginPayload — ESCREVER, teste `t.Skip`.
- [ ] Commit.

### Task 6: integração ao vivo (sem número)
**Files:** Create `internal/client/live_test.go` (`//go:build live`)
- [ ] Teste `//go:build live`: `Client.Connect` contra o WhatsApp REAL emite um QR válido e responde o pair-device sem erro. NÃO pareia. Rodado manualmente com `go test -tags live`.
- [ ] Documentar no README como rodar.
- [ ] Commit.

## Definition of Done (fase offline)
- `go test ./...` verde (unit + #1).
- ClientPayload registration marshal/unmarshal bate com fixture (campos determinísticos).
- QR montado == fixture.
- `go test -tags live ./internal/client/` emite QR conectando ao WhatsApp real (manual, sem número).
- Creds round-trip no SQLite.
- pair-success/login escritos, testes gated `t.Skip` aguardando chip.
