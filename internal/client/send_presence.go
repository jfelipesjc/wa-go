package client

import (
	"context"
	"errors"

	"github.com/jfelipesjc/wa-go/internal/wire"
)

// PresenceState is the global presence advertised via SendPresence.
type PresenceState string

const (
	// PresenceAvailable announces this device as online (Baileys 'available').
	PresenceAvailable PresenceState = "available"
	// PresenceUnavailable announces this device as offline (Baileys 'unavailable').
	PresenceUnavailable PresenceState = "unavailable"
)

// ChatState is the per-chat typing indicator sent via SendTyping.
type ChatState string

const (
	// ChatStateComposing signals "typing..." in a chat (Baileys 'composing').
	ChatStateComposing ChatState = "composing"
	// ChatStatePaused clears the typing indicator (Baileys 'paused').
	ChatStatePaused ChatState = "paused"
)

// SendPresence broadcasts the account's global presence. Mirrors Baileys'
// sendPresenceUpdate for 'available'/'unavailable': a top-level
// <presence type=...> stanza.
func (c *Client) SendPresence(ctx context.Context, state PresenceState) error {
	sess, ok := c.activeSession()
	if !ok {
		return errors.New("client: not logged in (no active session)")
	}
	return sess.send(presenceNode(state, sess.creds.Me))
}

// SendTyping sends a per-chat typing indicator (composing) or clears it
// (paused). Mirrors Baileys' sendPresenceUpdate('composing'|'paused', jid):
// a <chatstate to=jid><composing|paused/></chatstate> stanza.
func (c *Client) SendTyping(ctx context.Context, toJID string, state ChatState) error {
	sess, ok := c.activeSession()
	if !ok {
		return errors.New("client: not logged in (no active session)")
	}
	return sess.send(chatStateNode(toJID, state))
}

// SendReadReceipt marks the given message ids as read in toJID. participant is
// the message author for group reads (empty for 1:1). Mirrors Baileys'
// sendReceipt(jid, participant, ids, 'read'): a <receipt type=read to=jid
// [participant=...] id=firstId><list><item id=.../>...</list></receipt>.
func (c *Client) SendReadReceipt(ctx context.Context, toJID string, msgIDs []string, participant string) error {
	sess, ok := c.activeSession()
	if !ok {
		return errors.New("client: not logged in (no active session)")
	}
	if len(msgIDs) == 0 {
		return errors.New("client: read receipt requires at least one message id")
	}
	return sess.send(readReceiptNode(toJID, participant, msgIDs))
}

// --- pure node builders ---

// presenceNode builds a global presence stanza. Baileys includes the account's
// own name; we omit it (the server tolerates a bare type) but address it from
// our JID when known.
func presenceNode(state PresenceState, me string) wire.Node {
	attrs := map[string]string{"type": string(state)}
	if me != "" {
		attrs["from"] = me
	}
	return wire.Node{Tag: "presence", Attrs: attrs}
}

// chatStateNode builds a per-chat typing indicator stanza:
// <chatstate to=jid><composing|paused/></chatstate>.
func chatStateNode(toJID string, state ChatState) wire.Node {
	return wire.Node{
		Tag:   "chatstate",
		Attrs: map[string]string{"to": toJID},
		Content: []wire.Node{
			{Tag: string(state), Attrs: map[string]string{}},
		},
	}
}

// readReceiptNode builds a read receipt for one or more message ids. A single id
// is carried on the <receipt> directly; multiple ids add a <list><item.../></list>
// (Baileys' sendReceipts grouping).
func readReceiptNode(toJID, participant string, msgIDs []string) wire.Node {
	attrs := map[string]string{
		"to":   toJID,
		"type": "read",
		"id":   msgIDs[0],
	}
	if participant != "" {
		attrs["participant"] = participant
	}
	n := wire.Node{Tag: "receipt", Attrs: attrs}
	if len(msgIDs) > 1 {
		items := make([]wire.Node, 0, len(msgIDs)-1)
		for _, id := range msgIDs[1:] {
			items = append(items, wire.Node{Tag: "item", Attrs: map[string]string{"id": id}})
		}
		n.Content = []wire.Node{{Tag: "list", Attrs: map[string]string{}, Content: items}}
	}
	return n
}
