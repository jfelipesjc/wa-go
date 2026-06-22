// Package client: group_requests.go implements group join-request (membership
// approval) management — listing pending join requests, approving/rejecting
// them, and toggling the two related group settings (join-approval mode and
// member-add mode). These mirror Baileys' Socket/groups.js:
//
//   - groupRequestParticipantsList  -> GroupRequestParticipantsList
//   - groupRequestParticipantsUpdate -> GroupRequestParticipantsUpdate
//   - groupJoinApprovalMode          -> GroupJoinApprovalMode
//   - groupMemberAddMode             -> GroupMemberAddMode
//
// All four ride the same w:g2 iq envelope as the rest of group.go (buildGroupQuery).
//
// Stanza shapes confirmed against groups.js:
//
// List (type=get):
//
//	<iq xmlns=w:g2 type=get to=group>
//	  <membership_approval_requests/>
//	</iq>
//	# reply:
//	<membership_approval_requests>
//	  <membership_approval_request jid=.. request_method=.. request_time=../>
//	  ...
//	</membership_approval_requests>
//
// Approve/reject (type=set):
//
//	<iq xmlns=w:g2 type=set to=group>
//	  <membership_requests_action>
//	    <approve|reject>
//	      <participant jid=../>...
//	    </approve|reject>
//	  </membership_requests_action>
//	</iq>
//	# reply mirrors the request: <membership_requests_action><approve|reject>
//	#   <participant jid=.. [error=..]/> ...
//
// Join-approval mode (type=set):
//
//	<iq xmlns=w:g2 type=set to=group>
//	  <membership_approval_mode><group_join state=on|off/></membership_approval_mode>
//	</iq>
//
// Member-add mode (type=set): the mode is the byte content of <member_add_mode>:
//
//	<iq xmlns=w:g2 type=set to=group>
//	  <member_add_mode>all_member_add|admin_add</member_add_mode>
//	</iq>
package client

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/felipeleal/wa-go/internal/wire"
)

// GroupParticipantRequest is one pending join request, parsed from a
// <membership_approval_request> node. Mirrors the attrs Baileys returns from
// groupRequestParticipantsList.
type GroupParticipantRequest struct {
	// JID is the requester's JID.
	JID string
	// RequestMethod is how they requested to join (e.g. "InviteLink"), the
	// request_method attr; empty if the server omits it.
	RequestMethod string
	// RequestTime is the request_time attr (unix seconds), 0 if absent or
	// unparseable.
	RequestTime int64
}

// --- node builders (pure) ---

// buildGroupRequestList builds the pending-requests fetch:
//
//	<iq xmlns=w:g2 type=get to=group><membership_approval_requests/></iq>
func buildGroupRequestList(id, groupJID string) wire.Node {
	return buildGroupQuery(id, groupJID, "get", []wire.Node{
		{Tag: "membership_approval_requests"},
	})
}

// buildGroupRequestUpdate builds the approve/reject action iq. action is
// "approve" or "reject"; each jid becomes a <participant jid=..> under it:
//
//	<iq xmlns=w:g2 type=set to=group>
//	  <membership_requests_action>
//	    <<action>><participant jid=../>...</<action>>
//	  </membership_requests_action>
//	</iq>
func buildGroupRequestUpdate(id, groupJID, action string, jids []string) wire.Node {
	parts := make([]wire.Node, len(jids))
	for i, jid := range jids {
		parts[i] = wire.Node{Tag: "participant", Attrs: map[string]string{"jid": jid}}
	}
	return buildGroupQuery(id, groupJID, "set", []wire.Node{
		{
			Tag:     "membership_requests_action",
			Content: []wire.Node{{Tag: action, Content: parts}},
		},
	})
}

// buildGroupJoinApprovalMode builds the join-approval-mode toggle. on=true sets
// state=on (new joiners need admin approval), false sets state=off:
//
//	<iq xmlns=w:g2 type=set to=group>
//	  <membership_approval_mode><group_join state=on|off/></membership_approval_mode>
//	</iq>
func buildGroupJoinApprovalMode(id, groupJID string, on bool) wire.Node {
	state := "off"
	if on {
		state = "on"
	}
	return buildGroupQuery(id, groupJID, "set", []wire.Node{
		{
			Tag:     "membership_approval_mode",
			Content: []wire.Node{{Tag: "group_join", Attrs: map[string]string{"state": state}}},
		},
	})
}

// buildGroupMemberAddMode builds the member-add-mode toggle. The mode string is
// the byte content of <member_add_mode> (Baileys passes it as content):
//
//	<iq xmlns=w:g2 type=set to=group><member_add_mode>mode</member_add_mode></iq>
func buildGroupMemberAddMode(id, groupJID, mode string) wire.Node {
	return buildGroupQuery(id, groupJID, "set", []wire.Node{
		{Tag: "member_add_mode", Content: []byte(mode)},
	})
}

// --- parsers ---

// parseGroupRequestList extracts the pending join requests from a w:g2 reply.
// Mirrors Baileys: it reads the <membership_approval_requests> child and maps
// each <membership_approval_request> to its attrs. A missing container yields no
// requests (an empty list is a valid "nobody is waiting" answer, not an error).
func parseGroupRequestList(reply wire.Node) []GroupParticipantRequest {
	container, ok := childByTag(reply, "membership_approval_requests")
	if !ok {
		return nil
	}
	var out []GroupParticipantRequest
	for _, r := range childrenByTag(container, "membership_approval_request") {
		req := GroupParticipantRequest{
			JID:           r.Attrs["jid"],
			RequestMethod: r.Attrs["request_method"],
		}
		if t := r.Attrs["request_time"]; t != "" {
			if v, err := strconv.ParseInt(t, 10, 64); err == nil {
				req.RequestTime = v
			}
		}
		out = append(out, req)
	}
	return out
}

// parseGroupRequestUpdate extracts the per-participant outcome from an
// approve/reject reply. Mirrors Baileys groupRequestParticipantsUpdate: it dives
// <membership_requests_action> -> <action> and reads each <participant>, whose
// status is its error attr or "200" on success.
func parseGroupRequestUpdate(reply wire.Node, action string) []GroupParticipantResult {
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

// GroupRequestParticipantsList fetches the list of users waiting for admin
// approval to join the group (only meaningful when join-approval mode is on).
func (c *Client) GroupRequestParticipantsList(ctx context.Context, groupJID string) ([]GroupParticipantRequest, error) {
	sess, ok := c.activeSession()
	if !ok {
		return nil, errors.New("client: not logged in (no active session)")
	}
	if !isGroupJID(groupJID) {
		return nil, fmt.Errorf("client: %q is not a group JID", groupJID)
	}
	req := buildGroupRequestList(c.nextIQID("wa-go-group-"), groupJID)
	reply, err := c.sendIQ(ctx, sess, req)
	if err != nil {
		return nil, err
	}
	return parseGroupRequestList(reply), nil
}

// GroupRequestParticipantsUpdate approves or rejects pending join requests for
// the given jids and returns the per-jid result. action must be "approve" or
// "reject".
func (c *Client) GroupRequestParticipantsUpdate(ctx context.Context, groupJID string, jids []string, action string) ([]GroupParticipantResult, error) {
	sess, ok := c.activeSession()
	if !ok {
		return nil, errors.New("client: not logged in (no active session)")
	}
	if !isGroupJID(groupJID) {
		return nil, fmt.Errorf("client: %q is not a group JID", groupJID)
	}
	switch action {
	case "approve", "reject":
	default:
		return nil, fmt.Errorf("client: invalid request action %q (want approve|reject)", action)
	}
	if len(jids) == 0 {
		return nil, errors.New("client: request update requires at least one jid")
	}
	req := buildGroupRequestUpdate(c.nextIQID("wa-go-group-"), groupJID, action, jids)
	reply, err := c.sendIQ(ctx, sess, req)
	if err != nil {
		return nil, err
	}
	return parseGroupRequestUpdate(reply, action), nil
}

// GroupJoinApprovalMode turns the group's join-approval requirement on or off.
// When on, users joining via the invite link land in the pending-requests queue
// (GroupRequestParticipantsList) instead of joining directly.
func (c *Client) GroupJoinApprovalMode(ctx context.Context, groupJID string, on bool) error {
	sess, ok := c.activeSession()
	if !ok {
		return errors.New("client: not logged in (no active session)")
	}
	if !isGroupJID(groupJID) {
		return fmt.Errorf("client: %q is not a group JID", groupJID)
	}
	req := buildGroupJoinApprovalMode(c.nextIQID("wa-go-group-"), groupJID, on)
	_, err := c.sendIQ(ctx, sess, req)
	return err
}

// GroupMemberAddMode controls who may add new members to the group. mode must be
// "all_member_add" (any member can add) or "admin_add" (only admins).
func (c *Client) GroupMemberAddMode(ctx context.Context, groupJID, mode string) error {
	sess, ok := c.activeSession()
	if !ok {
		return errors.New("client: not logged in (no active session)")
	}
	if !isGroupJID(groupJID) {
		return fmt.Errorf("client: %q is not a group JID", groupJID)
	}
	switch mode {
	case "all_member_add", "admin_add":
	default:
		return fmt.Errorf("client: invalid member-add mode %q (want all_member_add|admin_add)", mode)
	}
	req := buildGroupMemberAddMode(c.nextIQID("wa-go-group-"), groupJID, mode)
	_, err := c.sendIQ(ctx, sess, req)
	return err
}
