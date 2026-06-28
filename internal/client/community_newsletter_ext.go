// Package client: community_newsletter_ext.go ports the last few Community and
// Newsletter protocol operations from Baileys (lib/Socket/communities.ts and
// newsletter.ts), cross-checked against whatsmeow (newsletter.go / group.go):
//
//   - CommunityFetchAllParticipating — list every community the account is in.
//   - NewsletterDelete               — deactivate an owned channel.
//   - NewsletterSubscriberCount       — fetch a channel's subscriber count.
//   - NewsletterReactMessage          — react to a channel message by server id.
//
// Transports reused from the existing code: w:g2 group iq (buildGroupQuery /
// sendIQ) for the community listing, w:mex (runMexQuery) for the newsletter
// mutation/metadata, and a bare fire-and-forget <message> stanza (sess.send)
// for the channel reaction (newsletters are unencrypted, so no Signal path).
package client

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jfelipesjc/wa-go/internal/waproto"
	"github.com/jfelipesjc/wa-go/internal/wire"
	"google.golang.org/protobuf/proto"
)

// Additional newsletter w:mex query ID / data path, copied verbatim from
// Baileys (lib/Types/Mex.ts). This is the web/browser ID; a desktop/mac
// fingerprint would use a different id server-side (whatsmeow's convertQueryID).
const (
	nlQueryDelete = "30062808666639665"
	nlPathDelete  = "xwa2_newsletter_delete_v2"
)

// isNewsletterJID reports whether jid addresses a channel (ends in @newsletter).
func isNewsletterJID(jid string) bool {
	return strings.HasSuffix(jid, "@newsletter")
}

// CommunityFetchAllParticipating returns the JID and subject of every community
// the account participates in. It issues the same w:g2 <participating> query as
// FetchAllGroups; the server returns communities and regular groups together
// under the <groups> wrapper, with communities distinguished by a <parent>
// child (Baileys' isCommunity / extractCommunityMetadata key). This filters to
// just the parent communities.
func (c *Client) CommunityFetchAllParticipating(ctx context.Context) ([]GroupLinkInfo, error) {
	sess, ok := c.activeSession()
	if !ok {
		return nil, errors.New("client: not logged in (no active session)")
	}
	req := buildGroupQuery(c.nextIQID("wa-go-community-"), "@g.us", "get", []wire.Node{
		{
			Tag:   "participating",
			Attrs: map[string]string{},
			Content: []wire.Node{
				{Tag: "participants", Attrs: map[string]string{}},
				{Tag: "description", Attrs: map[string]string{}},
			},
		},
	})
	reply, err := c.sendIQ(ctx, sess, req)
	if err != nil {
		return nil, err
	}
	return parseAllCommunities(reply), nil
}

// parseAllCommunities extracts the parent communities from a w:g2 participating
// reply: every <group> under <groups> that carries a <parent> marker, using the
// same jid/subject extraction as parseSubGroups.
func parseAllCommunities(reply wire.Node) []GroupLinkInfo {
	container, ok := childByTag(reply, "groups")
	if !ok {
		return nil
	}
	groups := childrenByTag(container, "group")
	out := make([]GroupLinkInfo, 0)
	for _, g := range groups {
		if _, isCommunity := childByTag(g, "parent"); !isCommunity {
			continue // a regular group, not a community
		}
		jid := g.Attrs["jid"]
		if jid == "" {
			jid = g.Attrs["id"]
		}
		if jid == "" {
			continue
		}
		if !containsAt(jid) {
			jid += "@g.us"
		}
		out = append(out, GroupLinkInfo{JID: jid, Subject: g.Attrs["subject"]})
	}
	return out
}

// NewsletterDelete deactivates a channel the account owns. This is IRREVERSIBLE.
// Mirrors Baileys' newsletterDelete: a w:mex mutation whose reply is discarded
// (success = absence of a GraphQL error).
func (c *Client) NewsletterDelete(ctx context.Context, jid string) error {
	if !isNewsletterJID(jid) {
		return fmt.Errorf("client: %q is not a newsletter JID", jid)
	}
	_, err := c.runMexQuery(ctx, nlQueryDelete, nlPathDelete, newsletterIDVariables(jid))
	return err
}

// NewsletterSubscriberCount returns the number of subscribers of a channel. The
// dedicated subscribers w:mex query is admin-list shaped and does not carry a
// count, so this reads the subscriber_count already present in the channel
// metadata (Baileys surfaces the same value).
func (c *Client) NewsletterSubscriberCount(ctx context.Context, jid string) (int, error) {
	if !isNewsletterJID(jid) {
		return 0, fmt.Errorf("client: %q is not a newsletter JID", jid)
	}
	info, err := c.NewsletterMetadata(ctx, jid, NewsletterKeyJID)
	if err != nil {
		return 0, err
	}
	if info == nil {
		return 0, nil
	}
	return info.SubscriberCount, nil
}

// SendNewsletterText posts a plain text message to a channel (newsletter) and
// returns the server-assigned message id (server_id), which can be passed to
// NewsletterReactMessage. Newsletters are unencrypted, so the message goes out as
// a bare <message type=text><plaintext>{proto bytes}</plaintext></message>
// (mirroring whatsmeow's sendNewsletter — no Signal, no padding). The server
// replies with an <ack id=..> carrying the server_id, which we await and return.
func (c *Client) SendNewsletterText(ctx context.Context, jid, text string) (string, error) {
	if !isNewsletterJID(jid) {
		return "", fmt.Errorf("client: %q is not a newsletter JID", jid)
	}
	return c.sendNewsletterMessage(ctx, jid, buildTextMessage(text), sendOpts{})
}

// sendNewsletterMessage is the shared core for posting to a channel: it is used
// by SendNewsletterText and by the @newsletter branch of sendRouted (so any
// Send* helper aimed at a channel routes here). Channels are unencrypted, so the
// serialized proto goes out in a <plaintext> child with no Signal/padding; the
// server replies with an <ack id=..> carrying the server_id, which is returned.
func (c *Client) sendNewsletterMessage(ctx context.Context, jid string, msg *waproto.Message, opts sendOpts) (string, error) {
	sess, ok := c.activeSession()
	if !ok {
		return "", errors.New("client: not logged in (no active session)")
	}
	plaintext, err := proto.Marshal(msg)
	if err != nil {
		return "", fmt.Errorf("client: marshal newsletter message: %w", err)
	}
	stanzaType := opts.stanzaType
	if stanzaType == "" {
		stanzaType = "text"
	}
	msgID := generateMessageID()
	stanza := wire.Node{
		Tag:   "message",
		Attrs: map[string]string{"to": jid, "id": msgID, "type": stanzaType},
		Content: []wire.Node{
			{Tag: "plaintext", Content: plaintext},
		},
	}
	// Register the ack waiter BEFORE sending so a fast ack isn't missed.
	ch, cancel := c.registerIQ(msgID)
	defer cancel()
	if err := sess.send(stanza); err != nil {
		return "", fmt.Errorf("client: send newsletter message: %w", err)
	}
	timer := time.NewTimer(iqTimeout)
	defer timer.Stop()
	select {
	case <-timer.C:
		return "", fmt.Errorf("client: newsletter send %s timed out (no ack)", msgID)
	case <-ctx.Done():
		return "", ctx.Err()
	case ack, ok := <-ch:
		if !ok {
			return "", errors.New("client: connection closed before newsletter ack")
		}
		if ec := ack.Attrs["error"]; ec != "" && ec != "0" {
			return "", fmt.Errorf("client: newsletter send rejected (error=%s)", ec)
		}
		sid := ack.Attrs["server_id"]
		if sid == "" {
			return "", errors.New("client: newsletter ack missing server_id")
		}
		return sid, nil
	}
}

// NewsletterReactMessage reacts to a channel message identified by its
// server-assigned id (serverID). An empty emoji removes a previous reaction.
// Channels are unencrypted, so this is a bare <message type=reaction> stanza
// sent fire-and-forget — mirroring whatsmeow's NewsletterSendReaction and
// Baileys' newsletterReactMessage.
func (c *Client) NewsletterReactMessage(ctx context.Context, jid, serverID, emoji string) error {
	sess, ok := c.activeSession()
	if !ok {
		return errors.New("client: not logged in (no active session)")
	}
	if !isNewsletterJID(jid) {
		return fmt.Errorf("client: %q is not a newsletter JID", jid)
	}
	if serverID == "" {
		return errors.New("client: NewsletterReactMessage requires a server message id")
	}
	return sess.send(buildNewsletterReaction(c.nextIQID("wa-go-nl-react-"), jid, serverID, emoji))
}

// buildNewsletterReaction is the pure constructor for a channel reaction stanza:
//
//	<message to=<jid> id=<tag> server_id=<serverID> type=reaction [edit=7]>
//	  <reaction code=<emoji>/>          (when adding)
//	  <reaction/>                        (when removing — empty, plus edit=7)
//	</message>
//
// edit="7" (Baileys' EditAttributeSenderRevoke) is set only when removing.
func buildNewsletterReaction(tag, jid, serverID, emoji string) wire.Node {
	attrs := map[string]string{
		"to":        jid,
		"id":        tag,
		"server_id": serverID,
		"type":      "reaction",
	}
	reaction := wire.Node{Tag: "reaction"}
	if emoji == "" {
		// Removal: empty <reaction/> and edit=7 on the message.
		attrs["edit"] = "7"
	} else {
		reaction.Attrs = map[string]string{"code": emoji}
	}
	return wire.Node{
		Tag:     "message",
		Attrs:   attrs,
		Content: []wire.Node{reaction},
	}
}
