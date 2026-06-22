# Spec — Capture Harness + Wire Layer (#0 + #1)

**Data:** 2026-06-22
**Projeto:** `wa-go` — reimplementação do protocolo WhatsApp Web (estilo Baileys) em Go, do zero.
**Sub-projetos cobertos por este spec:** #0 Capture Harness e #1 Wire Layer.
**Status:** aprovado para implementação (design de alto nível aprovado pelo usuário em 2026-06-22).

---

## 1. Contexto e objetivo macro

Reescrever, do zero em Go, o protocolo WhatsApp Web multi-device hoje fornecido pela
Baileys (e embutido no Evolution API). Motivação: **controle total** —
fingerprint de device por instância, cadência humana de envio (anti-ban), multi-conta
leve (centenas de sessões por processo) e acesso ao fio (frames brutos). O objetivo
final é aposentar o Evolution com um serviço Go compatível com o contrato que
Chatwoot e workers já consomem.

O projeto inteiro está decomposto em 9 sub-projetos (#0–#8). **Este spec detalha apenas
#0 e #1.** Os demais são roadmap e ganharão specs próprios.

### Decomposição completa (roadmap, não escopo deste spec)

| # | Sub-projeto | Depende de |
|---|---|---|
| 0 | Capture Harness | — |
| 1 | Wire Layer (framing + binary-node codec + Noise XX) | 0 |
| 2 | Pairing/Auth (multi-device, QR + código, storage de chaves) | 1 |
| 3 | Signal/E2E (X3DH, Double Ratchet, prekeys, sender keys) | 2 |
| 4 | Messaging (texto, recibos, presença, mídia) | 3 |
| 5 | App-state sync (LTHash, mutations) | 3 |
| 6 | Control layer (DeviceProfile, SendPacer, hooks de frame) | 4 |
| 7 | Instance manager (multi-sessão, persistência, reconexão) | 4 |
| 8 | Evolution-compat (HTTP/WS espelhando o contrato atual) | 6,7 |

---

## 2. Princípio de de-risco: golden traces (teste diferencial)

A premissa que torna o "do zero" viável: **não adivinhar o protocolo — comparar com a
Baileys real, byte a byte.** Antes de tocar a rede com código Go, gravamos o
comportamento de uma Baileys real instrumentada e o transformamos em *fixtures*. Cada
camada Go é validada contra esses traces.

Consequência de ordem: **#0 vem antes de #1.** Sem traces, #1 é chute.

---

## 3. Sub-projeto #0 — Capture Harness

### 3.1 Propósito
Produzir um conjunto de *golden traces* determinísticos a partir de uma sessão Baileys
real, cobrindo: handshake Noise completo, framing e um vocabulário representativo de
binary nodes (ex.: `<iq>`, `<stream:features>`, `<success>`, `<failure>`, presence,
`<receipt>`). Esses traces são a fonte da verdade para os testes de #1.

### 3.2 Forma
Um pequeno projeto Node.js separado (`harness/`), usando a Baileys real como dependência,
com *monkey-patching*/hooks nos pontos de I/O e cripto para despejar:

- **`frames_raw.jsonl`** — cada frame TCP (após o framing de 3 bytes de tamanho),
  em hex, com direção (`in`/`out`) e timestamp relativo.
- **`noise.json`** — material do handshake Noise exposto pela própria Baileys:
  chave estática local (priv/pub), ephemeral, chaves derivadas (write/read), e o
  payload do `ClientHello`/`ServerHello`/`ClientFinish`. Necessário para reproduzir
  o handshake deterministicamente nos testes (sem isso o Noise é não-determinístico
  por causa do ephemeral aleatório).
- **`nodes.jsonl`** — cada binary node **decodificado** (a árvore `{tag, attrs, content}`)
  emparelhado com os bytes **codificados** correspondentes (pré-criptografia Noise).
  Este é o fixture central do codec de #1.

### 3.3 Como capturar sem alterar a Baileys publicada
- Importar a Baileys e interceptar nas fronteiras:
  - o socket (`ws`/noise socket) para `frames_raw`;
  - a função de encode/decode de binary node (exportada internamente) para `nodes`;
  - o objeto de Noise handshake para `noise.json`.
- Se algum ponto não for exportado, usar um *patch* local mínimo documentado no
  README do harness (sem fork publicável; é ferramenta de teste).

### 3.4 Determinismo
- Fixar seeds onde a Baileys permitir; onde não permitir (ephemeral), **capturar** o
  valor usado e gravá-lo, para que o teste Go injete o mesmo ephemeral e reproduza o
  handshake idêntico.
- Cada trace é uma pasta versionada em `testdata/traces/<cenário>/`.

### 3.5 Cenários mínimos a capturar
1. `connect_pair` — primeira conexão + geração de QR (até `<pair-device>`).
   *(O pareamento completo é #2; aqui só queremos os frames de handshake e os
   primeiros nodes.)*
2. `connect_resume` — reconexão de sessão já pareada até `<success>`.
3. `keepalive` — ping/pong e presença em sessão estável.

> Pré-condição para os cenários 2 e 3: existe uma sessão Baileys já pareada num número
> sacrificial isolado. A separação desse chip do hub do ThinkPad é tarefa de setup,
> não-código, e roda antes do cenário 2.

### 3.6 Critério de pronto (#0)
- `testdata/traces/connect_pair/` existe com `frames_raw.jsonl`, `noise.json`,
  `nodes.jsonl` não-vazios e um `manifest.json` descrevendo o cenário.
- Um script `harness/verify.mjs` relê os traces e re-decodifica os nodes para
  confirmar consistência interna (round-trip do próprio harness).

---

## 4. Sub-projeto #1 — Wire Layer

### 4.1 Propósito
A camada Go mais baixa: abrir o socket, executar o handshake Noise XX, e
codificar/decodificar binary nodes. Nada de Signal/mensagens ainda — só pôr no fio um
node arbitrário e ler a resposta decodificada.

### 4.2 Pacotes Go
```
wa-go/
  internal/wire/
    framing.go      // moldura de 3 bytes (length-prefixed) + leitura/escrita
    noise.go        // handshake Noise_XX_25519_AESGCM_SHA256 + cifra de tráfego
    token.go        // dicionário de tokens single/double-byte do WA Web
    codec.go        // encode/decode de binary node (árvore <-> bytes)
    node.go         // type Node struct { Tag string; Attrs map[string]string; Content any }
    conn.go         // Conn: junta framing+noise+codec; SendNode/ReadNode
  internal/wire/*_test.go   // testes diferenciais contra testdata/traces
```

### 4.3 Detalhes técnicos
- **Framing:** cada frame é precedido de 3 bytes big-endian com o tamanho. O primeiro
  contato envia o header de rota (`WA` + versão) antes do ClientHello — capturar o
  formato exato do trace, não assumir.
- **Noise:** padrão `Noise_XX_25519_AESGCM_SHA256`. Implementar:
  - geração/uso de chave estática (Curve25519) e ephemeral;
  - mixing de hash/chave (HKDF-SHA256), `EncryptWithAd`/`DecryptWithAd` (AES-GCM);
  - os três passos XX (ClientHello → ServerHello → ClientFinish), com o
    `ClientPayload` (protobuf) injetado no finish.
  - **Validação:** injetar o ephemeral do `noise.json` e exigir que os bytes
    produzidos batam com `frames_raw` do trace `connect_pair`.
- **Token dictionary:** portar a tabela de tokens single-byte e os blocos double-byte
  do WA Web (constantes públicas). Validar via round-trip nos `nodes.jsonl`.
- **Codec:** decodificar todo node de `nodes.jsonl` e exigir igualdade estrutural;
  re-encodar e exigir bytes idênticos (round-trip). Tipos de conteúdo: lista de nodes,
  string, bytes, e atributos com valores tokenizados ou JID.

### 4.4 Stack/deps
- `golang.org/x/crypto/curve25519`, `.../hkdf`, `crypto/aes`+`cipher` (GCM).
- `google.golang.org/protobuf` para o `ClientPayload` (usar os `.proto` públicos do WA;
  gerar o Go em `internal/waproto/`).
- Go 1.22+ (instalar — ausente nesta workstation; é o primeiro passo do plano).

### 4.5 Critério de pronto (#1)
- `go test ./internal/wire/...` verde, incluindo:
  - handshake reproduz `connect_pair` byte-a-byte (com ephemeral injetado);
  - round-trip de **100%** dos nodes em `nodes.jsonl` (decode→encode idêntico);
  - decode de todos os frames `in` do trace sem erro.
- Um exemplo executável `cmd/wiredump/` que conecta a um endpoint de loopback que
  reproduz o trace e imprime os nodes decodificados (não conecta ao WA real ainda).

### 4.6 Fora de escopo (#1)
Pareamento, Signal, mensagens, mídia, app-state, multi-instância, API HTTP. Conexão
ao WhatsApp **real** só após #1 verde nos traces e com chip sacrificial.

---

## 5. Estratégia de teste (ambos)
- TDD por camada (skill `test-driven-development`).
- Vetores oficiais do Signal entram só em #3; aqui o oráculo são os golden traces.
- Nenhum número da operação é usado. Chip sacrificial isolado para captura ao vivo.

## 6. Riscos
- **Tokens/handshake mudam com versão do WA:** mitigado por traces versionados e por
  capturar (não assumir) o header de rota e a versão de cliente.
- **Pontos internos da Baileys não exportados:** mitigado por patch local documentado.
- **Ban do chip de captura:** usar só chip sacrificial; cenário `connect_pair` nem
  precisa de número pareado.

## 7. Pré-requisitos antes de codar
1. Instalar Go 1.22+ na workstation.
2. Node + Baileys no `harness/`.
3. Separar 1 chip sacrificial do hub do ThinkPad (para cenários 2 e 3).
