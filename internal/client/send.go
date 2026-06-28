package client

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jfelipesjc/wa-go/internal/signal"
	"github.com/jfelipesjc/wa-go/internal/store"
	"github.com/jfelipesjc/wa-go/internal/waproto"
	"github.com/jfelipesjc/wa-go/internal/wire"
	"google.golang.org/protobuf/proto"
)

// sendIQTimeout bounds how long SendText waits for a usync / prekey-bundle reply
// before giving up, so a stalled server cannot hang the caller indefinitely.
var sendIQTimeout = 30 * time.Second

// SendText and the other text variants live in send_text.go; they build a
// WAProto.Message and delegate to sendMessage below.
//
// Note on own devices: WhatsApp also encrypts a deviceSentMessage copy to the
// account's OTHER devices so they mirror the conversation. This implementation
// fans out to the RECIPIENT's devices only (the minimum for delivery); our own
// other devices are intentionally not targeted here. See the report for details.

// sendOpts carries optional knobs for the send core. All fields are optional:
// the zero value reproduces SendText's original behavior.
type sendOpts struct {
	// pacerHint is the length passed to the pacer's Wait (so longer payloads
	// incur a longer human-like delay). When 0 the message-type default is used.
	pacerHint int
	// stanzaType overrides the <message type=...> attribute. Empty defaults to
	// "text" (Baileys uses "text" for chat content and "media" for media; we
	// keep "text" as the safe default, matching the original SendText).
	stanzaType string

	// mediaType, when set (e.g. "image"/"video"/"audio"/"document"/"sticker"),
	// is stamped as the `mediatype` attribute on every <enc> node — required for
	// media messages (Baileys getMediaType). Empty for non-media.
	mediaType string

	// mediaID, when set, is stamped as the message node's `media_id` attribute —
	// the upload handle, required for channel (newsletter) media sends.
	mediaID string
}

// sendMessage is the shared 1:1 send core that SendText and every other 1:1
// sender (reply, mention, media, reaction, edit, delete) funnels through. It
// performs the full Baileys relayMessage pipeline for a non-group message:
//
//  1. consult the pacer (rate limit + human-like delay),
//  2. encode + pad the WAProto.Message,
//  3. usync the recipient's device list,
//  4. assertSessions (fetch prekey bundles + X3DH for missing devices),
//  5. encrypt per device into <to><enc> participant nodes,
//  6. wrap in a <message> stanza (with <device-identity> when any pkmsg) and send.
//
// It returns the generated message id once the stanza is written to the wire.
func (c *Client) sendMessage(ctx context.Context, toJID string, msg *waproto.Message, opts sendOpts) (string, error) {
	sess, ok := c.activeSession()
	if !ok {
		return "", errors.New("client: not logged in (no active session)")
	}

	// Control Layer (B): consult the pacer before sending. Allow enforces the
	// rate limit; Wait sleeps a human-like delay proportional to the payload
	// size. No pacer = original zero-delay behavior.
	if c.pacer != nil {
		if !c.pacer.Allow() {
			return "", errors.New("client: send rate limit exceeded")
		}
		if _, err := c.pacer.Wait(ctx, opts.pacerHint); err != nil {
			return "", fmt.Errorf("client: pacer wait: %w", err)
		}
	}

	msgID := generateMessageID()

	plaintext, err := encodeWAMessage(msg)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(ctx, sendIQTimeout)
	defer cancel()

	devices, err := c.fetchDevices(ctx, sess, toJID)
	if err != nil {
		return "", fmt.Errorf("client: usync devices: %w", err)
	}
	if len(devices) == 0 {
		// Fall back to the bare device-0 JID if usync returned nothing.
		user, _, perr := parseJID(toJID)
		if perr != nil {
			return "", perr
		}
		devices = []deviceJID{{User: user, Device: 0, JID: deviceJIDString(user, 0, serverOfJID(toJID))}}
	}

	if err := c.assertSessions(ctx, sess, devices); err != nil {
		return "", err
	}

	participantNodes, err := c.encryptForDevices(sess.creds, devices, plaintext, opts.mediaType)
	if err != nil {
		return "", err
	}
	if len(participantNodes) == 0 {
		return "", errors.New("client: no recipients could be encrypted for")
	}

	stanzaType := opts.stanzaType
	if stanzaType == "" {
		stanzaType = "text"
	}
	stanza := buildMessageStanza(msgID, toJID, stanzaType, participantNodes, sess.creds.Account)
	if err := sess.send(stanza); err != nil {
		return "", fmt.Errorf("client: send message: %w", err)
	}
	// Remember what we sent so a later <receipt type=retry> for this msgID can be
	// answered by re-encrypting the same content for the requesting device.
	c.rememberSent(msgID, toJID, msg)
	return msgID, nil
}

// sendRouted dispatches a built message to the right transport based on the
// destination: a `@g.us` JID goes through sendGroupMessage (sender keys), a
// `@newsletter` JID through the unencrypted channel sender (which returns the
// server_id as the id), and everything else 1:1 via sendMessage. Every public
// Send* helper routes through here so groups/channels work uniformly for text,
// media, reaction, location, edit, etc.
func (c *Client) sendRouted(ctx context.Context, toJID string, msg *waproto.Message, opts sendOpts) (string, error) {
	if isGroupJID(toJID) {
		parts, err := c.groupParticipantJIDs(ctx, toJID)
		if err != nil {
			return "", err
		}
		return c.sendGroupMessage(ctx, toJID, parts, msg, opts)
	}
	if isNewsletterJID(toJID) {
		return c.sendNewsletterMessage(ctx, toJID, msg, opts)
	}
	return c.sendMessage(ctx, toJID, msg, opts)
}

// assertSessions ensures a session exists for every device, fetching prekey
// bundles + running X3DH for those that don't (Baileys' assertSessions).
func (c *Client) assertSessions(ctx context.Context, sess *session, devices []deviceJID) error {
	var missing []string
	for _, d := range devices {
		addr, err := signalAddress(d.JID)
		if err != nil {
			return err
		}
		_, ok, err := c.store.LoadSession(addr)
		if err != nil {
			return err
		}
		if !ok {
			missing = append(missing, d.JID)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return c.fetchPreKeyBundles(ctx, sess, missing)
}

// encryptForDevices encrypts the padded plaintext for each device with its
// session cipher, persisting the advanced session, and returns the
// <to jid><enc v=2 type=...> participant nodes (Baileys' createParticipantNodes).
func (c *Client) encryptForDevices(creds *store.Creds, devices []deviceJID, plaintext []byte, mediaType string) ([]wire.Node, error) {
	var nodes []wire.Node
	var firstErr error
	for _, d := range devices {
		addr, err := signalAddress(d.JID)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		blob, ok, err := c.store.LoadSession(addr)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if !ok {
			if firstErr == nil {
				firstErr = fmt.Errorf("client: no session for %s after assertSessions", d.JID)
			}
			continue
		}
		rec, err := signal.UnmarshalSessionRecord(blob)
		if err != nil || rec.State == nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("client: bad session for %s", d.JID)
			}
			continue
		}
		cipher := signal.NewSessionCipher(rec.State)
		ct, err := cipher.Encrypt(plaintext)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("client: encrypt for %s: %w", d.JID, err)
			}
			continue
		}
		if err := c.persistSession(addr, rec.State); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		nodes = append(nodes, toEncNode(d.JID, ct.Type, mediaType, ct.Serialized))
	}
	if len(nodes) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return nodes, nil
}

// toEncNode builds a <to jid=...><enc v=2 type=...>ciphertext</enc></to> node, as
// Baileys' createParticipantNodes does per recipient device. mediaType, when
// non-empty (e.g. "image"), is added as the enc's `mediatype` attribute —
// Baileys sets it (relayMessage extraAttrs) for every media message, and the
// server drops/withholds a media <message> whose <enc> lacks it.
func toEncNode(jid, encType, mediaType string, ciphertext []byte) wire.Node {
	encAttrs := map[string]string{"v": "2", "type": encType}
	if mediaType != "" {
		encAttrs["mediatype"] = mediaType
	}
	return wire.Node{
		Tag:   "to",
		Attrs: map[string]string{"jid": jid},
		Content: []wire.Node{
			{
				Tag:     "enc",
				Attrs:   encAttrs,
				Content: append([]byte(nil), ciphertext...),
			},
		},
	}
}

// buildMessageStanza assembles the 1:1 <message> stanza. The participant <to>
// nodes are wrapped in a <participants> node, matching Baileys' multi-device
// relayMessage layout:
//
//	<message id=msgID to=toJID type=text>
//	  <participants>
//	    <to jid=device0><enc v=2 type=pkmsg>...</enc></to>
//	    <to jid=device1><enc v=2 type=msg>...</enc></to>
//	  </participants>
//	</message>
func buildMessageStanza(msgID, toJID, stanzaType string, participants []wire.Node, account []byte) wire.Node {
	content := []wire.Node{
		{Tag: "participants", Attrs: map[string]string{}, Content: participants},
	}
	// When any participant gets a pkmsg (new session), the recipient must verify
	// our device identity, so attach <device-identity> with the signed ADV identity
	// (Baileys: shouldIncludeDeviceIdentity). account is creds.Account from pairing.
	if len(account) > 0 && participantsHavePkmsg(participants) {
		content = append(content, wire.Node{
			Tag:     "device-identity",
			Attrs:   map[string]string{},
			Content: account,
		})
	}
	return wire.Node{
		Tag: "message",
		Attrs: map[string]string{
			"id":   msgID,
			"to":   toJID,
			"type": stanzaType,
		},
		Content: content,
	}
}

// participantsHavePkmsg reports whether any <to><enc> participant node carries a
// pkmsg (a freshly established session), which requires attaching device-identity.
func participantsHavePkmsg(participants []wire.Node) bool {
	for _, p := range participants {
		for _, ch := range children(p) {
			if ch.Tag == "enc" && ch.Attrs["type"] == "pkmsg" {
				return true
			}
		}
	}
	return false
}

// encodeWAMessage serializes a WAProto.Message and applies WhatsApp's random
// 1..16 byte padding (Baileys' encodeWAMessage / writeRandomPadMax16): a random
// pad length in [1,16], appended that many times as the byte value.
func encodeWAMessage(m *waproto.Message) ([]byte, error) {
	body, err := proto.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("client: marshal message: %w", err)
	}
	return padRandomMax16(body)
}

// padRandomMax16 appends padLen bytes (each equal to padLen) where padLen is a
// random value in [1,16], mirroring writeRandomPadMax16.
func padRandomMax16(b []byte) ([]byte, error) {
	var r [1]byte
	if _, err := rand.Read(r[:]); err != nil {
		return nil, fmt.Errorf("client: read pad random: %w", err)
	}
	padLen := int(r[0]&0x0f) + 1
	out := make([]byte, len(b)+padLen)
	copy(out, b)
	for i := len(b); i < len(out); i++ {
		out[i] = byte(padLen)
	}
	return out, nil
}

// generateMessageID generates a message id in Baileys' generateMessageID style:
// "3EB0" followed by 18 random bytes hex-encoded, upper-cased.
func generateMessageID() string {
	var b [18]byte
	if _, err := rand.Read(b[:]); err != nil {
		// rand failing is catastrophic; fall back to a sha of the time so we still
		// emit a unique-ish id rather than panicking the send path.
		h := sha256.Sum256([]byte(time.Now().String()))
		return "3EB0" + strings.ToUpper(hex.EncodeToString(h[:18]))
	}
	return "3EB0" + strings.ToUpper(hex.EncodeToString(b[:]))
}
