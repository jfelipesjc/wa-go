package client

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/felipeleal/wa-go/internal/keys"
	"github.com/felipeleal/wa-go/internal/signal"
	"github.com/felipeleal/wa-go/internal/store"
	"github.com/felipeleal/wa-go/internal/waproto"
	"github.com/felipeleal/wa-go/internal/wire"
	"google.golang.org/protobuf/proto"
)

// signalAddress derives the libsignal protocol address ("<user>.<device>") for a
// JID, mirroring Baileys' jidToSignalProtocolAddress: the numeric user, a dot,
// and the device (0 for the phone). The session store is keyed by this string.
//
// This works uniformly for phone-number JIDs ("<num>@s.whatsapp.net") and LID
// JIDs ("<num>@lid"): both carry a numeric user, and parseJID extracts it
// regardless of the server. A LID-addressed message therefore keys its own
// signal session under the LID user, kept distinct from the PN session — exactly
// as Baileys does (a @lid conversation uses the LID identity). The 1:1 PN flow is
// unaffected because PN senders still resolve to their PN user here.
func signalAddress(jid string) (string, error) {
	user, device, err := parseJID(jid)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d.%d", user, device), nil
}

// unpadMessage strips WhatsApp's random padding from a decrypted plaintext: the
// final byte is the number of padding bytes to remove (unpadRandomMax16 in
// Baileys). The padding is 1..16 bytes; the last byte equals that count.
func unpadMessage(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return nil, errors.New("client: empty padded message")
	}
	pad := int(b[len(b)-1])
	if pad == 0 || pad > len(b) {
		return nil, fmt.Errorf("client: bad pad %d for %d bytes", pad, len(b))
	}
	return b[:len(b)-pad], nil
}

// decryptEnc decrypts one <enc> payload for the given sender, handling both
// "pkmsg" (establishes a new session via X3DH responder, consuming a one-time
// prekey) and "msg" (advances an existing session). On success it persists the
// updated session record and returns the still-padded plaintext.
func (c *Client) decryptEnc(addr string, creds *store.Creds, encType string, ciphertext []byte) ([]byte, error) {
	switch encType {
	case "pkmsg":
		return c.decryptPreKey(addr, creds, ciphertext)
	case "msg":
		return c.decryptMsg(addr, ciphertext)
	default:
		return nil, fmt.Errorf("client: unsupported enc type %q", encType)
	}
}

// decryptPreKey handles a PreKeySignalMessage: parse the referenced signed/one-
// time prekey ids, load the matching key pairs, run the responder X3DH, decrypt,
// persist the new session, remember the peer identity, and consume the prekey.
func (c *Client) decryptPreKey(addr string, creds *store.Creds, ciphertext []byte) ([]byte, error) {
	pm, err := signal.ParsePreKeyWhisperMessage(ciphertext)
	if err != nil {
		return nil, err
	}

	localIdentity := keyPairFromCreds(creds.IdentityKey)

	// Signed pre-key referenced by the message (load by id, fall back to creds).
	signedPre, ok, err := c.loadSignedPreKey(pm.SignedPreKeyID, creds)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("client: signed pre-key %d not found", pm.SignedPreKeyID)
	}

	var preKey *keys.KeyPair
	if pm.HasPreKeyID {
		kp, ok, err := c.store.LoadPreKey(pm.PreKeyID)
		if err != nil {
			return nil, err
		}
		if !ok {
			// The one-time prekey was already consumed by an earlier pkmsg that
			// established this session. A peer keeps wrapping messages as pkmsg
			// (same baseKey/prekey) until it sees our first reply, so a burst
			// (e.g. history sync) arrives as several pkmsg referencing the same,
			// now-removed prekey. libsignal handles this by reusing the existing
			// session instead of requiring the prekey again: decrypt the embedded
			// WhisperMessage against the session we already built.
			if _, sok, serr := c.store.LoadSession(addr); serr == nil && sok {
				if pt, derr := c.decryptMsg(addr, pm.Message); derr == nil {
					return pt, nil
				}
			}
			return nil, fmt.Errorf("client: one-time pre-key %d not found (used already?)", pm.PreKeyID)
		}
		k := keyPairFromStored(kp)
		preKey = &k
	}

	plaintext, state, err := signal.ProcessPreKeyMessage(ciphertext, localIdentity, signedPre, preKey, creds.RegistrationID)
	if err != nil {
		return nil, err
	}

	if err := c.persistSession(addr, state); err != nil {
		return nil, err
	}
	// Trust-on-first-use: remember the peer's identity key.
	if err := c.store.SaveIdentity(addr, append([]byte(nil), state.RemoteIdentityPub[:]...)); err != nil {
		return nil, err
	}
	// The one-time pre-key is single-use; remove it once consumed.
	if pm.HasPreKeyID {
		if err := c.store.RemovePreKey(pm.PreKeyID); err != nil {
			return nil, err
		}
	}
	return plaintext, nil
}

// decryptMsg handles a bare WhisperMessage against an existing session.
func (c *Client) decryptMsg(addr string, ciphertext []byte) ([]byte, error) {
	blob, ok, err := c.store.LoadSession(addr)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("client: no session for %s", addr)
	}
	rec, err := signal.UnmarshalSessionRecord(blob)
	if err != nil {
		return nil, err
	}
	if rec.State == nil {
		return nil, fmt.Errorf("client: empty session for %s", addr)
	}
	cipher := signal.NewSessionCipher(rec.State)
	plaintext, err := cipher.Decrypt(ciphertext)
	if err != nil {
		return nil, err
	}
	if err := c.persistSession(addr, rec.State); err != nil {
		return nil, err
	}
	return plaintext, nil
}

// persistSession serializes and stores a session state for an address.
func (c *Client) persistSession(addr string, state *signal.SessionState) error {
	rec := &signal.SessionRecord{State: state}
	blob, err := rec.Marshal()
	if err != nil {
		return err
	}
	return c.store.StoreSession(addr, blob)
}

// loadSignedPreKey returns the signed pre-key pair for id, preferring the store
// and falling back to the one in creds (id matches creds.SignedPreKey.KeyID).
func (c *Client) loadSignedPreKey(id uint32, creds *store.Creds) (keys.KeyPair, bool, error) {
	kp, ok, err := c.store.LoadSignedPreKey(id)
	if err != nil {
		return keys.KeyPair{}, false, err
	}
	if ok {
		return keyPairFromStored(kp), true, nil
	}
	if id == creds.SignedPreKey.KeyID {
		return keyPairFromCreds(creds.SignedPreKey.KeyPair), true, nil
	}
	return keys.KeyPair{}, false, nil
}

// handleMessage processes an incoming <message> stanza: it decodes the sender
// and id, decrypts each <enc> child, unpads + parses the WAProto.Message, emits a
// MessageEvent with the text, and sends the receipt + message ack. send must be
// the serialized sender used by the read loop.
func (c *Client) handleMessage(send func(wire.Node) error, node wire.Node, creds *store.Creds) error {
	from := node.Attrs["from"]
	msgID := node.Attrs["id"]
	if from == "" || msgID == "" {
		return errors.New("client: message missing from/id")
	}

	isGroup := isGroupJID(from)
	// participant is the actual human sender for group messages; for 1:1 it is
	// empty and `from` is the sender. The signal session (pkmsg/msg) is keyed by
	// the sender's device address.
	participant := node.Attrs["participant"]
	senderJID := from
	if isGroup && participant != "" {
		senderJID = participant
	}

	// Newer stanzas carry the LID<->PN counterpart of the sender (participant_pn/
	// sender_pn when LID-addressed, the inverse *_lid attrs when PN-addressed).
	// Record any pairing they reveal so future addressing can resolve either side.
	c.registerAddressingMapping(node.Attrs)

	addr, err := signalAddress(senderJID)
	if err != nil {
		return err
	}

	var firstErr error
	decryptFailed := false
	noteErr := func(e error) {
		if firstErr == nil {
			firstErr = e
		}
	}

	for _, enc := range childrenByTag(node, "enc") {
		encType := enc.Attrs["type"]
		ciphertext := nodeBytes(enc)
		if len(ciphertext) == 0 {
			continue
		}

		var m *waproto.Message
		if encType == "skmsg" {
			// Group sender-key message: decrypt with the sender's installed
			// SenderKeyRecord for this group. No unpad/parse-as-pkmsg here.
			pm, err := c.decryptGroup(from, senderJID, ciphertext)
			if err != nil {
				noteErr(err)
				decryptFailed = true
				if debugPairing {
					fmt.Fprintf(debugOut, "[wa-go] decrypt skmsg from %s in %s: %v\n", senderJID, from, err)
				}
				continue
			}
			m = pm
		} else {
			padded, err := c.decryptEnc(addr, creds, encType, ciphertext)
			if err != nil {
				noteErr(err)
				decryptFailed = true
				if debugPairing {
					fmt.Fprintf(debugOut, "[wa-go] decrypt %s from %s: %v\n", encType, senderJID, err)
				}
				continue
			}
			plaintext, err := unpadMessage(padded)
			if err != nil {
				noteErr(err)
				continue
			}
			var msg waproto.Message
			if err := proto.Unmarshal(plaintext, &msg); err != nil {
				noteErr(fmt.Errorf("client: unmarshal message: %w", err))
				continue
			}
			m = &msg
		}

		// A pkmsg/msg in a group often carries only the SenderKeyDistributionMessage
		// (no user-visible content): install the sender key so subsequent skmsg can
		// be decrypted, then skip emitting an empty event.
		if skdm := senderKeyDistribution(m); skdm != nil {
			if err := c.processSenderKeyDistribution(from, senderJID, skdm); err != nil {
				noteErr(err)
			}
			if isOnlySenderKeyDistribution(m) {
				continue
			}
		}

		// Protocol-message side effects (app-state key share, history sync)
		// are ingested before surfacing the event. These carry no user-visible
		// content, so on a pure-side-effect message we skip emitting.
		if pm := protocolMessageOf(m); pm != nil {
			if c.handleProtocolSideEffects(pm) {
				continue
			}
		}

		ev := parseMessage(m)
		ev.From = from
		ev.ID = msgID
		ev.IsGroup = isGroup
		if isGroup {
			ev.Sender = senderJID
		}
		ev.Timestamp = parseTimestamp(node.Attrs["t"])
		ev.PushName = node.Attrs["notify"]
		c.emit(ev)
	}

	// On a decryption failure, ask the sender to resend (retry receipt) instead of
	// silently dropping the message. We still send the message <ack> so the server
	// stops queuing this delivery, but we do NOT send the plain delivery <receipt>
	// (that would tell the sender we received it fine). Mirrors Baileys, which
	// sends a retry receipt on a failed decrypt.
	if decryptFailed {
		if err := c.sendRetryReceipt(send, node, creds); err != nil && debugPairing {
			fmt.Fprintf(debugOut, "[wa-go] send retry receipt: %v\n", err)
		}
		if err := send(messageAckNode(node, creds.Me)); err != nil {
			return err
		}
		return firstErr
	}

	// Acknowledge the message regardless so the server does not redeliver.
	if err := send(receiptNode(node)); err != nil {
		return err
	}
	if err := send(messageAckNode(node, creds.Me)); err != nil {
		return err
	}
	return firstErr
}

// protocolMessageOf returns the ProtocolMessage carried by a decoded Message
// (after unwrapping device/ephemeral wrappers), or nil if none.
func protocolMessageOf(m *waproto.Message) *waproto.ProtocolMessage {
	m = unwrapMessage(m)
	if m == nil {
		return nil
	}
	return m.GetProtocolMessage()
}

// handleProtocolSideEffects ingests the protocol messages that carry state we
// persist/act on rather than surface as a MessageEvent: an
// APP_STATE_SYNC_KEY_SHARE (persist each key so app-state/chatmod can decrypt)
// and a HISTORY_SYNC_NOTIFICATION (download + decode history asynchronously).
// It returns true when the protocol message is a pure side effect (no further
// event should be emitted for it).
func (c *Client) handleProtocolSideEffects(pm *waproto.ProtocolMessage) bool {
	handled := false
	if share := pm.GetAppStateSyncKeyShare(); share != nil {
		if err := c.storeAppStateSyncKeys(share); err != nil && debugPairing {
			fmt.Fprintf(debugOut, "[wa-go] store app-state sync keys: %v\n", err)
		}
		handled = true
	}
	if notif := pm.GetHistorySyncNotification(); notif != nil {
		// The download is live (needs a media_conn + HTTP); run it off the read
		// loop so receipts/acks are not blocked. Errors are logged, not fatal.
		go func() {
			if err := c.handleHistorySync(context.Background(), notif); err != nil && debugPairing {
				fmt.Fprintf(debugOut, "[wa-go] history sync: %v\n", err)
			}
		}()
		handled = true
	}
	return handled
}

// storeAppStateSyncKeys persists every AppStateSyncKey carried in an
// APP_STATE_SYNC_KEY_SHARE into the store, keyed by its keyId. These keys unlock
// the app-state (chatmod) collections.
func (c *Client) storeAppStateSyncKeys(share *waproto.AppStateSyncKeyShare) error {
	var firstErr error
	for _, k := range share.GetKeys() {
		keyID := k.GetKeyId().GetKeyId()
		keyData := k.GetKeyData().GetKeyData()
		if len(keyID) == 0 || len(keyData) == 0 {
			continue
		}
		if err := c.store.StoreAppStateSyncKey(keyID, keyData); err != nil && firstErr == nil {
			firstErr = err
		}
		// Bridge: remember the keyId so chatmod/resync can auto-load the material
		// from the store without a manual ConfigureAppState call.
		c.noteAppStateKeyID(keyID)
	}
	return firstErr
}

// isGroupJID reports whether a JID addresses a group (@g.us).
func isGroupJID(jid string) bool {
	return strings.HasSuffix(jid, "@g.us")
}

// parseTimestamp parses the stanza `t` attribute (unix seconds) into int64, or 0.
func parseTimestamp(s string) int64 {
	if s == "" {
		return 0
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// senderKeyDistribution returns the inner SenderKeyDistributionMessage carried in
// a decoded Message (after unwrapping device/ephemeral wrappers), parsed from the
// embedded axolotl SKDM bytes, or nil if none/parse fails.
func senderKeyDistribution(m *waproto.Message) *signal.SenderKeyDistributionMessage {
	m = unwrapMessage(m)
	if m == nil {
		return nil
	}
	wrap := m.GetSenderKeyDistributionMessage()
	if wrap == nil {
		return nil
	}
	raw := wrap.GetAxolotlSenderKeyDistributionMessage()
	if len(raw) == 0 {
		return nil
	}
	skdm, err := signal.ParseSenderKeyDistributionMessage(raw)
	if err != nil {
		return nil
	}
	return skdm
}

// isOnlySenderKeyDistribution reports whether the message carries nothing but the
// sender-key distribution (no user-visible content to emit).
func isOnlySenderKeyDistribution(m *waproto.Message) bool {
	m = unwrapMessage(m)
	if m == nil {
		return true
	}
	return m.GetConversation() == "" &&
		m.GetExtendedTextMessage() == nil &&
		m.GetImageMessage() == nil &&
		m.GetVideoMessage() == nil &&
		m.GetAudioMessage() == nil &&
		m.GetDocumentMessage() == nil &&
		m.GetStickerMessage() == nil &&
		m.GetLocationMessage() == nil &&
		m.GetLiveLocationMessage() == nil &&
		m.GetContactMessage() == nil &&
		m.GetReactionMessage() == nil &&
		m.GetPollCreationMessage() == nil &&
		m.GetProtocolMessage() == nil
}

// processSenderKeyDistribution installs a peer's sender key for a group into the
// store, so subsequent skmsg from that sender can be decrypted. It loads the
// existing record (if any), applies the SKDM via the GroupCipher, and persists.
func (c *Client) processSenderKeyDistribution(group, sender string, skdm *signal.SenderKeyDistributionMessage) error {
	rec, err := c.loadSenderKeyRecord(group, sender)
	if err != nil {
		return err
	}
	cipher := signal.NewGroupCipher(rec)
	cipher.ProcessSenderKeyDistribution(skdm)
	return c.storeSenderKeyRecord(group, sender, cipher.Record())
}

// decryptGroup decrypts a group sender-key (skmsg) ciphertext for (group,
// sender), advancing and persisting the sender key chain. It returns the parsed
// WAProto.Message.
func (c *Client) decryptGroup(group, sender string, ciphertext []byte) (*waproto.Message, error) {
	rec, err := c.loadSenderKeyRecord(group, sender)
	if err != nil {
		return nil, err
	}
	if rec.IsEmpty() {
		return nil, fmt.Errorf("client: no sender key for %s in %s (SKDM not yet received)", sender, group)
	}
	cipher := signal.NewGroupCipher(rec)
	padded, err := cipher.DecryptGroup(ciphertext)
	if err != nil {
		return nil, err
	}
	// Persist the advanced chain so the next skmsg decrypts.
	if err := c.storeSenderKeyRecord(group, sender, cipher.Record()); err != nil {
		return nil, err
	}
	plaintext, err := unpadMessage(padded)
	if err != nil {
		return nil, err
	}
	var m waproto.Message
	if err := proto.Unmarshal(plaintext, &m); err != nil {
		return nil, fmt.Errorf("client: unmarshal group message: %w", err)
	}
	return &m, nil
}

// loadSenderKeyRecord loads (or creates empty) the SenderKeyRecord for (group,
// sender) from the store.
func (c *Client) loadSenderKeyRecord(group, sender string) (*signal.SenderKeyRecord, error) {
	blob, ok, err := c.store.LoadSenderKey(group, sender)
	if err != nil {
		return nil, err
	}
	if !ok {
		return &signal.SenderKeyRecord{}, nil
	}
	return signal.UnmarshalSenderKeyRecord(blob)
}

// storeSenderKeyRecord serializes and persists a SenderKeyRecord for (group,
// sender).
func (c *Client) storeSenderKeyRecord(group, sender string, rec *signal.SenderKeyRecord) error {
	blob, err := signal.MarshalSenderKeyRecord(rec)
	if err != nil {
		return err
	}
	return c.store.StoreSenderKey(group, sender, blob)
}

// receiptNode builds the <receipt> for a received message, mirroring Baileys'
// sendReceipt with no explicit type (delivery): <receipt to=from id=msgid
// [participant=...]/>.
func receiptNode(msg wire.Node) wire.Node {
	attrs := map[string]string{
		"to": msg.Attrs["from"],
		"id": msg.Attrs["id"],
	}
	if p := msg.Attrs["participant"]; p != "" {
		attrs["participant"] = p
	}
	return wire.Node{Tag: "receipt", Attrs: attrs}
}

// messageAckNode builds the <ack class=message> for a received message, mirroring
// Baileys' buildAckStanza: <ack id=msgid to=from class=message [type=...]
// [participant=...] from=meId/>.
func messageAckNode(msg wire.Node, meID string) wire.Node {
	attrs := map[string]string{
		"id":    msg.Attrs["id"],
		"to":    msg.Attrs["from"],
		"class": "message",
	}
	if t := msg.Attrs["type"]; t != "" {
		attrs["type"] = t
	}
	if p := msg.Attrs["participant"]; p != "" {
		attrs["participant"] = p
	}
	if meID != "" {
		attrs["from"] = meID
	}
	return wire.Node{Tag: "ack", Attrs: attrs}
}

// stanzaAckNode builds a generic <ack> for a notification/receipt stanza,
// mirroring Baileys' sendMessageAck: class = the stanza's tag, echoing id and
// addressing it back to the stanza's `from`.
func stanzaAckNode(n wire.Node, meID string) wire.Node {
	attrs := map[string]string{
		"id":    n.Attrs["id"],
		"to":    n.Attrs["from"],
		"class": n.Tag,
	}
	if t := n.Attrs["type"]; t != "" {
		attrs["type"] = t
	}
	if p := n.Attrs["participant"]; p != "" {
		attrs["participant"] = p
	}
	if meID != "" {
		attrs["from"] = meID
	}
	return wire.Node{Tag: "ack", Attrs: attrs}
}

// presenceAvailableNode announces availability so the server includes this device
// in message delivery, mirroring Baileys' sendPresenceUpdate('available').
func presenceAvailableNode() wire.Node {
	return wire.Node{Tag: "presence", Attrs: map[string]string{"type": "available"}}
}

// --- keypair adapters ---

func keyPairFromCreds(cp store.CredKeyPair) keys.KeyPair {
	var kp keys.KeyPair
	copy(kp.Priv[:], cp.Priv)
	copy(kp.Pub[:], cp.Pub)
	return kp
}

func keyPairFromStored(sp store.StoredKeyPair) keys.KeyPair {
	var kp keys.KeyPair
	copy(kp.Priv[:], sp.Priv)
	copy(kp.Pub[:], sp.Pub)
	return kp
}
