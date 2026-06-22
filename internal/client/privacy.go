// Package client: privacy.go implements the WhatsApp privacy and block-list
// IQs — fetching/updating privacy settings and blocking/unblocking/listing
// contacts. These mirror Baileys' Socket/chats.js helpers (fetchPrivacySettings,
// privacyQuery / updateXxxPrivacy, fetchBlocklist, updateBlockStatus).
//
// Confirmed Baileys structures (lib/Socket/chats.js):
//
//	fetchPrivacySettings:
//	  <iq xmlns=privacy to=@s.whatsapp.net type=get><privacy/></iq>
//	reply: <iq ...><privacy><category name=<k> value=<v>/>...</privacy></iq>
//	parsed via reduceBinaryNodeToDictionary(content[0], 'category').
//
//	privacyQuery(name, value)  (update one setting):
//	  <iq xmlns=privacy to=@s.whatsapp.net type=set>
//	    <privacy><category name=<name> value=<value>/></privacy>
//	  </iq>
//	The Baileys setting names map to: lastSeen->"last", online->"online",
//	profilePicture->"profile", status->"status", readReceipts->"readreceipts",
//	groupsAdd->"groupadd" (also messages->"messages", call->"calladd").
//
//	fetchBlocklist:
//	  <iq xmlns=blocklist to=@s.whatsapp.net type=get/>
//	reply: <iq ...><list><item jid=.../>...</list></iq>
//
//	updateBlockStatus(jid, action):
//	  <iq xmlns=blocklist to=@s.whatsapp.net type=set>
//	    <item action=block|unblock jid=<jid>/>
//	  </iq>
//	(Baileys additionally resolves LID/PN and adds a pn_jid attr on block; this
//	 build sends the bare jid the caller supplies, which the server accepts for
//	 phone-number jids.)
package client

import (
	"context"
	"errors"

	"github.com/felipeleal/wa-go/internal/wire"
)

// PrivacySetting names the privacy categories UpdatePrivacy understands. They
// are the friendly Baileys names; updatePrivacyCategory maps them to the wire
// category names.
type PrivacySetting string

const (
	PrivacyLastSeen       PrivacySetting = "lastSeen"
	PrivacyOnline         PrivacySetting = "online"
	PrivacyProfilePicture PrivacySetting = "profile"
	PrivacyStatus         PrivacySetting = "status"
	PrivacyReadReceipts   PrivacySetting = "readReceipts"
	PrivacyGroupsAdd      PrivacySetting = "groupsAdd"
)

// privacyCategoryName maps a friendly PrivacySetting to its wire category name.
// Returns ("", false) for an unknown setting.
func privacyCategoryName(s PrivacySetting) (string, bool) {
	switch s {
	case PrivacyLastSeen:
		return "last", true
	case PrivacyOnline:
		return "online", true
	case PrivacyProfilePicture:
		return "profile", true
	case PrivacyStatus:
		return "status", true
	case PrivacyReadReceipts:
		return "readreceipts", true
	case PrivacyGroupsAdd:
		return "groupadd", true
	default:
		return "", false
	}
}

// fetchPrivacySettingsNode builds the get-privacy iq:
//
//	<iq xmlns=privacy to=@s.whatsapp.net type=get id=...><privacy/></iq>
func fetchPrivacySettingsNode(id string) wire.Node {
	return wire.Node{
		Tag: "iq",
		Attrs: map[string]string{
			"xmlns": "privacy",
			"to":    sWhatsAppNet,
			"type":  "get",
			"id":    id,
		},
		Content: []wire.Node{{Tag: "privacy"}},
	}
}

// updatePrivacyNode builds the set-privacy iq for one category:
//
//	<iq xmlns=privacy to=@s.whatsapp.net type=set id=...>
//	  <privacy><category name=<category> value=<value>/></privacy>
//	</iq>
func updatePrivacyNode(id, category, value string) wire.Node {
	return wire.Node{
		Tag: "iq",
		Attrs: map[string]string{
			"xmlns": "privacy",
			"to":    sWhatsAppNet,
			"type":  "set",
			"id":    id,
		},
		Content: []wire.Node{
			{
				Tag: "privacy",
				Content: []wire.Node{
					{Tag: "category", Attrs: map[string]string{"name": category, "value": value}},
				},
			},
		},
	}
}

// fetchBlocklistNode builds the get-blocklist iq (no children):
//
//	<iq xmlns=blocklist to=@s.whatsapp.net type=get id=.../>
func fetchBlocklistNode(id string) wire.Node {
	return wire.Node{
		Tag: "iq",
		Attrs: map[string]string{
			"xmlns": "blocklist",
			"to":    sWhatsAppNet,
			"type":  "get",
			"id":    id,
		},
	}
}

// blockStatusNode builds the set-blocklist iq for one jid:
//
//	<iq xmlns=blocklist to=@s.whatsapp.net type=set id=...>
//	  <item action=block|unblock jid=<jid>/>
//	</iq>
func blockStatusNode(id, jid, action string) wire.Node {
	return wire.Node{
		Tag: "iq",
		Attrs: map[string]string{
			"xmlns": "blocklist",
			"to":    sWhatsAppNet,
			"type":  "set",
			"id":    id,
		},
		Content: []wire.Node{
			{Tag: "item", Attrs: map[string]string{"action": action, "jid": jid}},
		},
	}
}

// parsePrivacySettings extracts the {category-name: value} map from a
// get-privacy reply: <iq ...><privacy><category name= value=/>...</privacy></iq>.
// Mirrors Baileys' reduceBinaryNodeToDictionary over the privacy node's
// <category> children.
func parsePrivacySettings(reply wire.Node) map[string]string {
	out := make(map[string]string)
	privacy, ok := childByTag(reply, "privacy")
	if !ok {
		return out
	}
	for _, cat := range childrenByTag(privacy, "category") {
		name := cat.Attrs["name"]
		if name == "" {
			continue
		}
		out[name] = cat.Attrs["value"]
	}
	return out
}

// parseBlocklist extracts the blocked jids from a get-blocklist reply:
// <iq ...><list><item jid=.../>...</list></iq>.
func parseBlocklist(reply wire.Node) []string {
	list, ok := childByTag(reply, "list")
	if !ok {
		return nil
	}
	items := childrenByTag(list, "item")
	out := make([]string, 0, len(items))
	for _, it := range items {
		if jid := it.Attrs["jid"]; jid != "" {
			out = append(out, jid)
		}
	}
	return out
}

// FetchPrivacySettings returns the account's privacy settings as a
// {category-name: value} map (wire category names, e.g. "last", "online").
func (c *Client) FetchPrivacySettings(ctx context.Context) (map[string]string, error) {
	sess, ok := c.activeSession()
	if !ok {
		return nil, errors.New("client: FetchPrivacySettings requires a live session")
	}
	reply, err := c.sendIQ(ctx, sess, fetchPrivacySettingsNode(c.nextIQID("privacy")))
	if err != nil {
		return nil, err
	}
	return parsePrivacySettings(reply), nil
}

// UpdatePrivacy sets one privacy category to the given value (e.g. "all",
// "contacts", "contact_blacklist", "none"). setting is one of the Privacy*
// constants.
func (c *Client) UpdatePrivacy(ctx context.Context, setting PrivacySetting, value string) error {
	category, ok := privacyCategoryName(setting)
	if !ok {
		return errors.New("client: UpdatePrivacy: unknown setting " + string(setting))
	}
	if value == "" {
		return errors.New("client: UpdatePrivacy requires a value")
	}
	sess, live := c.activeSession()
	if !live {
		return errors.New("client: UpdatePrivacy requires a live session")
	}
	_, err := c.sendIQ(ctx, sess, updatePrivacyNode(c.nextIQID("privacy"), category, value))
	return err
}

// Block blocks the given jid.
func (c *Client) Block(ctx context.Context, jid string) error {
	return c.updateBlockStatus(ctx, jid, "block")
}

// Unblock unblocks the given jid.
func (c *Client) Unblock(ctx context.Context, jid string) error {
	return c.updateBlockStatus(ctx, jid, "unblock")
}

func (c *Client) updateBlockStatus(ctx context.Context, jid, action string) error {
	if jid == "" {
		return errors.New("client: block status requires a jid")
	}
	sess, ok := c.activeSession()
	if !ok {
		return errors.New("client: block status requires a live session")
	}
	_, err := c.sendIQ(ctx, sess, blockStatusNode(c.nextIQID("block"), jid, action))
	return err
}

// FetchBlocklist returns the jids currently on the account's block list.
func (c *Client) FetchBlocklist(ctx context.Context) ([]string, error) {
	sess, ok := c.activeSession()
	if !ok {
		return nil, errors.New("client: FetchBlocklist requires a live session")
	}
	reply, err := c.sendIQ(ctx, sess, fetchBlocklistNode(c.nextIQID("block")))
	if err != nil {
		return nil, err
	}
	return parseBlocklist(reply), nil
}
