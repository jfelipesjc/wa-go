# wa-go

Reimplementação do protocolo WhatsApp Web (estilo Baileys) em Go, **do zero** — sem
whatsmeow. Objetivo: controle total (fingerprint de device, cadência humana de envio,
multi-conta leve, acesso a frames brutos) e, no fim, aposentar o Evolution API.

Decomposto em 9 sub-projetos. Specs e planos em `docs/superpowers/`.

## Status

| # | Sub-projeto | Status |
|---|---|---|
| 0 | Capture harness (golden traces da Baileys real) | ✅ feito |
| 1 | Wire layer (framing + Noise XX + binary-node codec) | ✅ feito |
| 2 | Pairing/Auth (multi-device, QR + código, storage) | ⬜ próximo |
| 3 | Signal/E2E (X3DH, Double Ratchet, sender keys) | ⬜ |
| 4 | Messaging (texto, recibos, presença, mídia) | ⬜ |
| 5 | App-state sync (LTHash) | ⬜ |
| 6 | Control layer (fingerprint, SendPacer, hooks) | ⬜ |
| 7 | Instance manager (multi-sessão) | ⬜ |
| 8 | Evolution-compat (HTTP/WS) | ⬜ |

### #0 + #1 entregues

- **Harness** (`harness/`): instrumenta a Baileys real, captura `connect_pair` (handshake
  Noise + ephemeral + nodes) e gera bateria sintética de 19 nodes cobrindo todos os
  caminhos do codec. Traces em `testdata/traces/`.
- **Wire** (`internal/wire/`): framing 3-byte BE, dicionário de tokens (236 single +
  1024 double), codec `DecodeNode`/`EncodeNode` (round-trip 19/19 estrutural), handshake
  `Noise_XX_25519_AESGCM_SHA256`, e `Conn` (SendNode/ReadNode).
- **Validação decisiva:** o handshake roda contra o trace real e decifra o frame
  `pair-device` (698 B) do WhatsApp até decodificar o node idêntico ao capturado.
  `go test ./...` = 26/26 verde.

## Rodar

```sh
export PATH=$PATH:/usr/local/go/bin
go test ./...            # 26 testes
go run ./cmd/wiredump    # replay do trace, decodifica o pair-device (sem rede)
```

## Recapturar traces (opcional)

Requer Node + `cd harness && npm i`. `node harness/capture.mjs` conecta ao WhatsApp real
até o QR (não precisa de número) e regrava `testdata/traces/connect_pair/`.
`node harness/gen_codec_battery.mjs` regenera a bateria do codec (offline).

> ⚠️ Conexão ao WhatsApp **real** pelo código Go só a partir do #2, e só com chip
> sacrificial isolado. Ver `docs/superpowers/specs/` e `docs/superpowers/decisions.md`.
