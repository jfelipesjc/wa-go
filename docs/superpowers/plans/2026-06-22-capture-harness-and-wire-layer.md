# Capture Harness + Wire Layer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Capturar golden traces de uma Baileys real e implementar a wire layer Go (framing + Noise XX + binary-node codec) validada byte-a-byte contra esses traces.

**Architecture:** Um harness Node (`harness/`) instrumenta a Baileys real e despeja frames brutos, material do handshake Noise e binary nodes (codificados+decodificados) em `testdata/traces/`. A wire layer Go (`internal/wire/`) reproduz handshake e codec, com testes diferenciais contra os fixtures. Nenhuma conexão ao WhatsApp real pelo código Go neste plano.

**Tech Stack:** Go 1.22 (`x/crypto`, `google.golang.org/protobuf`), Node 22 + Baileys, SQLite só a partir do #2 (não aqui).

## Global Constraints

- Go 1.22+ (instalado: go1.22.2).
- Nenhum número da operação. Captura ao vivo só em chip sacrificial; cenário `connect_pair` não precisa de número pareado.
- Não commitar segredos/chaves reais; traces de chip sacrificial podem conter material de chave — guardar em `testdata/traces/` que é local e não-sensível por ser chip descartável, mas NUNCA traces de número de produção.
- TDD: teste falhando antes de implementar, commits frequentes.
- Validação do codec exige round-trip 100% dos nodes do trace; handshake reproduz `connect_pair` byte-a-byte com ephemeral injetado.

---

### Task 1: Scaffold do módulo Go e do harness

**Files:**
- Create: `go.mod`, `harness/package.json`, `harness/README.md`
- Create: `internal/wire/node.go` (tipo `Node` vazio + doc)

**Interfaces:**
- Produces: `module github.com/felipeleal/wa-go`; tipo `wire.Node struct { Tag string; Attrs map[string]string; Content any }`.

- [ ] **Step 1:** `go mod init github.com/felipeleal/wa-go`
- [ ] **Step 2:** Criar `harness/` com `npm init -y` e instalar `@whiskeysockets/baileys` + `qrcode-terminal`.
- [ ] **Step 3:** Definir `wire.Node` em `node.go`.
- [ ] **Step 4:** `go build ./...` passa.
- [ ] **Step 5:** Commit.

---

### Task 2: Harness — captura de `connect_pair` (#0)

**Files:**
- Create: `harness/capture.mjs`, `harness/verify.mjs`
- Create (gerado): `testdata/traces/connect_pair/{frames_raw.jsonl,noise.json,nodes.jsonl,manifest.json}`

**Interfaces:**
- Produces: fixtures em `testdata/traces/connect_pair/` consumidos pelos testes Go.

- [ ] **Step 1:** `capture.mjs` inicia socket Baileys (`printQRInTerminal:false`, `browser` fixo p/ determinismo), faz hook no noise socket pra gravar cada frame (`{dir,t,hex}`) em `frames_raw.jsonl`.
- [ ] **Step 2:** Hook no encode/decode de binary node → grava `{tree, encoded_hex}` em `nodes.jsonl`.
- [ ] **Step 3:** Extrair material Noise (static priv/pub, ephemeral, chaves derivadas, payloads) → `noise.json`.
- [ ] **Step 4:** Rodar até receber o node de pareamento/QR, depois fechar; escrever `manifest.json` (cenário, versão Baileys, timestamp).
- [ ] **Step 5:** `verify.mjs` relê e re-decodifica nodes (round-trip do harness) — consistência interna OK.
- [ ] **Step 6:** Rodar a captura real (WA acessível na 443; não precisa de número). Confirmar arquivos não-vazios.
- [ ] **Step 7:** Commit (traces incluídos).

---

### Task 3: Wire — framing length-prefixed

**Files:**
- Create: `internal/wire/framing.go`, `internal/wire/framing_test.go`

**Interfaces:**
- Produces: `func writeFrame(w io.Writer, payload []byte) error`; `func readFrame(r io.Reader) ([]byte, error)` (3 bytes big-endian de tamanho).

- [ ] **Step 1:** Teste falhando: round-trip de payload arbitrário via `writeFrame`/`readFrame`; e decode dos tamanhos dos frames de `frames_raw.jsonl`.
- [ ] **Step 2:** Rodar, falha (não definido).
- [ ] **Step 3:** Implementar framing (3-byte length, validar limite).
- [ ] **Step 4:** Testes verdes.
- [ ] **Step 5:** Commit.

---

### Task 4: Wire — dicionário de tokens

**Files:**
- Create: `internal/wire/token.go`, `internal/wire/token_test.go`

**Interfaces:**
- Produces: `singleByteTokens []string`, `doubleByteTokens [][]string`, helpers `tokenIndex(s) (int,bool)`.

- [ ] **Step 1:** Teste falhando: tokens conhecidos mapeiam pro índice esperado (amostras dos nodes do trace).
- [ ] **Step 2:** Falha.
- [ ] **Step 3:** Portar tabelas de tokens single/double-byte do WA Web.
- [ ] **Step 4:** Verde.
- [ ] **Step 5:** Commit.

---

### Task 5: Wire — codec de binary node (decode)

**Files:**
- Create: `internal/wire/codec.go`, `internal/wire/codec_decode_test.go`

**Interfaces:**
- Consumes: `Node`, tokens.
- Produces: `func DecodeNode(b []byte) (Node, error)`.

- [ ] **Step 1:** Teste falhando: decodificar TODOS os `encoded_hex` de `nodes.jsonl` e comparar com `tree` esperado.
- [ ] **Step 2:** Falha.
- [ ] **Step 3:** Implementar decode (listas, strings, bytes, JID, nibble/hex packing, tokens).
- [ ] **Step 4:** 100% dos nodes decodificam igual.
- [ ] **Step 5:** Commit.

---

### Task 6: Wire — codec de binary node (encode) + round-trip

**Files:**
- Modify: `internal/wire/codec.go`
- Create: `internal/wire/codec_encode_test.go`

**Interfaces:**
- Produces: `func EncodeNode(n Node) ([]byte, error)`.

- [ ] **Step 1:** Teste falhando: `EncodeNode(tree)` produz `encoded_hex` idêntico para todos os nodes do trace.
- [ ] **Step 2:** Falha.
- [ ] **Step 3:** Implementar encode espelhando o decode.
- [ ] **Step 4:** Round-trip 100%.
- [ ] **Step 5:** Commit.

---

### Task 7: Wire — handshake Noise XX

**Files:**
- Create: `internal/wire/noise.go`, `internal/wire/noise_test.go`
- Create (gerado): `internal/waproto/` (ClientPayload a partir dos .proto públicos)

**Interfaces:**
- Consumes: framing, `noise.json`.
- Produces: `type Noise struct{...}` com `WriteMessage`/`ReadMessage` e `Handshake(conn, clientPayload []byte, ephemeralPriv []byte) (cipher, error)`.

- [ ] **Step 1:** Gerar Go protobuf do `ClientPayload` (subset necessário).
- [ ] **Step 2:** Teste falhando: injetando ephemeral de `noise.json`, o ClientHello+ClientFinish produzidos batem byte-a-byte com `frames_raw` (frames `out` do handshake).
- [ ] **Step 3:** Falha.
- [ ] **Step 4:** Implementar Noise XX (curve25519, HKDF-SHA256, AES-GCM, mixHash/mixKey, 3 passos).
- [ ] **Step 5:** Handshake reproduz o trace; decifrar o ServerHello do trace OK.
- [ ] **Step 6:** Commit.

---

### Task 8: Wire — Conn integrando tudo + cmd/wiredump

**Files:**
- Create: `internal/wire/conn.go`, `internal/wire/conn_test.go`, `cmd/wiredump/main.go`

**Interfaces:**
- Produces: `type Conn` com `SendNode(Node) error`, `ReadNode() (Node, error)`; `func Dial(...)` (não usado contra WA real neste plano — testado contra um replay loopback dos traces).

- [ ] **Step 1:** Teste falhando: um servidor loopback que reproduz `frames_raw` do trace; `Conn` faz handshake + lê os primeiros nodes decodificados corretamente.
- [ ] **Step 2:** Falha.
- [ ] **Step 3:** Implementar `Conn` (framing+noise+codec) e o loopback de teste.
- [ ] **Step 4:** Verde. `cmd/wiredump` imprime nodes do replay.
- [ ] **Step 5:** Commit.

---

## Definition of Done (plano)
- `go test ./...` verde.
- Handshake reproduz `connect_pair` byte-a-byte (ephemeral injetado).
- Round-trip 100% dos nodes do trace.
- `cmd/wiredump` decodifica o replay do trace sem erro.
- Nenhuma conexão ao WhatsApp real feita pelo código Go.
