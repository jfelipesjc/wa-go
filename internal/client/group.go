package client

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/felipeleal/wa-go/internal/wire"
)

// groupEndpoint is the destination for group-wide queries that are not scoped to
// a specific group jid (groupCreate, groupLeave, groupAcceptInvite).
const groupEndpoint = "@g.us"

// xmlnsGroup is the namespace for all group management iqs (Baileys' 'w:g2').
const xmlnsGroup = "w:g2"

// GroupParticipant is one member of a group as returned by GroupMetadata.
type GroupParticipant struct {
	JID          string
	IsAdmin      bool
	IsSuperAdmin bool
}

// GroupInfo is the parsed metadata of a group (the <group> node of a w:g2
// query reply). It mirrors the subset of Baileys' extractGroupMetadata that this
// build consumes.
type GroupInfo struct {
	JID          string
	Subject      string
	Owner        string
	Desc         string
	Creation     int64
	Participants []GroupParticipant
}

// GroupParticipantResult reports the per-participant outcome of a
// GroupParticipantsUpdate (add/remove/promote/demote): the affected jid and the
// status code the server returned ("200" on success, otherwise the error code).
type GroupParticipantResult struct {
	JID    string
	Status string
}

// --- node builders (pure; the iq assembly is testable without a session) ---

// buildGroupQuery wraps content children in the standard w:g2 iq envelope, the
// analogue of Baileys' groupQuery helper:
//
//	<iq xmlns=w:g2 type=<type> to=<jid> id=<id>>...content...</iq>
func buildGroupQuery(id, to, typ string, content []wire.Node) wire.Node {
	return wire.Node{
		Tag: "iq",
		Attrs: map[string]string{
			"id":    id,
			"xmlns": xmlnsGroup,
			"type":  typ,
			"to":    to,
		},
		Content: content,
	}
}

// buildGroupMetadataQuery builds the metadata fetch:
//
//	<iq xmlns=w:g2 type=get to=group><query request=interactive/></iq>
func buildGroupMetadataQuery(id, groupJID string) wire.Node {
	return buildGroupQuery(id, groupJID, "get", []wire.Node{
		{Tag: "query", Attrs: map[string]string{"request": "interactive"}},
	})
}

// buildGroupCreate builds the create iq:
//
//	<iq xmlns=w:g2 type=set to=@g.us>
//	  <create subject=<subject> key=<key}>
//	    <participant jid=.../>...
//	  </create>
//	</iq>
func buildGroupCreate(id, key, subject string, participants []string) wire.Node {
	parts := make([]wire.Node, len(participants))
	for i, jid := range participants {
		parts[i] = wire.Node{Tag: "participant", Attrs: map[string]string{"jid": jid}}
	}
	return buildGroupQuery(id, groupEndpoint, "set", []wire.Node{
		{
			Tag:     "create",
			Attrs:   map[string]string{"subject": subject, "key": key},
			Content: parts,
		},
	})
}

// buildGroupParticipantsUpdate builds the add/remove/promote/demote iq:
//
//	<iq xmlns=w:g2 type=set to=group>
//	  <<action>><participant jid=.../>...</<action>>
//	</iq>
func buildGroupParticipantsUpdate(id, groupJID, action string, participants []string) wire.Node {
	parts := make([]wire.Node, len(participants))
	for i, jid := range participants {
		parts[i] = wire.Node{Tag: "participant", Attrs: map[string]string{"jid": jid}}
	}
	return buildGroupQuery(id, groupJID, "set", []wire.Node{
		{Tag: action, Content: parts},
	})
}

// buildGroupUpdateSubject builds the subject change iq. The subject is carried as
// the byte content of the <subject> node (Baileys' Buffer.from(subject,'utf-8')):
//
//	<iq xmlns=w:g2 type=set to=group><subject>...bytes...</subject></iq>
func buildGroupUpdateSubject(id, groupJID, subject string) wire.Node {
	return buildGroupQuery(id, groupJID, "set", []wire.Node{
		{Tag: "subject", Content: []byte(subject)},
	})
}

// buildGroupUpdateDescription builds the description change iq. A non-empty
// description carries an id attr and a <body> with the text bytes; an empty
// description deletes the existing one (delete=true, no body), matching Baileys'
// groupUpdateDescription:
//
//	<iq xmlns=w:g2 type=set to=group>
//	  <description id=<msgID>><body>...bytes...</body></description>
//	</iq>
//	<iq xmlns=w:g2 type=set to=group>
//	  <description delete=true/>
//	</iq>
func buildGroupUpdateDescription(id, descID, groupJID, desc string) wire.Node {
	var descNode wire.Node
	if desc == "" {
		descNode = wire.Node{Tag: "description", Attrs: map[string]string{"delete": "true"}}
	} else {
		descNode = wire.Node{
			Tag:     "description",
			Attrs:   map[string]string{"id": descID},
			Content: []wire.Node{{Tag: "body", Content: []byte(desc)}},
		}
	}
	return buildGroupQuery(id, groupJID, "set", []wire.Node{descNode})
}

// buildGroupLeave builds the leave iq:
//
//	<iq xmlns=w:g2 type=set to=@g.us><leave><group id=group/></leave></iq>
func buildGroupLeave(id, groupJID string) wire.Node {
	return buildGroupQuery(id, groupEndpoint, "set", []wire.Node{
		{
			Tag:     "leave",
			Content: []wire.Node{{Tag: "group", Attrs: map[string]string{"id": groupJID}}},
		},
	})
}

// buildGroupInviteCode builds the invite-code fetch iq:
//
//	<iq xmlns=w:g2 type=get to=group><invite/></iq>
func buildGroupInviteCode(id, groupJID string) wire.Node {
	return buildGroupQuery(id, groupJID, "get", []wire.Node{{Tag: "invite"}})
}

// buildGroupRevokeInvite builds the invite-code revoke iq (same node, type=set):
//
//	<iq xmlns=w:g2 type=set to=group><invite/></iq>
func buildGroupRevokeInvite(id, groupJID string) wire.Node {
	return buildGroupQuery(id, groupJID, "set", []wire.Node{{Tag: "invite"}})
}

// buildGroupAcceptInvite builds the invite-accept iq:
//
//	<iq xmlns=w:g2 type=set to=@g.us><invite code=<code>/></iq>
func buildGroupAcceptInvite(id, code string) wire.Node {
	return buildGroupQuery(id, groupEndpoint, "set", []wire.Node{
		{Tag: "invite", Attrs: map[string]string{"code": code}},
	})
}

// buildGroupSettingUpdate builds a setting toggle iq (announce / not_announce /
// locked / unlocked), matching Baileys' groupSettingUpdate where the setting is
// the tag itself:
//
//	<iq xmlns=w:g2 type=set to=group><<setting>/></iq>
func buildGroupSettingUpdate(id, groupJID, setting string) wire.Node {
	return buildGroupQuery(id, groupJID, "set", []wire.Node{{Tag: setting}})
}

// --- parsers ---

// parseGroupMetadata extracts a GroupInfo from a w:g2 reply node (an <iq> or a
// synthetic <result> whose child is the <group> node), mirroring the fields of
// Baileys' extractGroupMetadata that we surface.
func parseGroupMetadata(reply wire.Node) (*GroupInfo, error) {
	group, ok := childByTag(reply, "group")
	if !ok {
		return nil, errors.New("client: group metadata reply missing <group> node")
	}
	id := group.Attrs["id"]
	if id == "" {
		return nil, errors.New("client: group metadata reply missing group id")
	}
	if !strings.Contains(id, "@") {
		id += "@g.us"
	}

	info := &GroupInfo{
		JID:     id,
		Subject: group.Attrs["subject"],
		Owner:   group.Attrs["creator"],
	}
	if s := group.Attrs["creation"]; s != "" {
		if v, err := strconv.ParseInt(s, 10, 64); err == nil {
			info.Creation = v
		}
	}
	if descChild, ok := childByTag(group, "description"); ok {
		if body, ok := childByTag(descChild, "body"); ok {
			info.Desc = string(nodeBytes(body))
		}
	}
	for _, p := range childrenByTag(group, "participant") {
		gp := GroupParticipant{JID: p.Attrs["jid"]}
		switch p.Attrs["type"] {
		case "admin":
			gp.IsAdmin = true
		case "superadmin":
			gp.IsAdmin = true
			gp.IsSuperAdmin = true
		}
		info.Participants = append(info.Participants, gp)
	}
	return info, nil
}

// parseInviteCode pulls the invite code attr from an <invite> child of the reply,
// matching Baileys' groupInviteCode / groupRevokeInvite return.
func parseInviteCode(reply wire.Node) (string, error) {
	inv, ok := childByTag(reply, "invite")
	if !ok {
		return "", errors.New("client: invite reply missing <invite> node")
	}
	code := inv.Attrs["code"]
	if code == "" {
		return "", errors.New("client: invite reply missing code attr")
	}
	return code, nil
}

// parseAcceptInvite pulls the joined group's jid from the <group> child of an
// accept-invite reply, matching Baileys' groupAcceptInvite return.
func parseAcceptInvite(reply wire.Node) (string, error) {
	group, ok := childByTag(reply, "group")
	if !ok {
		return "", errors.New("client: accept-invite reply missing <group> node")
	}
	jid := group.Attrs["jid"]
	if jid == "" {
		return "", errors.New("client: accept-invite reply missing group jid")
	}
	return jid, nil
}

// parseParticipantsUpdate extracts the per-participant results from an
// add/remove/promote/demote reply (the <action> child holds <participant> nodes
// carrying an optional error attr), matching Baileys' groupParticipantsUpdate.
func parseParticipantsUpdate(reply wire.Node, action string) []GroupParticipantResult {
	actionNode, ok := childByTag(reply, action)
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

// GroupMetadata fetches the metadata of a group.
func (c *Client) GroupMetadata(ctx context.Context, groupJID string) (*GroupInfo, error) {
	sess, ok := c.activeSession()
	if !ok {
		return nil, errors.New("client: not logged in (no active session)")
	}
	if !isGroupJID(groupJID) {
		return nil, fmt.Errorf("client: %q is not a group JID", groupJID)
	}
	req := buildGroupMetadataQuery(c.nextIQID("wa-go-group-"), groupJID)
	reply, err := c.sendIQ(ctx, sess, req)
	if err != nil {
		return nil, err
	}
	return parseGroupMetadata(reply)
}

// GroupCreate creates a new group with the given subject and initial participants
// and returns its metadata.
func (c *Client) GroupCreate(ctx context.Context, subject string, participants []string) (*GroupInfo, error) {
	sess, ok := c.activeSession()
	if !ok {
		return nil, errors.New("client: not logged in (no active session)")
	}
	if subject == "" {
		return nil, errors.New("client: group subject required")
	}
	req := buildGroupCreate(c.nextIQID("wa-go-group-"), generateMessageID(), subject, participants)
	reply, err := c.sendIQ(ctx, sess, req)
	if err != nil {
		return nil, err
	}
	return parseGroupMetadata(reply)
}

// GroupParticipantsUpdate adds, removes, promotes or demotes participants in a
// group and returns the per-participant result. action must be one of "add",
// "remove", "promote", "demote".
func (c *Client) GroupParticipantsUpdate(ctx context.Context, groupJID string, participants []string, action string) ([]GroupParticipantResult, error) {
	sess, ok := c.activeSession()
	if !ok {
		return nil, errors.New("client: not logged in (no active session)")
	}
	if !isGroupJID(groupJID) {
		return nil, fmt.Errorf("client: %q is not a group JID", groupJID)
	}
	switch action {
	case "add", "remove", "promote", "demote":
	default:
		return nil, fmt.Errorf("client: invalid participants action %q", action)
	}
	if len(participants) == 0 {
		return nil, errors.New("client: participants update requires at least one jid")
	}
	req := buildGroupParticipantsUpdate(c.nextIQID("wa-go-group-"), groupJID, action, participants)
	reply, err := c.sendIQ(ctx, sess, req)
	if err != nil {
		return nil, err
	}
	return parseParticipantsUpdate(reply, action), nil
}

// GroupUpdateSubject changes a group's subject (name).
func (c *Client) GroupUpdateSubject(ctx context.Context, groupJID, subject string) error {
	sess, ok := c.activeSession()
	if !ok {
		return errors.New("client: not logged in (no active session)")
	}
	if !isGroupJID(groupJID) {
		return fmt.Errorf("client: %q is not a group JID", groupJID)
	}
	req := buildGroupUpdateSubject(c.nextIQID("wa-go-group-"), groupJID, subject)
	_, err := c.sendIQ(ctx, sess, req)
	return err
}

// GroupUpdateDescription changes a group's description. An empty desc deletes the
// existing description.
func (c *Client) GroupUpdateDescription(ctx context.Context, groupJID, desc string) error {
	sess, ok := c.activeSession()
	if !ok {
		return errors.New("client: not logged in (no active session)")
	}
	if !isGroupJID(groupJID) {
		return fmt.Errorf("client: %q is not a group JID", groupJID)
	}
	req := buildGroupUpdateDescription(c.nextIQID("wa-go-group-"), generateMessageID(), groupJID, desc)
	_, err := c.sendIQ(ctx, sess, req)
	return err
}

// GroupLeave leaves a group.
func (c *Client) GroupLeave(ctx context.Context, groupJID string) error {
	sess, ok := c.activeSession()
	if !ok {
		return errors.New("client: not logged in (no active session)")
	}
	if !isGroupJID(groupJID) {
		return fmt.Errorf("client: %q is not a group JID", groupJID)
	}
	req := buildGroupLeave(c.nextIQID("wa-go-group-"), groupJID)
	_, err := c.sendIQ(ctx, sess, req)
	return err
}

// GroupInviteCode fetches the current invite code of a group (the short code in
// the chat.whatsapp.com/<code> link).
func (c *Client) GroupInviteCode(ctx context.Context, groupJID string) (string, error) {
	sess, ok := c.activeSession()
	if !ok {
		return "", errors.New("client: not logged in (no active session)")
	}
	if !isGroupJID(groupJID) {
		return "", fmt.Errorf("client: %q is not a group JID", groupJID)
	}
	req := buildGroupInviteCode(c.nextIQID("wa-go-group-"), groupJID)
	reply, err := c.sendIQ(ctx, sess, req)
	if err != nil {
		return "", err
	}
	return parseInviteCode(reply)
}

// GroupRevokeInvite revokes the current invite code and returns the new one.
func (c *Client) GroupRevokeInvite(ctx context.Context, groupJID string) (string, error) {
	sess, ok := c.activeSession()
	if !ok {
		return "", errors.New("client: not logged in (no active session)")
	}
	if !isGroupJID(groupJID) {
		return "", fmt.Errorf("client: %q is not a group JID", groupJID)
	}
	req := buildGroupRevokeInvite(c.nextIQID("wa-go-group-"), groupJID)
	reply, err := c.sendIQ(ctx, sess, req)
	if err != nil {
		return "", err
	}
	return parseInviteCode(reply)
}

// GroupAcceptInvite joins a group via its invite code and returns the joined
// group's jid.
func (c *Client) GroupAcceptInvite(ctx context.Context, code string) (string, error) {
	sess, ok := c.activeSession()
	if !ok {
		return "", errors.New("client: not logged in (no active session)")
	}
	if code == "" {
		return "", errors.New("client: invite code required")
	}
	req := buildGroupAcceptInvite(c.nextIQID("wa-go-group-"), code)
	reply, err := c.sendIQ(ctx, sess, req)
	if err != nil {
		return "", err
	}
	return parseAcceptInvite(reply)
}

// GroupSettingUpdate toggles a group setting. setting must be one of "announce"
// (only admins can send), "not_announce" (everyone can send), "locked" (only
// admins can edit group info) or "unlocked" (everyone can edit group info).
func (c *Client) GroupSettingUpdate(ctx context.Context, groupJID, setting string) error {
	sess, ok := c.activeSession()
	if !ok {
		return errors.New("client: not logged in (no active session)")
	}
	if !isGroupJID(groupJID) {
		return fmt.Errorf("client: %q is not a group JID", groupJID)
	}
	switch setting {
	case "announce", "not_announce", "locked", "unlocked":
	default:
		return fmt.Errorf("client: invalid group setting %q", setting)
	}
	req := buildGroupSettingUpdate(c.nextIQID("wa-go-group-"), groupJID, setting)
	_, err := c.sendIQ(ctx, sess, req)
	return err
}
