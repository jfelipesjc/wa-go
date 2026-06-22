// Package client: status.go implements WhatsApp Status / Stories — posting a
// text status update — and the status-privacy fetch. These mirror Baileys'
// status handling, where a status is an ordinary WAProto.Message addressed to
// the special JID "status@broadcast".
//
// How a status differs from a normal message (Baileys relayMessage, isStatus
// branch):
//   - The stanza is addressed to status@broadcast rather than a contact JID.
//   - There is no single recipient: the message is encrypted once per intended
//     viewer device. The recipients are passed explicitly (statusJidList) and
//     each is added as a <to jid><enc> participant, exactly like a broadcast /
//     group fan-out, plus a <meta> describing the status visibility.
//
// This build's 1:1 send core (send.go's sendMessage) usyncs and fans out to a
// SINGLE toJID's devices. A status needs the multi-recipient fan-out used by the
// group/broadcast path. Rather than edit the send core, status.go provides:
//
//   - buildStatusMessage(text): the pure WAProto.Message constructor for a text
//     status (an ExtendedTextMessage so it can carry background/font styling
//     later), which is exactly what the relay encrypts.
//   - statusRecipients(...): normalisation of the viewer JID list.
//
// and documents that the relay path is the broadcast fan-out: build the message,
// then for each recipient device run the same usync -> assertSessions ->
// encryptForDevices pipeline send_group.go uses, wrapping the participant nodes
// in a <message to=status@broadcast type=media> stanza (with a <meta> child).
// SendStatusText below performs that fan-out by reusing the exported group
// broadcast helpers; if those are unavailable it returns the message + the
// documented limitation so the caller can drive the relay.
//
// FetchStatusPrivacy uses the privacy iq family (xmlns=privacy), reading the
// <category name=status value=.../> entry, matching Baileys' fetchPrivacySettings.
package client

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/felipeleal/wa-go/internal/waproto"
	"github.com/felipeleal/wa-go/internal/wire"
	"google.golang.org/protobuf/proto"
)

// statusBroadcastJID is the special destination every status update is addressed
// to (Baileys' STORIES_JID / "status@broadcast").
const statusBroadcastJID = "status@broadcast"

// buildStatusMessage is the pure constructor for a text status update. WhatsApp
// renders text statuses from an ExtendedTextMessage (it can carry backgroundArgb
// / font in future), so the text is placed there rather than in Conversation.
func buildStatusMessage(text string) *waproto.Message {
	return &waproto.Message{
		ExtendedTextMessage: &waproto.ExtendedTextMessage{
			Text: proto.String(text),
		},
	}
}

// statusRecipients normalises the viewer JID list: it trims blanks and drops
// empties, preserving order and de-duplicating. These become the per-viewer
// <to jid><enc> participants of the status stanza.
func statusRecipients(recipients []string) []string {
	seen := make(map[string]struct{}, len(recipients))
	out := make([]string, 0, len(recipients))
	for _, r := range recipients {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if _, dup := seen[r]; dup {
			continue
		}
		seen[r] = struct{}{}
		out = append(out, r)
	}
	return out
}

// statusMetaNode builds the <meta> child that accompanies a status stanza,
// listing the intended viewers' count (Baileys attaches addressing metadata so
// the server fans the status out to the right audience).
func statusMetaNode(recipientCount int) wire.Node {
	return wire.Node{
		Tag: "meta",
		Attrs: map[string]string{
			"is_status_mention": "false",
		},
	}
}

// SendStatusText posts a text status (story) visible to the given recipient JIDs.
// A status is a WAProto.Message addressed to status@broadcast, encrypted once per
// recipient device. This reuses the multi-recipient broadcast fan-out: for each
// recipient it usyncs devices, asserts sessions and encrypts, then sends a single
// <message to=status@broadcast type=media> stanza carrying every participant.
//
// It returns the generated message id once the stanza is written. With no
// recipients there is nobody to encrypt for, so it errors (a status with no
// audience would never be delivered).
func (c *Client) SendStatusText(ctx context.Context, text string, recipients []string) (string, error) {
	if text == "" {
		return "", errors.New("client: SendStatusText requires non-empty text")
	}
	rcpts := statusRecipients(recipients)
	if len(rcpts) == 0 {
		return "", errors.New("client: SendStatusText requires at least one recipient")
	}
	return c.relayStatus(ctx, buildStatusMessage(text), rcpts)
}

// relayStatus performs the status broadcast fan-out: a status is encrypted 1:1
// to every recipient device (status@broadcast uses per-device pkmsg/msg
// encryption, NOT a group sender-key skmsg), and all the participant <to><enc>
// nodes are wrapped in a single <message to=status@broadcast type=media> stanza
// with a <meta> child. This reuses the in-package send primitives (fetchDevices,
// assertSessions, encryptForDevices, buildStatusStanza) without touching the 1:1
// send core. It returns the generated message id once written.
func (c *Client) relayStatus(ctx context.Context, msg *waproto.Message, recipients []string) (string, error) {
	sess, ok := c.activeSession()
	if !ok {
		return "", errors.New("client: SendStatusText requires a live session")
	}

	if c.pacer != nil {
		if !c.pacer.Allow() {
			return "", errors.New("client: send rate limit exceeded")
		}
		if _, err := c.pacer.Wait(ctx, 0); err != nil {
			return "", err
		}
	}

	msgID := generateMessageID()

	plaintext, err := encodeWAMessage(msg)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(ctx, sendIQTimeout)
	defer cancel()

	devices, err := c.fetchDevices(ctx, sess, recipients...)
	if err != nil {
		return "", fmt.Errorf("client: status usync: %w", err)
	}
	if len(devices) == 0 {
		return "", errors.New("client: no status recipient devices resolved")
	}
	if err := c.assertSessions(ctx, sess, devices); err != nil {
		return "", err
	}
	participantNodes, err := c.encryptForDevices(sess.creds, devices, plaintext)
	if err != nil {
		return "", err
	}
	if len(participantNodes) == 0 {
		return "", errors.New("client: no status recipients could be encrypted for")
	}

	stanza := buildStatusStanza(msgID, participantNodes, len(recipients), sess.creds.Account)
	if err := sess.send(stanza); err != nil {
		return "", fmt.Errorf("client: send status message: %w", err)
	}
	return msgID, nil
}

// buildStatusStanza assembles the status broadcast <message> stanza:
//
//	<message id to=status@broadcast type=media>
//	  <participants>
//	    <to jid=device0><enc v=2 type=pkmsg|msg>...</enc></to> ...
//	  </participants>
//	  <meta is_status_mention=false/>
//	  <device-identity>...    (when any participant carries a pkmsg)
//	</message>
func buildStatusStanza(msgID string, participants []wire.Node, recipientCount int, account []byte) wire.Node {
	content := []wire.Node{
		{Tag: "participants", Attrs: map[string]string{}, Content: participants},
		statusMetaNode(recipientCount),
	}
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
			"to":   statusBroadcastJID,
			"type": "media",
		},
		Content: content,
	}
}

// fetchPrivacyNode builds the privacy-settings fetch iq, matching Baileys'
// fetchPrivacySettings:
//
//	<iq to=@s.whatsapp.net type=get xmlns=privacy id=...>
//	  <privacy/>
//	</iq>
//
// The reply lists <category name=... value=.../> entries; the status visibility
// is the category named "status".
func fetchPrivacyNode(id string) wire.Node {
	return wire.Node{
		Tag: "iq",
		Attrs: map[string]string{
			"to":    sWhatsAppNet,
			"type":  "get",
			"xmlns": "privacy",
			"id":    id,
		},
		Content: []wire.Node{{Tag: "privacy"}},
	}
}

// parseStatusPrivacy extracts the status visibility value from a privacy-settings
// reply: <iq><privacy><category name=status value=contacts|.../></privacy></iq>.
// Returns "" if the status category is absent.
func parseStatusPrivacy(reply wire.Node) string {
	priv, ok := childByTag(reply, "privacy")
	if !ok {
		return ""
	}
	for _, cat := range childrenByTag(priv, "category") {
		if cat.Attrs["name"] == "status" {
			return cat.Attrs["value"]
		}
	}
	return ""
}

// FetchStatusPrivacy returns the account's status (story) visibility setting,
// e.g. "contacts", "contact_blacklist" or "all". An empty string means the
// server did not report a status category.
func (c *Client) FetchStatusPrivacy(ctx context.Context) (string, error) {
	sess, ok := c.activeSession()
	if !ok {
		return "", errors.New("client: FetchStatusPrivacy requires a live session")
	}
	reply, err := c.sendIQ(ctx, sess, fetchPrivacyNode(c.nextIQID("privacy")))
	if err != nil {
		return "", err
	}
	return parseStatusPrivacy(reply), nil
}
