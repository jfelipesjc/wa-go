# Decisões de arquitetura (ADR leve)

## ADR-001 — `Node.Attrs` é `map[string]string` (ordem de attrs não preservada)
**Data:** 2026-06-22 · **Contexto:** #1 Wire / codec.

**Decisão:** manter `Node.Attrs map[string]string` e emitir os atributos em ordem
**determinística (alfabética)** no encode, em vez de preservar a ordem de inserção do
node original.

**Consequência:** o round-trip de binary node é **100% estrutural** e **byte-a-byte exato
exceto pela ordem dos atributos** (17/19 nodes da bateria batem byte-a-byte; os 2 que
não batem diferem só na ordem dos pares de attr — todos os valores/encoding estão
corretos).

**Justificativa:**
1. A whatsmeow (implementação Go de referência, em produção massiva) usa `map` e emite
   attrs em ordem **aleatória** do Go, sem qualquer penalidade do servidor → o WhatsApp
   **aceita qualquer ordem de atributos**. Não há evidência de que ordem de attr seja
   sinal de fingerprint/anti-ban.
2. `map[string]string` dá acesso ergonômico (`node.Attrs["from"]`) às camadas de cima
   (client API, control layer), que é o consumo dominante.
3. Ordem determinística (sort) > ordem aleatória: reprodutível em testes e logs.

**Revisitar se:** testes de anti-ban com chip sacrificial mostrarem que a ordem dos
atributos é observável/penalizada pelo servidor. Nesse caso, trocar `Attrs` por um
tipo ordenado (slice de pares com acesso map-like) — mudança contida no pacote `wire`.

**Onde byte-exatidão É obrigatória:** handshake Noise e payloads protobuf (ClientPayload),
onde a verificação criptográfica depende dos bytes exatos. Isso é coberto por Task 7.
