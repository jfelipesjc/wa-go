# Spec — Signal/E2E (#3)

**Data:** 2026-06-22
**Projeto:** `wa-go`
**Sub-projeto:** #3 Signal/E2E. Depende de #1 (wire) e #2 (pairing/auth, sessão logada).
**Status:** aprovado (usuário pediu para seguir; abordagem espelha #1/#2).

---

## 1. Objetivo
Implementar o protocolo Signal (1:1) do zero em Go e usá-lo para **receber e decifrar
uma mensagem de texto real** enviada de outro telefone para a conta do chip pareado.
Isso prova o stack E2E ponta a ponta.

## 2. Escopo (focado) e fora de escopo
**Dentro:**
- Double Ratchet (root key, chain keys, message keys, DH ratchet).
- X3DH lado **responder** (processar um PreKeySignalMessage que chega) e lado
  **initiator** (construir sessão a partir de um prekey bundle) — initiator é necessário
  para o teste? Não para *receber*; mas implementamos os dois pois a cripto é simétrica e
  os vetores cobrem ambos. O *uso* de envio fica para #4.
- Prekeys: geração de one-time prekeys + signed prekey (parte em `keys`, completar aqui),
  e **upload** ao servidor (`<iq set>` set-prekeys) após o login.
- Especificidades WhatsApp: prefixo `0x05` nas pubkeys, versão do protocolo libsignal,
  padding da mensagem (WAMessage pad), `WAProto.Message`.
- Store real para Signal: sessions, prekeys, signed prekeys, identities (implementa a
  `SignalStore` que ficou stub no #2).
- Caminho de **recebimento**: tratar `<message>` com `<enc type="pkmsg"|"msg">`, decifrar,
  remover padding, parsear `WAProto.Message`, emitir evento com o texto, e mandar o
  **receipt** de volta.

**Fora (vai para #4):** envio de mensagens, sender keys / grupos, mídia, retries de
sessão, history sync.

## 3. De-risco (golden vectors + vetores oficiais + live)
1. **Golden vectors da libsignal da Baileys (harness Node):** a Baileys usa
   `libsignal` (em `harness/node_modules/@whiskeysockets/baileys/node_modules/libsignal`
   ou similar — confirmar). Script que cria duas identidades (alice/bob), gera prekey
   bundle do bob, alice estabelece sessão e cifra; bob processa o PreKeySignalMessage e
   decifra; trocam mais algumas mensagens (exercita a cadeia simétrica e o DH ratchet).
   Dump em `testdata/signal/session_ab.json`: todas as chaves (identity, prekeys, base,
   ephemeral), os ciphertexts (pkmsg + msgs) e os plaintexts. Os testes Go exigem:
   decifrar os ciphertexts da Baileys e (onde determinístico) reproduzir os ciphertexts.
2. **Vetores oficiais do Signal** (curve25519/ratchet known-answer) quando aplicável.
3. **Live:** receber uma mensagem real (o usuário manda do celular para o chip).

## 4. Componentes Go

### 4.1 `internal/signal/` — protocolo
- `ratchet.go`: root key + chain key + message key derivation (HKDF com os infos do
  WhatsApp/libsignal: "WhisperRatchet", "WhisperMessageKeys", "WhisperText"), DH ratchet.
- `session.go`: `SessionRecord`/`SessionState` (serializável p/ o store, espelhando o
  formato do libsignal o suficiente p/ persistir), `SessionCipher` com
  `Encrypt(plaintext) (CiphertextMessage, err)` e `Decrypt(msg) (plaintext, err)`.
- `x3dh.go`: `ProcessPreKeyMessage` (responder) e `ProcessBundle` (initiator) — derivação
  da root key inicial a partir dos DHs do X3DH (IK/EK/SPK/OPK).
- `message.go`: serialização do `SignalMessage` e `PreKeySignalMessage` (protobuf do
  libsignal — `WhisperTextProtocol.proto`; gerar ou hand-encode) + o byte de versão e o
  MAC truncado (HMAC-SHA256, 8 bytes) que o libsignal anexa.
- `numeric.go`/helpers: prefixo 0x05, curve helpers (reusar `internal/keys`).
- Padding: `padMessage`/`unpadMessage` (WhatsApp usa pad aleatório 1..16 no fim).

### 4.2 `internal/store` — SignalStore real
Implementar de verdade os métodos que ficaram stub: `LoadSession/StoreSession` (por
endereço `jid.device`), `LoadPreKey/RemovePreKey`, `LoadSignedPreKey`,
`LoadIdentity/SaveIdentity`, `Load/StorePreKeys` (lote). Persistência no `signal_kv` por
namespace, valores serializados.

### 4.3 prekeys + upload (`internal/client`)
- `keys`: `GenPreKeys(start, count)` (one-time prekeys), já temos signed prekey.
- `client`: após `<success>`, montar e enviar o nó de upload de prekeys
  (`<iq to=s.whatsapp.net type=set xmlns=encrypt><registration/><type/><identity/>
  <list>…<key><id/><value/></key>…</list><skey>…</skey></iq>` — formato exato conforme
  Baileys `Socket/messages-send`/`Utils` `getNextPreKeysNode`). Persistir os prekeys
  enviados no store.

### 4.4 recebimento (`internal/client`)
- Após login: enviar presença/`<iq>` inicial que o WhatsApp espera (mínimo p/ receber).
- Handler de `<message>`: extrair `from`, `<enc type>`, decifrar via SessionCipher
  (pkmsg → ProcessPreKeyMessage; msg → Decrypt), unpad, `WAProto.Message.Unmarshal`,
  extrair `conversation`/`extendedTextMessage.text`. Emitir `MessageEvent{From, Text}`.
- Enviar o `<receipt>` de recebimento (e o ack do `<message>`), senão o servidor reenvia.

## 5. Testes
- Unit/diferencial: decifrar e (onde determinístico) reproduzir os ciphertexts do
  `session_ab.json`; KDFs contra vetores; pad/unpad round-trip; serialização de sessão
  round-trip no store.
- Live (`//go:build live` + manual): logar com as creds do chip (#2), uploadar prekeys,
  aguardar uma mensagem de texto enviada pelo usuário, decifrar e imprimir o texto.

## 6. Riscos
- **Formato de sessão do libsignal:** não precisamos ser byte-compatíveis com o disco do
  libsignal — só internamente consistentes e corretos no fio. Validação é por
  decifrar/reproduzir ciphertexts reais, não por igualar o blob de sessão.
- **Versão do protocolo / MAC:** o libsignal prefixa version byte e anexa MAC de 8 bytes
  sobre (versão+serialized) com a macKey derivada; replicar exatamente (golden vectors
  pegam erro).
- **Upload de prekeys errado → não recebe:** validar o nó contra o que a Baileys gera.
- **Ban do chip:** já pareado; receber mensagem é tráfego normal.

## 7. Pré-requisitos
- Sessão pareada do #2 (temos: chip 5512991272281).
- Harness Node com a libsignal da Baileys para gerar `session_ab.json`.

## 8. Definition of Done (#3)
- `go test ./...` verde, incluindo os vetores diferenciais do Signal (decifrar
  ciphertexts da Baileys + reproduzir os determinísticos).
- Store Signal real com round-trip de sessão/prekeys.
- Upload de prekeys validado contra o formato da Baileys.
- **Live:** receber e decifrar uma mensagem de texto real no chip (texto correto
  impresso), enviar o receipt. Marcado `//go:build live`/manual.
