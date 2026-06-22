// Package client: notifications.go turns inbound <notification> and <receipt>
// stanzas into the granular typed events defined in events.go. It mirrors
// Baileys' Socket/messages-recv.js (processNotification / handleGroupNotification
// / handleReceipt): the read loop still acks every stanza, this file adds the
// event emission so the API layer (#8) can surface rich webhooks.
package client

import (
	"fmt"
	"strconv"

	"github.com/felipeleal/wa-go/internal/wire"
)

// minPreKeyCount mirrors Baileys' MIN_PREKEY_COUNT: when the server reports our
// stock of one-time pre-keys has fallen below this, we re-upload a fresh batch so
// peers can keep starting sessions with us.
const minPreKeyCount = 5

// handleNotification classifies an inbound <notification> stanza and emits the
// matching granular event. It mirrors Baileys' processNotification: a type-switch
// on the `type` attr. Unknown types still surface a generic NotificationEvent so
// nothing is silently dropped. Acking the stanza stays in the read loop.
func (c *Client) handleNotification(node wire.Node) {
	from := node.Attrs["from"]
	switch node.Attrs["type"] {
	case "w:gp2":
		c.handleGroupNotification(node)
	case "encrypt":
		c.handleEncryptNotification(node)
	case "picture":
		c.handlePictureNotification(node)
	case "account_sync", "server_sync":
		c.handleAppStateSyncNotification(node)
	case "contacts", "devices":
		c.emit(ContactUpdateEvent{JID: from, Type: node.Attrs["type"]})
	default:
		c.emit(NotificationEvent{Type: node.Attrs["type"], From: from, Raw: node})
	}
}

// handleGroupNotification parses a <notification type=w:gp2 from=group> and emits
// a GroupParticipantsUpdateEvent (membership/role change) or a GroupUpdateEvent
// (subject/announce/locked/ephemeral/description), per its first child tag. The
// structure is:
//
//	<notification type=w:gp2 from=group@g.us participant=actor>
//	  <add|remove|promote|demote|leave|modify>
//	    <participant jid=member@s.whatsapp.net/> ...
//	  </add>
//	  -- or --
//	  <subject subject="New name"/>
//	  <announcement/> | <not_announcement/>
//	  <locked/> | <unlocked/>
//	  <ephemeral expiration=N/> | <not_ephemeral/>
//	  <description><body>text</body></description>
//	</notification>
func (c *Client) handleGroupNotification(node wire.Node) {
	group := node.Attrs["from"]
	by := node.Attrs["participant"]

	kids := children(node)
	if len(kids) == 0 {
		c.emit(NotificationEvent{Type: "w:gp2", From: group, Raw: node})
		return
	}
	child := kids[0]

	switch child.Tag {
	case "add", "remove", "promote", "demote", "leave", "modify":
		var parts []string
		for _, p := range childrenByTag(child, "participant") {
			if jid := p.Attrs["jid"]; jid != "" {
				parts = append(parts, jid)
			}
		}
		c.emit(GroupParticipantsUpdateEvent{
			GroupJID:     group,
			Action:       GroupParticipantAction(child.Tag),
			Participants: parts,
			By:           by,
		})
	case "subject":
		subj := child.Attrs["subject"]
		c.emit(GroupUpdateEvent{GroupJID: group, By: by, Subject: &subj})
	case "announcement", "not_announcement":
		v := child.Tag == "announcement"
		c.emit(GroupUpdateEvent{GroupJID: group, By: by, Announce: &v})
	case "locked", "unlocked":
		v := child.Tag == "locked"
		c.emit(GroupUpdateEvent{GroupJID: group, By: by, Locked: &v})
	case "ephemeral", "not_ephemeral":
		var exp uint32
		if child.Tag == "ephemeral" {
			if v, err := strconv.ParseUint(child.Attrs["expiration"], 10, 32); err == nil {
				exp = uint32(v)
			}
		}
		c.emit(GroupUpdateEvent{GroupJID: group, By: by, Ephemeral: &exp})
	case "description":
		desc := ""
		if body, ok := childByTag(child, "body"); ok {
			desc = string(nodeBytes(body))
		}
		c.emit(GroupUpdateEvent{GroupJID: group, By: by, Description: &desc})
	default:
		// Unrecognized w:gp2 child (create/invite/membership_*): surface raw so the
		// caller can still react.
		c.emit(NotificationEvent{Type: "w:gp2", From: group, Raw: node})
	}
}

// handleEncryptNotification handles a <notification type=encrypt> from the server
// reporting our one-time pre-key count. When it has fallen below minPreKeyCount we
// re-upload a fresh batch (Baileys' handleEncryptNotification). The upload reuses
// the live session's send; if there is no live session the re-upload is skipped.
func (c *Client) handleEncryptNotification(node wire.Node) {
	countChild, ok := childByTag(node, "count")
	if !ok {
		c.emit(NotificationEvent{Type: "encrypt", From: node.Attrs["from"], Raw: node})
		return
	}
	count, err := strconv.Atoi(countChild.Attrs["value"])
	if err != nil {
		return
	}
	if count >= minPreKeyCount {
		return
	}
	sess, ok := c.activeSession()
	if !ok {
		return
	}
	if err := c.uploadPreKeys(sess.send, sess.creds); err != nil && debugPairing {
		fmt.Fprintf(debugOut, "[wa-go] re-upload pre-keys: %v\n", err)
	}
}

// handlePictureNotification parses a <notification type=picture> with a <set> or
// <delete> child and emits a PictureUpdateEvent. JID is the subject (from), Author
// is the acting JID, Removed is true for a delete.
func (c *Client) handlePictureNotification(node wire.Node) {
	jid := node.Attrs["from"]
	ev := PictureUpdateEvent{JID: jid}
	if set, ok := childByTag(node, "set"); ok {
		ev.Author = set.Attrs["author"]
	} else if del, ok := childByTag(node, "delete"); ok {
		ev.Author = del.Attrs["author"]
		ev.Removed = true
	}
	c.emit(ev)
}

// handleAppStateSyncNotification parses a <notification type=account_sync|server_sync>
// and emits an AppStateSyncDirtyEvent naming the dirty collections. For a
// server_sync the collections come from <collection name=.../> children; for an
// account_sync (which has no collection children) the first child tag names the
// dirty area. It does NOT force a resync — that policy is left to the consumer.
func (c *Client) handleAppStateSyncNotification(node wire.Node) {
	var cols []string
	for _, col := range childrenByTag(node, "collection") {
		if n := col.Attrs["name"]; n != "" {
			cols = append(cols, n)
		}
	}
	if len(cols) == 0 {
		if kids := children(node); len(kids) > 0 && kids[0].Tag != "" {
			cols = append(cols, kids[0].Tag)
		}
	}
	c.emit(AppStateSyncDirtyEvent{Collections: cols})
}

// handleReceiptUpdate emits a ReceiptUpdateEvent for a (non-retry) <receipt>
// stanza. It mirrors Baileys' handleReceipt status emission: a receipt with no
// `type` is a delivery; type=read/read-self is a read; type=played is a played
// receipt. The covered message IDs are the stanza `id` plus any batched
// <list><item id=.../> children. Retry receipts are handled separately by the
// resend path and produce no ReceiptUpdateEvent.
func (c *Client) handleReceiptUpdate(node wire.Node) {
	rt, ok := receiptUpdateType(node.Attrs["type"])
	if !ok {
		return
	}
	ids := []string{node.Attrs["id"]}
	if list, ok := childByTag(node, "list"); ok {
		for _, item := range childrenByTag(list, "item") {
			if id := item.Attrs["id"]; id != "" {
				ids = append(ids, id)
			}
		}
	}
	c.emit(ReceiptUpdateEvent{
		For:         ids,
		From:        node.Attrs["from"],
		Participant: node.Attrs["participant"],
		Type:        rt,
		Timestamp:   parseTimestamp(node.Attrs["t"]),
	})
}

// receiptUpdateType maps a <receipt> `type` attr to a ReceiptType. It returns
// ok=false for receipt types that are not delivery/read/played status updates
// (e.g. "retry", "sender"), which are handled elsewhere.
func receiptUpdateType(t string) (ReceiptType, bool) {
	switch t {
	case "":
		return ReceiptDelivery, true
	case "read", "read-self":
		return ReceiptRead, true
	case "played":
		return ReceiptPlayed, true
	default:
		return "", false
	}
}
