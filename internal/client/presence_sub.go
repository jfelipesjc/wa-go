// Package client: presence_sub.go implements presence subscription (asking the
// server to forward a contact's online/typing state) and the parse of inbound
// <presence>/<chatstate> stanzas into PresenceEvent. It mirrors Baileys'
// presenceSubscribe (a <presence type=subscribe to=jid/> stanza) and the
// CB:presence / CB:chatstate handlers in Socket/messages-recv.js.
package client

import (
	"context"
	"errors"
	"strconv"

	"github.com/jfelipesjc/wa-go/internal/wire"
)

// SubscribePresence asks the server to start forwarding presence updates for the
// given contact JID. Until subscribed, the server does not push a contact's
// online/last-seen/typing state. Mirrors Baileys' presenceSubscribe: a top-level
// <presence type=subscribe to=jid/> stanza (fire-and-forget, no iq reply).
func (c *Client) SubscribePresence(ctx context.Context, jid string) error {
	if jid == "" {
		return errors.New("client: SubscribePresence requires a jid")
	}
	sess, ok := c.activeSession()
	if !ok {
		return errors.New("client: SubscribePresence requires a live session")
	}
	return sess.send(presenceSubscribeNode(jid))
}

// presenceSubscribeNode builds the <presence type=subscribe to=jid/> stanza.
func presenceSubscribeNode(jid string) wire.Node {
	return wire.Node{
		Tag:   "presence",
		Attrs: map[string]string{"type": "subscribe", "to": jid},
	}
}

// parsePresenceNode parses an inbound <presence>/<chatstate> stanza into a
// PresenceEvent. It is pure so it can be unit-tested and reused by the read loop.
//
// A <presence> carries the peer in `from`; its `type` is "unavailable" (offline,
// often with a `last` last-seen timestamp) or absent (online → "available").
//
// A <chatstate> carries the peer in `from` and a single child whose tag is the
// state ("composing"/"paused"). It returns ok=false for any other node.
func parsePresenceNode(node wire.Node) (PresenceEvent, bool) {
	switch node.Tag {
	case "presence":
		ev := PresenceEvent{From: node.Attrs["from"]}
		switch node.Attrs["type"] {
		case "unavailable":
			ev.State = string(PresenceUnavailable)
		case "", "available":
			ev.State = string(PresenceAvailable)
		default:
			ev.State = node.Attrs["type"]
		}
		if last := node.Attrs["last"]; last != "" && last != "deny" {
			if v, err := strconv.ParseInt(last, 10, 64); err == nil {
				ev.LastSeen = v
			}
		}
		return ev, true
	case "chatstate":
		ev := PresenceEvent{From: node.Attrs["from"]}
		if kids := children(node); len(kids) > 0 {
			ev.State = kids[0].Tag // composing / paused
		}
		if ev.State == "" {
			return PresenceEvent{}, false
		}
		return ev, true
	default:
		return PresenceEvent{}, false
	}
}
