# Spec — Messaging (#4)

**Data:** 2026-06-22
**Projeto:** `wa-go`
**Sub-projeto:** #4 Messaging. Depende de #1/#2/#3.
**Status:** em progresso (usuário pediu para seguir).

## 1. Objetivo
Tornar o device um participante de verdade do tráfego de mensagens: (a) **entrar no
fanout** para RECEBER mensagens novas de remetentes externos em tempo real, e (b)
**ENVIAR** mensagens 1:1. Mídia e grupos (sender keys) ficam para depois.

## 2. Diagnóstico que motiva o #4 (confirmado 2026-06-22)
Com o device logado e a sessão saudável (keepalive ok), mensagens NOVAS não chegam —
nem de remetente externo (testado: ADM 5512992020865 → chip via Evolution) nem do próprio
chip. Só o **lote de history-sync** do link inicial chegou (e foi decifrado com sucesso).
Conclusão: a cripto está provada (#3); falta a participação no **fanout / device-list /
app-state**. Recebemos `<ib>`, `<notification type=account_sync|server_sync>` mas NÃO os
ACKamos, e não enviamos presença.

## 3. Teste repetível (harness Evolution)
Instância **ADM** do Evolution (`https://whats001.digitalsjc.com`, apikey global
`0ea941...`, número 5512992020865) envia texto pro chip (5512991272281):
`POST /message/sendText/ADM {number,text}`. O wa-go `-listen` deve receber e decifrar.

## 4. Incrementos (ordem)
1. **ACK + presença** (hipótese barata): ACKar todo `<message>`/`<notification>`/`<ib>`
   (`<ack id class to [from][participant][type]>` como Baileys), e enviar
   `<presence type="available">` pós-login. Re-testar fanout via Evolution.
2. **Device-list / usync**: se (1) não bastar, garantir que o device está anunciado e que
   senders conseguem nossas prekeys; tratar `notification type=account_sync` (device-list)
   e responder o que o servidor espera. Possivelmente processar app-state o suficiente.
3. **App-state sync (LTHash)**: se necessário para o device-list — pode empurrar parte
   para #5; implementar o mínimo para o fanout.
4. **Enviar 1:1**: usync do destinatário (device-list), buscar prekey bundle
   (`<iq type=get xmlns=encrypt><key>`), X3DH initiator (já temos), montar `<message>`
   com `<enc>` por device, padding, enviar; tratar `<receipt>`/`<ack>` de retorno.

## 5. Componentes
- `internal/client/receive.go`: ackar notifications/ib/message; handler de presença.
- `internal/client/send.go`: enviar 1:1 (usync + bundle fetch + encrypt fanout).
- `internal/client/usync.go`: query de device-list + prekey bundle.
- `internal/waproto`: o que faltar de Message para envio.

## 6. De-risco
- Harness Evolution (ADM→chip) para receber; e wa-go→ADM (ou wa-go→Felipe) para enviar,
  conferindo no Chatwoot/WhatsApp do destino.
- Golden traces da Baileys para o nó de envio/usync se necessário.

## 7. Definition of Done (#4, fase 1 = receber)
- Mensagem enviada do ADM via Evolution chega ao wa-go `-listen` e é impressa com o texto
  correto, em tempo real.
- (fase 2) wa-go envia uma mensagem 1:1 que chega no destino.
