// Package client: community.go implements WhatsApp Communities — the parent
// groups that link a set of sub-groups together.
//
// Communities reuse the group transport ("w:g2", same as group.go's
// buildGroupQuery). A community is itself a group JID whose metadata carries a
// <parent> marker; its members are sub-groups linked via <links>/<link>.
//
// Confirmed in Baileys (lib/Socket/groups.js extractGroupMetadata):
//
//	isCommunity      = presence of a <parent> child on the <group> node
//	linkedParent     = <linked_parent jid=.../> attr (a sub-group's parent)
//
// Baileys (the pinned version) does not ship getSubGroups / link / unlink, so
// the sub-group and (un)link envelopes below follow the whatsmeow / WhatsApp
// w:g2 wire conventions:
//
//	CommunitySubGroups:
//	  <iq xmlns=w:g2 type=get to=community><sub_groups/></iq>
//	reply: <iq ...><sub_groups><group jid=... subject=.../>...</sub_groups></iq>
//
//	LinkGroup:
//	  <iq xmlns=w:g2 type=set to=community>
//	    <links><link link_type=sub_group><group id=<group}/></link></links>
//	  </iq>
//	reply: <iq ...><links><link link_type=sub_group>
//	         <group id=... /><error code=.../?></link></links></iq>
//
//	UnlinkGroup:
//	  <iq xmlns=w:g2 type=set to=community>
//	    <unlink unlink_type=sub_group><group id=<group}/></unlink>
//	  </iq>
package client

import (
	"context"
	"errors"
	"fmt"

	"github.com/felipeleal/wa-go/internal/wire"
)

// linkTypeSubGroup is the link_type used to attach a group to a community.
const linkTypeSubGroup = "sub_group"

// GroupLinkInfo is one sub-group linked into a community, as returned by
// CommunitySubGroups.
type GroupLinkInfo struct {
	JID     string
	Subject string
}

// --- node builders (pure; testable without a session) ---

// buildSubGroupsQuery builds the sub-group listing iq:
//
//	<iq xmlns=w:g2 type=get to=community><sub_groups/></iq>
func buildSubGroupsQuery(id, communityJID string) wire.Node {
	return buildGroupQuery(id, communityJID, "get", []wire.Node{
		{Tag: "sub_groups"},
	})
}

// buildLinkGroupQuery builds the link iq:
//
//	<iq xmlns=w:g2 type=set to=community>
//	  <links><link link_type=sub_group><group id=<group}/></link></links>
//	</iq>
func buildLinkGroupQuery(id, communityJID, groupJID string) wire.Node {
	return buildGroupQuery(id, communityJID, "set", []wire.Node{
		{
			Tag: "links",
			Content: []wire.Node{
				{
					Tag:   "link",
					Attrs: map[string]string{"link_type": linkTypeSubGroup},
					Content: []wire.Node{
						{Tag: "group", Attrs: map[string]string{"id": groupJID}},
					},
				},
			},
		},
	})
}

// buildUnlinkGroupQuery builds the unlink iq:
//
//	<iq xmlns=w:g2 type=set to=community>
//	  <unlink unlink_type=sub_group><group id=<group}/></unlink>
//	</iq>
func buildUnlinkGroupQuery(id, communityJID, groupJID string) wire.Node {
	return buildGroupQuery(id, communityJID, "set", []wire.Node{
		{
			Tag:   "unlink",
			Attrs: map[string]string{"unlink_type": linkTypeSubGroup},
			Content: []wire.Node{
				{Tag: "group", Attrs: map[string]string{"id": groupJID}},
			},
		},
	})
}

// --- response parsing ---

// parseSubGroups extracts the linked sub-groups from a <sub_groups> reply,
// each carried as a <group jid=... subject=.../> child.
func parseSubGroups(reply wire.Node) []GroupLinkInfo {
	container, ok := childByTag(reply, "sub_groups")
	if !ok {
		return nil
	}
	groups := childrenByTag(container, "group")
	out := make([]GroupLinkInfo, 0, len(groups))
	for _, g := range groups {
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

// parseLinkResult inspects a link/unlink reply for a per-group <error code=.../>
// and returns a non-nil error when the server rejected the (un)link.
func parseLinkResult(reply wire.Node, container string) error {
	c, ok := childByTag(reply, container)
	if !ok {
		// No explicit container echoed back: treat as success (server sent a
		// bare <iq type=result/>).
		return nil
	}
	// The error may sit directly under the container or under a <link> child.
	if err := linkError(c); err != nil {
		return err
	}
	for _, link := range childrenByTag(c, "link") {
		if err := linkError(link); err != nil {
			return err
		}
	}
	return nil
}

// linkError returns an error if node has an <error code=.../> child.
func linkError(node wire.Node) error {
	e, ok := childByTag(node, "error")
	if !ok {
		return nil
	}
	code := e.Attrs["code"]
	if code == "" || code == "200" {
		return nil
	}
	return fmt.Errorf("client: community link failed (code %s)", code)
}

// containsAt reports whether jid already carries a server suffix.
func containsAt(jid string) bool {
	for i := 0; i < len(jid); i++ {
		if jid[i] == '@' {
			return true
		}
	}
	return false
}

// --- public methods ---

// CommunityMetadata fetches the metadata of a community (a parent group). The
// reply is parsed with the shared group metadata parser; the returned GroupInfo
// describes the community group itself.
func (c *Client) CommunityMetadata(ctx context.Context, communityJID string) (*GroupInfo, error) {
	sess, ok := c.activeSession()
	if !ok {
		return nil, errors.New("client: not logged in (no active session)")
	}
	if !isGroupJID(communityJID) {
		return nil, fmt.Errorf("client: %q is not a community/group JID", communityJID)
	}
	req := buildGroupMetadataQuery(c.nextIQID("wa-go-community-"), communityJID)
	reply, err := c.sendIQ(ctx, sess, req)
	if err != nil {
		return nil, err
	}
	return parseGroupMetadata(reply)
}

// CommunitySubGroups lists the groups linked into a community.
func (c *Client) CommunitySubGroups(ctx context.Context, communityJID string) ([]GroupLinkInfo, error) {
	sess, ok := c.activeSession()
	if !ok {
		return nil, errors.New("client: not logged in (no active session)")
	}
	if !isGroupJID(communityJID) {
		return nil, fmt.Errorf("client: %q is not a community/group JID", communityJID)
	}
	req := buildSubGroupsQuery(c.nextIQID("wa-go-community-"), communityJID)
	reply, err := c.sendIQ(ctx, sess, req)
	if err != nil {
		return nil, err
	}
	return parseSubGroups(reply), nil
}

// LinkGroup links the given group into the community as a sub-group.
func (c *Client) LinkGroup(ctx context.Context, communityJID, groupJID string) error {
	sess, ok := c.activeSession()
	if !ok {
		return errors.New("client: not logged in (no active session)")
	}
	if !isGroupJID(communityJID) || !isGroupJID(groupJID) {
		return errors.New("client: LinkGroup requires community and group JIDs")
	}
	req := buildLinkGroupQuery(c.nextIQID("wa-go-community-"), communityJID, groupJID)
	reply, err := c.sendIQ(ctx, sess, req)
	if err != nil {
		return err
	}
	return parseLinkResult(reply, "links")
}

// UnlinkGroup removes the given group from the community.
func (c *Client) UnlinkGroup(ctx context.Context, communityJID, groupJID string) error {
	sess, ok := c.activeSession()
	if !ok {
		return errors.New("client: not logged in (no active session)")
	}
	if !isGroupJID(communityJID) || !isGroupJID(groupJID) {
		return errors.New("client: UnlinkGroup requires community and group JIDs")
	}
	req := buildUnlinkGroupQuery(c.nextIQID("wa-go-community-"), communityJID, groupJID)
	reply, err := c.sendIQ(ctx, sess, req)
	if err != nil {
		return err
	}
	return parseLinkResult(reply, "unlink")
}
