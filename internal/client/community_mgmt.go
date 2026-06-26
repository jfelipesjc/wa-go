// Package client: community_mgmt.go completes the WhatsApp Community surface
// that community.go starts (CommunityMetadata / CommunitySubGroups / LinkGroup /
// UnlinkGroup). Everything here is a faithful port of Baileys'
// lib/Socket/communities.js (makeCommunitiesSocket); the wire shapes below quote
// the exact w:g2 envelopes that file emits.
//
// Communities ride the same "w:g2" group transport as group.go. A community is a
// group whose <create> carries a <parent> marker (extractCommunityMetadata keys
// isCommunity off the presence of a <parent> child). Sub-groups are normal groups
// linked in via <links>/<link link_type=sub_group> and carry a <linked_parent>.
//
// Confirmed nodes (Baileys communities.js):
//
//	communityCreate:
//	  <iq w:g2 set to=@g.us><create subject=<subject>>
//	    <description id=<descID>><body>...bytes...</body></description>
//	    <parent default_membership_approval_mode=request_required/>
//	    <allow_non_admin_sub_group_creation/>
//	    <create_general_chat/>
//	  </create></iq>
//
//	communityParticipantsUpdate (action add/remove/promote/demote; remove adds
//	linked_groups=true):
//	  <iq w:g2 set to=community><<action> [linked_groups=true]>
//	    <participant jid=.../>...</<action>></iq>
//
//	communityRequestParticipantsList:
//	  <iq w:g2 get to=community><membership_approval_requests/></iq>
//	  reply: <membership_approval_requests>
//	           <membership_approval_request jid=.../>...</...>
//
//	communityRequestParticipantsUpdate (action approve/reject):
//	  <iq w:g2 set to=community><membership_requests_action>
//	    <<action>><participant jid=.../>...</<action>>
//	  </membership_requests_action></iq>
//
//	communityLeave:
//	  <iq w:g2 set to=@g.us><leave><community id=<id>/></leave></iq>
//
//	communityUpdateSubject:
//	  <iq w:g2 set to=community><subject>...bytes...</subject></iq>
//
//	communityUpdateDescription (non-empty: id=<msgID>; empty: delete=true; both
//	carry prev=<descId> when a previous description exists):
//	  <iq w:g2 set to=community>
//	    <description id=<msgID> [prev=<prev>]><body>...bytes...</body></description>
//	  </iq>
//
//	communityToggleEphemeral:
//	  <iq w:g2 set to=community><ephemeral expiration=<sec>/></iq>   (enable)
//	  <iq w:g2 set to=community><not_ephemeral/></iq>               (disable)
//
//	communitySettingUpdate (setting is the tag itself, e.g. modify_only_admins):
//	  <iq w:g2 set to=community><<setting>/></iq>
//
//	communityMemberAddMode (mode carried as the byte content of the node):
//	  <iq w:g2 set to=community><member_add_mode>...mode bytes...</member_add_mode></iq>
//
//	communityJoinApprovalMode:
//	  <iq w:g2 set to=community><membership_approval_mode>
//	    <community_join state=<on|off>/></membership_approval_mode></iq>
//
//	communityInviteCode / communityRevokeInvite / communityGetInviteInfo /
//	communityAcceptInvite mirror the group invite envelopes (community.go's
//	parent surface reuses group.go for these on the community JID).
package client

import (
	"context"
	"errors"
	"fmt"

	"github.com/jfelipesjc/wa-go/internal/wire"
)

// CommunityMembershipRequest is one pending join request for a community, as
// returned by CommunityRequestParticipantsList. Attrs mirrors the raw
// <membership_approval_request .../> attributes Baileys returns verbatim.
type CommunityMembershipRequest struct {
	JID   string
	Attrs map[string]string
}

// --- node builders (pure; testable without a session) ---

// buildCommunityCreate builds the community-create iq. A community is a group
// carrying a <parent> marker.
//
//	<iq w:g2 set to=@g.us><create subject=<subject>>
//	  <description id=<descID>><body>...</body></description>
//	  <parent default_membership_approval_mode=request_required/>
//	  <allow_non_admin_sub_group_creation/>
//	  <create_general_chat/>
//	</create></iq>
func buildCommunityCreate(id, descID, subject, description string) wire.Node {
	return buildGroupQuery(id, groupEndpoint, "set", []wire.Node{
		{
			Tag:   "create",
			Attrs: map[string]string{"subject": subject},
			Content: []wire.Node{
				{
					Tag:     "description",
					Attrs:   map[string]string{"id": descID},
					Content: []wire.Node{{Tag: "body", Content: []byte(description)}},
				},
				{
					Tag:   "parent",
					Attrs: map[string]string{"default_membership_approval_mode": "request_required"},
				},
				{Tag: "allow_non_admin_sub_group_creation"},
				{Tag: "create_general_chat"},
			},
		},
	})
}

// buildCommunityParticipantsUpdate builds the add/remove/promote/demote iq on the
// community JID. A "remove" carries linked_groups=true (Baileys), which cascades
// the removal across the community's linked groups.
//
//	<iq w:g2 set to=community><<action> [linked_groups=true]>
//	  <participant jid=.../>...</<action>></iq>
func buildCommunityParticipantsUpdate(id, communityJID, action string, participants []string) wire.Node {
	parts := make([]wire.Node, len(participants))
	for i, jid := range participants {
		parts[i] = wire.Node{Tag: "participant", Attrs: map[string]string{"jid": jid}}
	}
	var attrs map[string]string
	if action == "remove" {
		attrs = map[string]string{"linked_groups": "true"}
	}
	return buildGroupQuery(id, communityJID, "set", []wire.Node{
		{Tag: action, Attrs: attrs, Content: parts},
	})
}

// buildCommunityRequestList builds the pending-membership listing iq:
//
//	<iq w:g2 get to=community><membership_approval_requests/></iq>
func buildCommunityRequestList(id, communityJID string) wire.Node {
	return buildGroupQuery(id, communityJID, "get", []wire.Node{
		{Tag: "membership_approval_requests"},
	})
}

// buildCommunityRequestUpdate builds the membership approve/reject iq:
//
//	<iq w:g2 set to=community><membership_requests_action>
//	  <<action>><participant jid=.../>...</<action>>
//	</membership_requests_action></iq>
func buildCommunityRequestUpdate(id, communityJID, action string, participants []string) wire.Node {
	parts := make([]wire.Node, len(participants))
	for i, jid := range participants {
		parts[i] = wire.Node{Tag: "participant", Attrs: map[string]string{"jid": jid}}
	}
	return buildGroupQuery(id, communityJID, "set", []wire.Node{
		{
			Tag:     "membership_requests_action",
			Content: []wire.Node{{Tag: action, Content: parts}},
		},
	})
}

// buildCommunityLeave builds the leave iq. Unlike group leave (<group id=...>),
// communities leave via <community id=...>.
//
//	<iq w:g2 set to=@g.us><leave><community id=<id>/></leave></iq>
func buildCommunityLeave(id, communityJID string) wire.Node {
	return buildGroupQuery(id, groupEndpoint, "set", []wire.Node{
		{
			Tag:     "leave",
			Content: []wire.Node{{Tag: "community", Attrs: map[string]string{"id": communityJID}}},
		},
	})
}

// buildCommunityUpdateSubject builds the subject-change iq (subject as byte
// content of <subject>):
//
//	<iq w:g2 set to=community><subject>...bytes...</subject></iq>
func buildCommunityUpdateSubject(id, communityJID, subject string) wire.Node {
	return buildGroupQuery(id, communityJID, "set", []wire.Node{
		{Tag: "subject", Content: []byte(subject)},
	})
}

// buildCommunityUpdateDescription builds the description-change iq. A non-empty
// description carries id=<msgID> and a <body>; an empty description deletes via
// delete=true. When a previous description id is known it is echoed as prev.
//
//	<iq w:g2 set to=community>
//	  <description id=<msgID> [prev=<prev>]><body>...bytes...</body></description>
//	</iq>
func buildCommunityUpdateDescription(id, msgID, prev, communityJID, desc string) wire.Node {
	attrs := map[string]string{}
	var content interface{}
	if desc == "" {
		attrs["delete"] = "true"
	} else {
		attrs["id"] = msgID
		content = []wire.Node{{Tag: "body", Content: []byte(desc)}}
	}
	if prev != "" {
		attrs["prev"] = prev
	}
	return buildGroupQuery(id, communityJID, "set", []wire.Node{
		{Tag: "description", Attrs: attrs, Content: content},
	})
}

// buildCommunityToggleEphemeral builds the disappearing-messages toggle iq. A
// non-zero expiration enables <ephemeral expiration=...>; zero disables via
// <not_ephemeral/>.
//
//	<iq w:g2 set to=community><ephemeral expiration=<sec>/></iq>
//	<iq w:g2 set to=community><not_ephemeral/></iq>
func buildCommunityToggleEphemeral(id, communityJID string, expiration int) wire.Node {
	var node wire.Node
	if expiration > 0 {
		node = wire.Node{Tag: "ephemeral", Attrs: map[string]string{"expiration": itoa(expiration)}}
	} else {
		node = wire.Node{Tag: "not_ephemeral"}
	}
	return buildGroupQuery(id, communityJID, "set", []wire.Node{node})
}

// buildCommunitySettingUpdate builds a setting toggle iq where the setting is the
// tag itself (e.g. modify_only_admins / allow_non_admin_sub_group_creation):
//
//	<iq w:g2 set to=community><<setting>/></iq>
func buildCommunitySettingUpdate(id, communityJID, setting string) wire.Node {
	return buildGroupQuery(id, communityJID, "set", []wire.Node{{Tag: setting}})
}

// buildCommunityMemberAddMode builds the member-add-mode iq. The mode (e.g.
// "all_member_add" / "admin_add") is the byte content of <member_add_mode>:
//
//	<iq w:g2 set to=community><member_add_mode>...mode...</member_add_mode></iq>
func buildCommunityMemberAddMode(id, communityJID, mode string) wire.Node {
	return buildGroupQuery(id, communityJID, "set", []wire.Node{
		{Tag: "member_add_mode", Content: []byte(mode)},
	})
}

// buildCommunityJoinApprovalMode builds the join-approval-mode iq:
//
//	<iq w:g2 set to=community><membership_approval_mode>
//	  <community_join state=<on|off>/></membership_approval_mode></iq>
func buildCommunityJoinApprovalMode(id, communityJID, state string) wire.Node {
	return buildGroupQuery(id, communityJID, "set", []wire.Node{
		{
			Tag: "membership_approval_mode",
			Content: []wire.Node{
				{Tag: "community_join", Attrs: map[string]string{"state": state}},
			},
		},
	})
}

// itoa converts a non-negative int to its decimal string without importing
// strconv just for the toggle path.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// --- response parsing ---

// parseCommunityRequestList extracts the pending join requests from a
// <membership_approval_requests> reply.
func parseCommunityRequestList(reply wire.Node) []CommunityMembershipRequest {
	container, ok := childByTag(reply, "membership_approval_requests")
	if !ok {
		return nil
	}
	reqs := childrenByTag(container, "membership_approval_request")
	out := make([]CommunityMembershipRequest, 0, len(reqs))
	for _, r := range reqs {
		out = append(out, CommunityMembershipRequest{JID: r.Attrs["jid"], Attrs: r.Attrs})
	}
	return out
}

// parseCommunityRequestUpdate extracts the per-participant outcome from a
// <membership_requests_action><action><participant .../> reply.
func parseCommunityRequestUpdate(reply wire.Node, action string) []GroupParticipantResult {
	container, ok := childByTag(reply, "membership_requests_action")
	if !ok {
		return nil
	}
	actionNode, ok := childByTag(container, action)
	if !ok {
		return nil
	}
	var out []GroupParticipantResult
	for _, p := range childrenByTag(actionNode, "participant") {
		status := p.Attrs["error"]
		if status == "" {
			status = "200"
		}
		out = append(out, GroupParticipantResult{JID: p.Attrs["jid"], Status: status})
	}
	return out
}

// --- public methods ---

// CommunityCreate creates a new community (a parent group) with the given subject
// and description, returning the community's group metadata.
func (c *Client) CommunityCreate(ctx context.Context, subject, description string) (*GroupInfo, error) {
	sess, ok := c.activeSession()
	if !ok {
		return nil, errors.New("client: not logged in (no active session)")
	}
	if subject == "" {
		return nil, errors.New("client: community subject required")
	}
	descID := generateMessageID()
	if len(descID) > 12 {
		descID = descID[:12]
	}
	req := buildCommunityCreate(c.nextIQID("wa-go-community-"), descID, subject, description)
	reply, err := c.sendIQ(ctx, sess, req)
	if err != nil {
		return nil, err
	}
	return parseGroupMetadata(reply)
}

// CommunityParticipantsUpdate adds, removes, promotes or demotes participants on a
// community. A "remove" cascades across the community's linked groups. action must
// be one of "add", "remove", "promote", "demote".
func (c *Client) CommunityParticipantsUpdate(ctx context.Context, communityJID string, participants []string, action string) ([]GroupParticipantResult, error) {
	sess, ok := c.activeSession()
	if !ok {
		return nil, errors.New("client: not logged in (no active session)")
	}
	if !isGroupJID(communityJID) {
		return nil, fmt.Errorf("client: %q is not a community JID", communityJID)
	}
	switch action {
	case "add", "remove", "promote", "demote":
	default:
		return nil, fmt.Errorf("client: invalid participants action %q", action)
	}
	if len(participants) == 0 {
		return nil, errors.New("client: participants update requires at least one jid")
	}
	req := buildCommunityParticipantsUpdate(c.nextIQID("wa-go-community-"), communityJID, action, participants)
	reply, err := c.sendIQ(ctx, sess, req)
	if err != nil {
		return nil, err
	}
	return parseParticipantsUpdate(reply, action), nil
}

// CommunityRequestParticipantsList lists pending membership (join) requests for a
// community.
func (c *Client) CommunityRequestParticipantsList(ctx context.Context, communityJID string) ([]CommunityMembershipRequest, error) {
	sess, ok := c.activeSession()
	if !ok {
		return nil, errors.New("client: not logged in (no active session)")
	}
	if !isGroupJID(communityJID) {
		return nil, fmt.Errorf("client: %q is not a community JID", communityJID)
	}
	req := buildCommunityRequestList(c.nextIQID("wa-go-community-"), communityJID)
	reply, err := c.sendIQ(ctx, sess, req)
	if err != nil {
		return nil, err
	}
	return parseCommunityRequestList(reply), nil
}

// CommunityRequestParticipantsUpdate approves or rejects pending membership
// requests. action must be "approve" or "reject".
func (c *Client) CommunityRequestParticipantsUpdate(ctx context.Context, communityJID string, participants []string, action string) ([]GroupParticipantResult, error) {
	sess, ok := c.activeSession()
	if !ok {
		return nil, errors.New("client: not logged in (no active session)")
	}
	if !isGroupJID(communityJID) {
		return nil, fmt.Errorf("client: %q is not a community JID", communityJID)
	}
	switch action {
	case "approve", "reject":
	default:
		return nil, fmt.Errorf("client: invalid membership action %q", action)
	}
	if len(participants) == 0 {
		return nil, errors.New("client: membership update requires at least one jid")
	}
	req := buildCommunityRequestUpdate(c.nextIQID("wa-go-community-"), communityJID, action, participants)
	reply, err := c.sendIQ(ctx, sess, req)
	if err != nil {
		return nil, err
	}
	return parseCommunityRequestUpdate(reply, action), nil
}

// CommunityFetchLinkedGroups returns the groups linked into a community. It is a
// documented wrapper over CommunitySubGroups (community.go), which already issues
// the <sub_groups/> query; Baileys' communityFetchLinkedGroups additionally
// resolves a sub-group's parent first, but here the caller passes the community
// JID directly.
func (c *Client) CommunityFetchLinkedGroups(ctx context.Context, communityJID string) ([]GroupLinkInfo, error) {
	return c.CommunitySubGroups(ctx, communityJID)
}

// CommunityToggleEphemeral enables (expiration > 0, seconds) or disables
// (expiration == 0) disappearing messages for a community.
func (c *Client) CommunityToggleEphemeral(ctx context.Context, communityJID string, expiration int) error {
	sess, ok := c.activeSession()
	if !ok {
		return errors.New("client: not logged in (no active session)")
	}
	if !isGroupJID(communityJID) {
		return fmt.Errorf("client: %q is not a community JID", communityJID)
	}
	req := buildCommunityToggleEphemeral(c.nextIQID("wa-go-community-"), communityJID, expiration)
	_, err := c.sendIQ(ctx, sess, req)
	return err
}

// CommunityUpdateSubject changes a community's subject (name).
func (c *Client) CommunityUpdateSubject(ctx context.Context, communityJID, subject string) error {
	sess, ok := c.activeSession()
	if !ok {
		return errors.New("client: not logged in (no active session)")
	}
	if !isGroupJID(communityJID) {
		return fmt.Errorf("client: %q is not a community JID", communityJID)
	}
	req := buildCommunityUpdateSubject(c.nextIQID("wa-go-community-"), communityJID, subject)
	_, err := c.sendIQ(ctx, sess, req)
	return err
}

// CommunityUpdateDescription changes a community's description. An empty desc
// deletes the existing description. prev, when non-empty, is the previous
// description id (Baileys reads it from community metadata before updating).
func (c *Client) CommunityUpdateDescription(ctx context.Context, communityJID, desc, prev string) error {
	sess, ok := c.activeSession()
	if !ok {
		return errors.New("client: not logged in (no active session)")
	}
	if !isGroupJID(communityJID) {
		return fmt.Errorf("client: %q is not a community JID", communityJID)
	}
	req := buildCommunityUpdateDescription(c.nextIQID("wa-go-community-"), generateMessageID(), prev, communityJID, desc)
	_, err := c.sendIQ(ctx, sess, req)
	return err
}

// CommunityLeave leaves a community.
func (c *Client) CommunityLeave(ctx context.Context, communityJID string) error {
	sess, ok := c.activeSession()
	if !ok {
		return errors.New("client: not logged in (no active session)")
	}
	if !isGroupJID(communityJID) {
		return fmt.Errorf("client: %q is not a community JID", communityJID)
	}
	req := buildCommunityLeave(c.nextIQID("wa-go-community-"), communityJID)
	_, err := c.sendIQ(ctx, sess, req)
	return err
}

// CommunitySettingUpdate toggles a community setting where the setting name is the
// node tag itself (e.g. "modify_only_admins" to restrict who can edit info, or
// "allow_non_admin_sub_group_creation").
func (c *Client) CommunitySettingUpdate(ctx context.Context, communityJID, setting string) error {
	sess, ok := c.activeSession()
	if !ok {
		return errors.New("client: not logged in (no active session)")
	}
	if !isGroupJID(communityJID) {
		return fmt.Errorf("client: %q is not a community JID", communityJID)
	}
	if setting == "" {
		return errors.New("client: community setting required")
	}
	req := buildCommunitySettingUpdate(c.nextIQID("wa-go-community-"), communityJID, setting)
	_, err := c.sendIQ(ctx, sess, req)
	return err
}

// CommunityMemberAddMode sets who may add members to the community. mode is
// typically "all_member_add" or "admin_add".
func (c *Client) CommunityMemberAddMode(ctx context.Context, communityJID, mode string) error {
	sess, ok := c.activeSession()
	if !ok {
		return errors.New("client: not logged in (no active session)")
	}
	if !isGroupJID(communityJID) {
		return fmt.Errorf("client: %q is not a community JID", communityJID)
	}
	if mode == "" {
		return errors.New("client: member add mode required")
	}
	req := buildCommunityMemberAddMode(c.nextIQID("wa-go-community-"), communityJID, mode)
	_, err := c.sendIQ(ctx, sess, req)
	return err
}

// CommunityJoinApprovalMode toggles whether joining the community requires admin
// approval. state is "on" (approval required) or "off".
func (c *Client) CommunityJoinApprovalMode(ctx context.Context, communityJID, state string) error {
	sess, ok := c.activeSession()
	if !ok {
		return errors.New("client: not logged in (no active session)")
	}
	if !isGroupJID(communityJID) {
		return fmt.Errorf("client: %q is not a community JID", communityJID)
	}
	switch state {
	case "on", "off":
	default:
		return fmt.Errorf("client: invalid join approval state %q", state)
	}
	req := buildCommunityJoinApprovalMode(c.nextIQID("wa-go-community-"), communityJID, state)
	_, err := c.sendIQ(ctx, sess, req)
	return err
}
