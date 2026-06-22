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

	"github.com/felipeleal/wa-go/internal/signal"
	"github.com/felipeleal/wa-go/internal/store"
	"github.com/felipeleal/wa-go/internal/waproto"
	"github.com/felipeleal/wa-go/internal/wire"
	"google.golang.org/protobuf/proto"
)

// sendIQTimeout bounds how long SendText waits for a usync / prekey-bundle reply
// before giving up, so a stalled server cannot hang the caller indefinitely.
var sendIQTimeout = 30 * time.Second

// SendText sends a 1:1 text message to toJID and returns the generated message
// id once the stanza has been written to the wire.
//
// Flow (mirrors Baileys' relayMessage for a non-group, non-status message):
//  1. Build a WAProto.Message{conversation: text}, serialize, pad (1..16).
//  2. usync the recipient's device list to enumerate target devices.
//  3. assertSessions: for devices without a session, fetch their prekey bundle
//     and run the X3DH initiator handshake.
//  4. Encrypt the padded plaintext per device, building <to jid><enc v=2 type>.
//  5. Wrap the participant nodes in <participants> inside a
//     <message id to type=text> and send it.
//
// Note on own devices: WhatsApp also encrypts a deviceSentMessage copy to the
// account's OTHER devices so they mirror the conversation. This implementation
// fans out to the RECIPIENT's devices only (the minimum for delivery); our own
// other devices are intentionally not targeted here. See the report for details.
func (c *Client) SendText(ctx context.Context, toJID, text string) (string, error) {
	sess, ok := c.activeSession()
	if !ok {
		return "", errors.New("client: not logged in (no active session)")
	}

	// Control Layer (B): consult the pacer before sending. Allow enforces the
	// rate limit; Wait sleeps a human-like delay proportional to the text length.
	// No pacer = original zero-delay behavior.
	if c.pacer != nil {
		if !c.pacer.Allow() {
			return "", errors.New("client: send rate limit exceeded")
		}
		if _, err := c.pacer.Wait(ctx, len(text)); err != nil {
			return "", fmt.Errorf("client: pacer wait: %w", err)
		}
	}

	msgID := generateMessageID()

	msg := &waproto.Message{Conversation: proto.String(text)}
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

	participantNodes, err := c.encryptForDevices(sess.creds, devices, plaintext)
	if err != nil {
		return "", err
	}
	if len(participantNodes) == 0 {
		return "", errors.New("client: no recipients could be encrypted for")
	}

	stanza := buildMessageStanza(msgID, toJID, participantNodes, sess.creds.Account)
	if err := sess.send(stanza); err != nil {
		return "", fmt.Errorf("client: send message: %w", err)
	}
	return msgID, nil
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
func (c *Client) encryptForDevices(creds *store.Creds, devices []deviceJID, plaintext []byte) ([]wire.Node, error) {
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
		nodes = append(nodes, toEncNode(d.JID, ct.Type, ct.Serialized))
	}
	if len(nodes) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return nodes, nil
}

// toEncNode builds a <to jid=...><enc v=2 type=...>ciphertext</enc></to> node, as
// Baileys' createParticipantNodes does per recipient device.
func toEncNode(jid, encType string, ciphertext []byte) wire.Node {
	return wire.Node{
		Tag:   "to",
		Attrs: map[string]string{"jid": jid},
		Content: []wire.Node{
			{
				Tag:     "enc",
				Attrs:   map[string]string{"v": "2", "type": encType},
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
func buildMessageStanza(msgID, toJID string, participants []wire.Node, account []byte) wire.Node {
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
			"type": "text",
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
