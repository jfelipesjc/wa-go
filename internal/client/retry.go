package client

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/felipeleal/wa-go/internal/keys"
	"github.com/felipeleal/wa-go/internal/store"
	"github.com/felipeleal/wa-go/internal/waproto"
	"github.com/felipeleal/wa-go/internal/wire"
)

// maxMsgRetryCount caps how many retry receipts we ask for per inbound message
// before giving up, mirroring Baileys' DEFAULT_RETRY_CONFIG.maxMsgRetryCount.
const maxMsgRetryCount = 5

// defaultSentCacheCap bounds the outbound message cache used to answer retry
// receipts. Older entries are evicted FIFO once the cap is exceeded.
const defaultSentCacheCap = 256

// retryPreKeyBase is the first id used for one-time pre-keys minted on demand for
// a retry receipt's <keys> bundle. It sits far above the initial upload range
// (1..initialPreKeyCount) so retry pre-keys never collide with uploaded ones.
const retryPreKeyBase = 0x800000

// --- 1. retry counter (per inbound msgId) ---

// nextRetryCount increments and returns the retry count for an inbound msgId.
// It returns (count, ok): ok is false once the cap (maxMsgRetryCount) has been
// reached, signalling the caller to give up rather than ask again. Mirrors the
// fallback branch of Baileys' sendRetryRequest (retryCount >= maxMsgRetryCount
// => clear and stop).
func (c *Client) nextRetryCount(msgID string) (int, bool) {
	c.retryMu.Lock()
	defer c.retryMu.Unlock()
	cur := c.msgRetry[msgID]
	if cur >= maxMsgRetryCount {
		delete(c.msgRetry, msgID)
		return cur, false
	}
	cur++
	c.msgRetry[msgID] = cur
	return cur, true
}

// --- 2. outbound message cache (to answer retry receipts) ---

// rememberSent records an outgoing message so a later retry receipt for it can be
// answered by re-encrypting the same content. The cache is FIFO-evicted at sentCap.
func (c *Client) rememberSent(msgID, toJID string, msg *waproto.Message) {
	if msgID == "" || msg == nil {
		return
	}
	c.retryMu.Lock()
	defer c.retryMu.Unlock()
	if _, exists := c.sentCache[msgID]; !exists {
		c.sentOrder = append(c.sentOrder, msgID)
	}
	c.sentCache[msgID] = &sentMessage{msg: msg, toJID: toJID}
	limit := c.sentCap
	if limit <= 0 {
		limit = defaultSentCacheCap
	}
	for len(c.sentOrder) > limit {
		oldest := c.sentOrder[0]
		c.sentOrder = c.sentOrder[1:]
		delete(c.sentCache, oldest)
	}
}

// lookupSent returns the cached outbound message for msgID, if present.
func (c *Client) lookupSent(msgID string) (*sentMessage, bool) {
	c.retryMu.Lock()
	defer c.retryMu.Unlock()
	m, ok := c.sentCache[msgID]
	return m, ok
}

// --- retry pre-key minting ---

// mintRetryPreKey generates one fresh one-time pre-key (with a unique high id),
// persists its pair in the store (so the remote can consume it when it re-derives
// the session), and returns it for inclusion in the retry <keys> bundle.
func (c *Client) mintRetryPreKey() (keys.PreKey, error) {
	id := c.retryPreKey.Add(1)
	preKeys, err := keys.GenPreKeys(id, 1)
	if err != nil {
		return keys.PreKey{}, fmt.Errorf("client: mint retry pre-key: %w", err)
	}
	pk := preKeys[0]
	batch := map[uint32]store.StoredKeyPair{
		pk.KeyID: {
			Priv: append([]byte(nil), pk.KeyPair.Priv[:]...),
			Pub:  append([]byte(nil), pk.KeyPair.Pub[:]...),
		},
	}
	if err := c.store.StorePreKeys(batch); err != nil {
		return keys.PreKey{}, fmt.Errorf("client: persist retry pre-key: %w", err)
	}
	return pk, nil
}

// --- retry receipt construction (the receipt WE send when WE failed to decrypt) ---

// buildRetryReceipt assembles a <receipt type=retry> for an inbound message we
// could not decrypt, mirroring Baileys' sendRetryRequest:
//
//	<receipt id=<msgId> type=retry to=<from> [participant=<p>] [recipient=<r>]>
//	  <retry count=<n> id=<stanzaId> t=<t> v="1"/>
//	  <registration>{regId 4B BE}</registration>
//	  [ <keys> (only when count>1)
//	      <type>{0x05}</type>
//	      <identity>{ourIdentityPub 32B}</identity>
//	      <key><id>{3B}</id><value>{prekeyPub 32B}</value></key>
//	      <skey><id>{3B}</id><value>{signedPub 32B}</value><signature>{64B}</signature></skey>
//	      <device-identity>{account}</device-identity>
//	  </keys> ]
//	</receipt>
//
// inMsg is the original <message> stanza. When includeKeys is true a fresh
// one-time pre-key is minted (and persisted) so the remote can re-establish the
// session; the minted key is returned for inspection/testing.
func (c *Client) buildRetryReceipt(inMsg wire.Node, creds *store.Creds, count int) (wire.Node, *keys.PreKey, error) {
	msgID := inMsg.Attrs["id"]
	from := inMsg.Attrs["from"]

	retryAttrs := map[string]string{
		"count": strconv.Itoa(count),
		"id":    msgID,
		"v":     "1",
	}
	if t := inMsg.Attrs["t"]; t != "" {
		retryAttrs["t"] = t
	} else {
		retryAttrs["t"] = strconv.FormatInt(time.Now().Unix(), 10)
	}

	content := []wire.Node{
		{Tag: "retry", Attrs: retryAttrs},
		{Tag: "registration", Attrs: map[string]string{}, Content: encodeBigEndianN(creds.RegistrationID, 4)},
	}

	var minted *keys.PreKey
	// Baileys attaches the <keys> bundle from the 2nd retry onward (retryCount > 1)
	// so a remote that keeps failing can re-derive the session against fresh keys.
	if count > 1 {
		pk, err := c.mintRetryPreKey()
		if err != nil {
			return wire.Node{}, nil, err
		}
		minted = &pk
		content = append(content, retryKeysNode(creds, pk))
	}

	attrs := map[string]string{
		"id":   msgID,
		"type": "retry",
		"to":   from,
	}
	if p := inMsg.Attrs["participant"]; p != "" {
		attrs["participant"] = p
	}
	if r := inMsg.Attrs["recipient"]; r != "" {
		attrs["recipient"] = r
	}

	return wire.Node{Tag: "receipt", Attrs: attrs, Content: content}, minted, nil
}

// retryKeysNode builds the <keys> bundle attached to a retry receipt, mirroring
// the bundle Baileys' getNextPreKeys / uploadPreKeys produces: a key-bundle type
// byte, our raw identity public key, one one-time pre-key, our signed pre-key,
// and our signed device-identity (account).
func retryKeysNode(creds *store.Creds, pk keys.PreKey) wire.Node {
	content := []wire.Node{
		{Tag: "type", Attrs: map[string]string{}, Content: append([]byte(nil), keyBundleType...)},
		{Tag: "identity", Attrs: map[string]string{}, Content: append([]byte(nil), creds.IdentityKey.Pub...)},
		xmppPreKey(pk),
		xmppSignedPreKey(creds),
		{Tag: "device-identity", Attrs: map[string]string{}, Content: append([]byte(nil), creds.Account...)},
	}
	return wire.Node{Tag: "keys", Attrs: map[string]string{}, Content: content}
}

// sendRetryReceipt asks the remote to resend an inbound message we could not
// decrypt. It bumps the per-msgId retry counter (giving up at the cap) and writes
// a <receipt type=retry> via send. Errors are non-fatal to the read loop: the
// message is already lost, this is a best-effort recovery.
func (c *Client) sendRetryReceipt(send func(wire.Node) error, inMsg wire.Node, creds *store.Creds) error {
	msgID := inMsg.Attrs["id"]
	count, ok := c.nextRetryCount(msgID)
	if !ok {
		if debugPairing {
			fmt.Fprintf(debugOut, "[wa-go] retry: giving up on %s after %d tries\n", msgID, count)
		}
		return nil
	}
	receipt, _, err := c.buildRetryReceipt(inMsg, creds, count)
	if err != nil {
		return err
	}
	if debugPairing {
		fmt.Fprintf(debugOut, "[wa-go] retry: requesting resend of %s (count=%d)\n", msgID, count)
	}
	return send(receipt)
}

// --- 3. responding to a retry receipt for OUR message ---

// handleRetryReceipt processes an inbound <receipt type=retry>: a device could
// not decrypt a message WE sent and is asking us to resend it. We look up the
// original in the sent cache, (re)establish a session with the requesting device
// — preferring the <keys> bundle the receipt carries, otherwise re-fetching via
// assertSessions — re-encrypt, and resend a <message> targeted at that one
// device. If the message is not cached we log and ignore (Baileys: "message not
// available"). Mirrors Baileys' sendMessagesAgain.
func (c *Client) handleRetryReceipt(ctx context.Context, sess *session, node wire.Node) error {
	msgID := node.Attrs["id"]
	if msgID == "" {
		return nil
	}
	// The requesting device: participant when present (group / specific device),
	// otherwise the receipt's from. Mirrors Baileys' key.participant || attrs.from.
	device := node.Attrs["participant"]
	if device == "" {
		device = node.Attrs["from"]
	}
	if device == "" {
		return nil
	}

	cached, ok := c.lookupSent(msgID)
	if !ok {
		if debugPairing {
			fmt.Fprintf(debugOut, "[wa-go] retry: no cached message for %s, ignoring\n", msgID)
		}
		return nil
	}

	dev, err := parseDeviceJID(device)
	if err != nil {
		return err
	}

	// Re-establish the session with the requesting device. If the receipt carries
	// a <keys> bundle, use it directly (X3DH initiator against the fresh bundle);
	// this replaces any stale session via persistSession. Otherwise fall back to a
	// fresh prekey fetch + assertSessions.
	if bundle, ok := extractRetryBundle(node, dev.JID); ok {
		if err := c.initiateSessionFromBundle(sess.creds, bundle); err != nil {
			return fmt.Errorf("client: retry inject session for %s: %w", dev.JID, err)
		}
	} else {
		if err := c.fetchPreKeyBundles(ctx, sess, []string{dev.JID}); err != nil {
			return fmt.Errorf("client: retry fetch session for %s: %w", dev.JID, err)
		}
	}

	plaintext, err := encodeWAMessage(cached.msg)
	if err != nil {
		return err
	}
	participantNodes, err := c.encryptForDevices(sess.creds, []deviceJID{dev}, plaintext)
	if err != nil {
		return err
	}
	if len(participantNodes) == 0 {
		return fmt.Errorf("client: retry resend %s: no encryptable device", msgID)
	}

	// Resend to the recipient JID (the conversation), with the participant node
	// scoped to the one requesting device. Reuse the SAME msgID so the device
	// dedupes against the message it failed to decrypt.
	stanza := buildMessageStanza(msgID, cached.toJID, "text", participantNodes, sess.creds.Account)
	if debugPairing {
		fmt.Fprintf(debugOut, "[wa-go] retry: resending %s to %s\n", msgID, dev.JID)
	}
	return sess.send(stanza)
}

// parseDeviceJID turns a JID string into the deviceJID the encrypt path expects.
func parseDeviceJID(jid string) (deviceJID, error) {
	user, device, err := parseJID(jid)
	if err != nil {
		return deviceJID{}, err
	}
	return deviceJID{
		User:   user,
		Device: device,
		JID:    deviceJIDString(user, device, serverOfJID(jid)),
	}, nil
}

// extractRetryBundle parses the optional <keys> bundle of a retry receipt into a
// preKeyBundle keyed to jid, mirroring Baileys' extractE2ESessionFromRetryReceipt.
// It returns ok=false when no (valid) bundle is present.
func extractRetryBundle(receipt wire.Node, jid string) (preKeyBundle, bool) {
	keysNode, ok := childByTag(receipt, "keys")
	if !ok {
		return preKeyBundle{}, false
	}
	typeNode, ok := childByTag(keysNode, "type")
	if !ok {
		return preKeyBundle{}, false
	}
	tb := nodeBytes(typeNode)
	if len(tb) != 1 || tb[0] != keyBundleType[0] {
		return preKeyBundle{}, false
	}
	idNode, ok := childByTag(keysNode, "identity")
	if !ok {
		return preKeyBundle{}, false
	}
	idPub, err := stripKeyType(nodeBytes(idNode))
	if err != nil {
		return preKeyBundle{}, false
	}
	skey, ok := childByTag(keysNode, "skey")
	if !ok {
		return preKeyBundle{}, false
	}

	b := preKeyBundle{JID: jid, IdentityKey: idPub}
	b.RegistrationID = childUintBE(receipt, "registration")
	b.SignedPreKeyID = childUintBE(skey, "id")

	sval, ok := childByTag(skey, "value")
	if !ok {
		return preKeyBundle{}, false
	}
	spkPub, err := stripKeyType(nodeBytes(sval))
	if err != nil {
		return preKeyBundle{}, false
	}
	b.SignedPreKeyPub = spkPub
	sig, ok := childByTag(skey, "signature")
	if !ok || len(nodeBytes(sig)) != 64 {
		return preKeyBundle{}, false
	}
	copy(b.SignedPreKeySig[:], nodeBytes(sig))

	if key, ok := childByTag(keysNode, "key"); ok {
		if kv, ok := childByTag(key, "value"); ok {
			pkPub, err := stripKeyType(nodeBytes(kv))
			if err == nil {
				b.PreKeyPub = pkPub
				b.PreKeyID = childUintBE(key, "id")
				b.HasPreKey = true
			}
		}
	}
	return b, true
}

// isRetryReceipt reports whether a node is a <receipt type=retry>.
func isRetryReceipt(n wire.Node) bool {
	return n.Tag == "receipt" && n.Attrs["type"] == "retry"
}
