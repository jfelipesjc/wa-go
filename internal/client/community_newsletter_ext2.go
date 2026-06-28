// Package client: community_newsletter_ext2.go ports a second batch of Community
// and Newsletter protocol operations from Baileys (lib/Socket/communities.ts and
// newsletter.ts), cross-checked against whatsmeow (group.go / newsletter.go):
//
//   - CommunityCreateGroup              — create a new sub-group under a community.
//   - CommunityLinkedGroupsParticipants — list participants of all sub-groups.
//   - NewsletterSubscribed              — list channels the account follows/owns.
//   - AcceptTOSNotice                   — accept the channels TOS notice.
//   - NewsletterMarkViewed              — bump the view counter of channel messages.
//
// Transports reused: w:g2 group iq (buildGroupQuery / sendIQ) for the community
// create + linked-participants query, w:mex (runMexQuery) for the subscribed
// channel listing, a bare xmlns=tos iq for the TOS notice, and a fire-and-forget
// <receipt type=view> stanza (sess.send) for the view marker.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jfelipesjc/wa-go/internal/wire"
)

// Newsletter w:mex query ID / data path for listing subscribed channels, copied
// verbatim from Baileys (lib/Types/Mex.ts SUBSCRIBED_NEWSLETTERS). This is the
// web/browser ID; a desktop fingerprint would map it server-side differently.
const (
	nlQuerySubscribed = "6388546374527196"
	nlPathSubscribed  = "xwa2_newsletter_subscribed"
)

// ensureUserJID returns jid unchanged when it already carries a server suffix,
// otherwise it appends @s.whatsapp.net (sWhatsAppNet already includes the '@').
// Used to normalize bare phone numbers passed as community participants.
func ensureUserJID(jid string) string {
	if containsAt(jid) {
		return jid
	}
	return jid + sWhatsAppNet
}

// --- pure builders (testable without a session) ---

// buildCommunityCreateGroup is the pure constructor for the w:g2 stanza that
// creates a sub-group linked to a community (Baileys communities.ts createGroup
// + whatsmeow CreateGroup with a LinkedParentJID):
//
//	<iq xmlns=w:g2 type=set to=@g.us id=<id>>
//	  <create subject=<subject> key=<key>>
//	    <participant jid=.../>...
//	    <linked_parent jid=<communityJID>/>
//	  </create>
//	</iq>
//
// Each participant jid is normalized to a full @s.whatsapp.net address.
func buildCommunityCreateGroup(id, key, subject, communityJID string, participants []string) wire.Node {
	content := make([]wire.Node, 0, len(participants)+1)
	for _, p := range participants {
		content = append(content, wire.Node{
			Tag:   "participant",
			Attrs: map[string]string{"jid": ensureUserJID(p)},
		})
	}
	content = append(content, wire.Node{
		Tag:   "linked_parent",
		Attrs: map[string]string{"jid": communityJID},
	})
	create := wire.Node{
		Tag:     "create",
		Attrs:   map[string]string{"subject": subject, "key": key},
		Content: content,
	}
	return buildGroupQuery(id, "@g.us", "set", []wire.Node{create})
}

// buildLinkedGroupsParticipants is the pure constructor for the w:g2 query that
// lists the participants of every sub-group of a community (whatsmeow
// GetLinkedGroupsParticipants):
//
//	<iq xmlns=w:g2 type=get to=<communityJID> id=<id>>
//	  <linked_groups_participants/>
//	</iq>
func buildLinkedGroupsParticipants(id, communityJID string) wire.Node {
	return buildGroupQuery(id, communityJID, "get", []wire.Node{
		{Tag: "linked_groups_participants"},
	})
}

// parseLinkedGroupsParticipants extracts the participant jids from a
// linked_groups_participants reply. Returns a (possibly empty) slice of jids.
func parseLinkedGroupsParticipants(reply wire.Node) []string {
	container, ok := childByTag(reply, "linked_groups_participants")
	if !ok {
		return nil
	}
	parts := childrenByTag(container, "participant")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if jid := p.Attrs["jid"]; jid != "" {
			out = append(out, jid)
		}
	}
	return out
}

// buildAcceptTOSNotice is the pure constructor for the xmlns=tos iq that accepts
// the channels TOS notice (whatsmeow AcceptTOSNotice):
//
//	<iq xmlns=tos type=set to=@s.whatsapp.net id=<id>>
//	  <notice id=20601218 stage=5/>
//	</iq>
func buildAcceptTOSNotice(id string) wire.Node {
	return wire.Node{
		Tag: "iq",
		Attrs: map[string]string{
			"id":    id,
			"xmlns": "tos",
			"type":  "set",
			"to":    sWhatsAppNet,
		},
		Content: []wire.Node{
			{Tag: "notice", Attrs: map[string]string{"id": "20601218", "stage": "5"}},
		},
	}
}

// buildNewsletterMarkViewed is the pure constructor for the view receipt that
// bumps the view counter of one or more channel messages (whatsmeow
// NewsletterMarkViewed):
//
//	<receipt to=<jid> type=view id=<id>>
//	  <list>
//	    <item server_id=<sid>/>...
//	  </list>
//	</receipt>
func buildNewsletterMarkViewed(id, jid string, serverIDs []string) wire.Node {
	items := make([]wire.Node, 0, len(serverIDs))
	for _, sid := range serverIDs {
		items = append(items, wire.Node{
			Tag:   "item",
			Attrs: map[string]string{"server_id": sid},
		})
	}
	return wire.Node{
		Tag: "receipt",
		Attrs: map[string]string{
			"to":   jid,
			"type": "view",
			"id":   id,
		},
		Content: []wire.Node{
			{Tag: "list", Content: items},
		},
	}
}

// --- public methods ---

// CommunityCreateGroup creates a new sub-group under an existing community
// (communityJID, a parent group), seeded with the given subject and initial
// participants, and returns the new group's metadata. Mirrors Baileys'
// community create-group path and whatsmeow's CreateGroup with a LinkedParentJID.
func (c *Client) CommunityCreateGroup(ctx context.Context, communityJID, subject string, participants []string) (*GroupInfo, error) {
	sess, ok := c.activeSession()
	if !ok {
		return nil, errors.New("client: not logged in (no active session)")
	}
	if !isGroupJID(communityJID) {
		return nil, fmt.Errorf("client: %q is not a community (group) JID", communityJID)
	}
	req := buildCommunityCreateGroup(c.nextIQID("wa-go-community-"), generateMessageID(), subject, communityJID, participants)
	reply, err := c.sendIQ(ctx, sess, req)
	if err != nil {
		return nil, err
	}
	return parseGroupMetadata(reply)
}

// CommunityLinkedGroupsParticipants lists the JIDs of every participant across
// all sub-groups of a community. The result may be empty. Mirrors whatsmeow's
// GetLinkedGroupsParticipants.
func (c *Client) CommunityLinkedGroupsParticipants(ctx context.Context, communityJID string) ([]string, error) {
	sess, ok := c.activeSession()
	if !ok {
		return nil, errors.New("client: not logged in (no active session)")
	}
	if !isGroupJID(communityJID) {
		return nil, fmt.Errorf("client: %q is not a community (group) JID", communityJID)
	}
	req := buildLinkedGroupsParticipants(c.nextIQID("wa-go-community-"), communityJID)
	reply, err := c.sendIQ(ctx, sess, req)
	if err != nil {
		return nil, err
	}
	return parseLinkedGroupsParticipants(reply), nil
}

// NewsletterSubscribed lists the channels the account follows or owns. The w:mex
// reply is a JSON array of newsletter payloads; items that fail to parse are
// skipped. Mirrors whatsmeow's GetSubscribedNewsletters.
func (c *Client) NewsletterSubscribed(ctx context.Context) ([]*NewsletterInfo, error) {
	raw, err := c.runMexQuery(ctx, nlQuerySubscribed, nlPathSubscribed, map[string]any{})
	if err != nil {
		return nil, err
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("client: decode subscribed newsletters: %w", err)
	}
	out := make([]*NewsletterInfo, 0, len(items))
	for _, item := range items {
		info, err := parseNewsletter(item)
		if err != nil {
			continue // skip malformed entries
		}
		out = append(out, info)
	}
	return out, nil
}

// AcceptTOSNotice accepts the WhatsApp Channels TOS notice. This is a
// prerequisite for creating a channel on a fresh account. Success is the absence
// of an error (the iq reply is discarded). Mirrors whatsmeow's AcceptTOSNotice.
func (c *Client) AcceptTOSNotice(ctx context.Context) error {
	sess, ok := c.activeSession()
	if !ok {
		return errors.New("client: not logged in (no active session)")
	}
	req := buildAcceptTOSNotice(c.nextIQID("wa-go-tos-"))
	_, err := c.sendIQ(ctx, sess, req)
	return err
}

// NewsletterMarkViewed bumps the view counter of one or more channel messages,
// identified by their server ids. This is a fire-and-forget <receipt type=view>
// stanza (there is no iq reply). Mirrors whatsmeow's NewsletterMarkViewed.
func (c *Client) NewsletterMarkViewed(ctx context.Context, jid string, serverIDs []string) error {
	sess, ok := c.activeSession()
	if !ok {
		return errors.New("client: not logged in (no active session)")
	}
	if !isNewsletterJID(jid) {
		return fmt.Errorf("client: %q is not a newsletter JID", jid)
	}
	if len(serverIDs) == 0 {
		return errors.New("client: NewsletterMarkViewed requires at least one server id")
	}
	return sess.send(buildNewsletterMarkViewed(c.nextIQID("wa-go-view-"), jid, serverIDs))
}
