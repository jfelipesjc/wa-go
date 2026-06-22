# Spec — Pairing/Auth (#2)

**Data:** 2026-06-22
**Projeto:** `wa-go`
**Sub-projeto:** #2 Pairing/Auth. Depende de #1 (wire layer, completo).
**Status:** aprovado (abordagem "spec + partes offline" aprovada pelo usuário 2026-06-22;
chip sacrificial sendo provisionado em paralelo).

---

## 1. Objetivo
Levar a wire layer (#1) de "decifra frames" até "uma sessão autenticada": gerar a
identidade do device, montar o `ClientPayload`, dialar o WebSocket real do WhatsApp,
executar o handshake e o fluxo de pareamento multi-device (QR), persistir as credenciais,
e reconectar como sessão logada.

## 2. Fronteira offline vs. gated-no-chip
A maior parte é construível e testável SEM número:

| Componente | Testável offline? |
|---|---|
| Geração de identidade (creds: noise static, identity, regId, advSecret, prekeys) | ✅ unit |
| `ClientPayload` protobuf (registration + login) | ✅ contra fixture capturado |
| Montagem da string de QR | ✅ contra QR capturado |
| Storage SQLite (creds + interface signal store) | ✅ unit |
| Transporte WebSocket real (`wss://web.whatsapp.com/ws/chat`) | ✅ **integração ao vivo até o QR — não precisa de número** |
| Verificação do device-identity HMAC + assinatura (pair-success) | ⛔ precisa do scan (chip) |
| Resume/login + `<success>` | ⛔ precisa de sessão pareada (chip) |

O código dos dois últimos é escrito agora (com unit tests sobre vetores construídos),
mas a validação ponta-a-ponta fica para quando o chip estiver pronto.

## 3. Extensão do harness (#0bis) — novos fixtures
Estender `harness/capture.mjs` (ou um novo `capture_pair.mjs`) para gravar, no cenário
`connect_pair`, fixtures que faltam para validar #2 offline:
- **`client_payload.json`**: o `ClientPayload` (registration) que a Baileys monta, em
  protobuf hex + a árvore de campos decodificada. Hook no ponto onde a Baileys gera o
  nó de registro (`generateRegistrationNode`/`generateLoginNode` em `Utils`/`Socket`).
- **`qr.json`**: a string de QR emitida (`connection.update.qr`) e suas 4 partes
  (`ref`, `noiseKeyB64`, `identityKeyB64`, `advSecretB64`), para validar a montagem.
- **`creds.json`** (do chip sacrificial, MAIS TARDE): após um pareamento real, as creds
  resultantes para um fixture de login. NÃO commitar (chip, mas ainda assim sensível) —
  manter local e gitignored.

## 4. Componentes Go

### 4.1 `internal/keys/` — identidade
- `type KeyPair struct { Priv, Pub [32]byte }` (Curve25519); `GenKeyPair()`.
- Signal identity: par de chaves de identidade (Curve25519 com assinaturas XEdDSA — a
  assinatura em si é #3; aqui só geramos e guardamos o par).
- `RegistrationID() uint32` (14 bits, como Signal).
- `advSecretKey [32]byte` aleatório (HMAC do pareamento).
- Signed pre-key + one-time pre-keys: a GERAÇÃO entra aqui; o USO criptográfico é #3.

### 4.2 `internal/store/` — persistência
- Interface `Store` com `LoadCreds`/`SaveCreds` e os métodos de signal store (prekeys,
  sessions, identities, senderkeys) **declarados** (impl real no #3; stub que persiste
  blobs por enquanto).
- Impl `sqliteStore` (database/sql + modernc.org/sqlite, CGo-free). Schema: tabela
  `creds` (singleton por device), tabelas vazias preparadas para signal store.
- `type Creds struct {...}` serializável (JSON no SQLite): noise key, identity key,
  regId, advSecret, signed prekey, e — após pareamento — `me` (JID), `account`
  (ADVSignedDeviceIdentity), `platform`, `pushName`.

### 4.3 `internal/waproto/` — protobuf
- Gerar Go a partir do subset necessário de `harness/node_modules/.../WAProto/*.proto`:
  `ClientPayload` (+ `UserAgent`, `WebInfo`, `DevicePairingRegistrationData`,
  `CompanionRegData`, `CompanionPropsPlatform`) e `ADVSignedDeviceIdentity` /
  `ADVDeviceIdentity` / `ADVSignedDeviceIdentityHMAC` para o pair-success.
- Usar `protoc` se disponível; se não, instalar via apt/go install. (Diferente do #1,
  aqui o payload é grande demais para hand-encode.)
- `func RegistrationPayload(creds) *ClientPayload` e `func LoginPayload(creds) *ClientPayload`.
  Validar: `proto.Marshal(RegistrationPayload(fixtureCreds))` reproduz o
  `client_payload.json` capturado (os campos determinísticos batem; campos com
  aleatoriedade são injetados do fixture).

### 4.4 `internal/ws/` — transporte real
- `func Dial(ctx) (io.ReadWriteCloser, error)`: WebSocket para
  `wss://web.whatsapp.com/ws/chat`, `Origin: https://web.whatsapp.com`, subprotocolos/
  headers conforme a Baileys (`Socket/socket.js`). Usar `github.com/coder/websocket` ou
  `gorilla/websocket`. O `io.ReadWriteCloser` adapta mensagens binárias do WS para o
  `Conn` do #1 (cada mensagem WS = um ou mais frames; tratar fragmentação).

### 4.5 `internal/client/` — orquestração do pareamento
- `type Client struct { conn *wire.Conn; store store.Store; ... }`.
- `Connect(ctx)`: Dial → `wire.Conn.Handshake` (com `RegistrationPayload`) → loop de leitura.
- Emite eventos: `QR(string)`, `PairSuccess`, `LoggedIn`, `Disconnected(reason)`.
- Handler do `pair-device` iq → monta QR (ref + 3 chaves b64), emite `QR`, responde o iq.
- Handler do `pair-success` iq → verifica HMAC com advSecret, valida a assinatura do
  device identity, monta a resposta `pair-device-sign`, persiste creds, fecha p/ relogar.
  *(escrito agora; validado com o chip)*
- Reconexão com `LoginPayload` → trata `<success>`/`<failure>`.

## 5. Estratégia de teste
- Unit: geração de chaves (tamanhos, regId range), serialização de creds (round-trip
  SQLite), montagem de QR (== fixture), `ClientPayload` marshal (== fixture).
- **Integração ao vivo (sem número):** `Client.Connect` contra o WhatsApp REAL deve
  chegar a emitir um `QR` válido e responder o `pair-device` iq sem erro. Marcado com
  build tag `//go:build live` para não rodar no CI normal. Não pareia, não usa número.
- Gated-no-chip (marcados `t.Skip` até o fixture `creds.json` existir): pair-success HMAC,
  resume/login.

## 6. Riscos
- **WS headers/version desatualizam:** mitigado capturando da Baileys atual e mantendo a
  versão do WA em um único lugar (`waproto`/config).
- **protoc ausente:** instalar; é dependência só de build do `waproto`.
- **Ban no chip:** o teste ao vivo offline NÃO pareia (não queima número). O pareamento
  real só com chip sacrificial isolado.

## 7. Pré-requisitos
1. `protoc` + `protoc-gen-go` (ou alternativa) para `internal/waproto/`.
2. Fixtures novos do harness (`client_payload.json`, `qr.json`).
3. Chip sacrificial (só para os testes gated; não bloqueia o resto).

## 8. Definition of Done (#2, fase offline)
- `go test ./...` verde (unit + os de #1).
- `ClientPayload` (registration) marshal reproduz o fixture nos campos determinísticos.
- Montagem de QR == fixture.
- `go test -tags live ./internal/client/` chega a emitir um QR conectando ao WhatsApp
  real (rodado manualmente, sem número).
- Creds persistem/recarregam do SQLite.
- Código de pair-success/login escrito, com testes `t.Skip`-ados aguardando o chip.
