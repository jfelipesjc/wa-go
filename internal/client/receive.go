package client

import (
	"errors"
	"fmt"

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

// messageText extracts the displayable text from a decoded WAProto.Message,
// preferring `conversation` (field 1) then `extendedTextMessage.text` (6.1).
func messageText(m *waproto.Message) string {
	if c := m.GetConversation(); c != "" {
		return c
	}
	if et := m.GetExtendedTextMessage(); et != nil {
		return et.GetText()
	}
	// deviceSentMessage: a message the account sent from another device (e.g. the
	// phone). Linked devices receive a copy with the real content nested; unwrap.
	if ds := m.GetDeviceSentMessage(); ds != nil {
		return messageText(ds.GetMessage())
	}
	return ""
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

	addr, err := signalAddress(from)
	if err != nil {
		return err
	}

	var firstErr error
	for _, enc := range childrenByTag(node, "enc") {
		encType := enc.Attrs["type"]
		ciphertext := nodeBytes(enc)
		if len(ciphertext) == 0 {
			continue
		}
		padded, err := c.decryptEnc(addr, creds, encType, ciphertext)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			if debugPairing {
				fmt.Fprintf(debugOut, "[wa-go] decrypt %s from %s: %v\n", encType, from, err)
			}
			continue
		}
		plaintext, err := unpadMessage(padded)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		var m waproto.Message
		if err := proto.Unmarshal(plaintext, &m); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("client: unmarshal message: %w", err)
			}
			continue
		}
		c.emit(MessageEvent{From: from, Text: messageText(&m), ID: msgID})
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
