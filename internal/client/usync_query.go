// Package client: usync_query.go implements OnWhatsApp — checking whether a list
// of phone numbers are registered WhatsApp users via a USync "contact" query.
// This mirrors Baileys' Socket/chats.ts onWhatsApp (USyncQuery().withContactProtocol).
//
// USync contact query (Baileys getUSyncDevices is the device variant; the
// contact variant uses the contact protocol):
//
//	<iq to=@s.whatsapp.net type=get xmlns=usync id=...>
//	  <usync context=interactive mode=query sid=... last=true index=0>
//	    <query><contact/></query>
//	    <list>
//	      <user><contact>+<phone></contact></user>
//	      ...
//	    </list>
//	  </usync>
//	</iq>
//
// reply (USyncContactProtocol.parser):
//
//	<iq><usync><list>
//	  <user jid=<wid>><contact type=in|out [/]></user>   # registered: jid present
//	  <user><contact type=out/></user>                    # not registered: no jid
//	</list></usync></iq>
//
// A user is on WhatsApp when the reply's <user> node carries a jid attribute and
// its <contact> has type="in" (Baileys: exists = contactType === 'in'). We treat
// a present jid as the authoritative signal and also expose the contact type.
//
// The contact value must be the phone in international form prefixed with "+"
// (Baileys phoneNumber); callers may pass numbers with or without the leading
// "+" — normalisePhone adds it.
package client

import (
	"context"
	"errors"
	"strings"

	"github.com/felipeleal/wa-go/internal/wire"
)

// OnWhatsAppResult is the per-phone result of an OnWhatsApp query.
type OnWhatsAppResult struct {
	// Query is the phone number as supplied by the caller (echoed back so the
	// caller can correlate results to inputs).
	Query string
	// JID is the resolved WhatsApp JID when the number is registered, else "".
	JID string
	// Exists is true when the number is a registered WhatsApp user.
	Exists bool
	// ContactType is the <contact type=...> value from the reply ("in" for a
	// registered contact), informational.
	ContactType string
}

// normalisePhone trims spaces and ensures a single leading "+" on a non-empty
// number, matching the international form WhatsApp's contact protocol expects.
func normalisePhone(phone string) string {
	phone = strings.TrimSpace(phone)
	if phone == "" {
		return ""
	}
	phone = strings.TrimLeft(phone, "+")
	return "+" + phone
}

// usyncContactQueryNode builds the USync contact-protocol query iq for the given
// phone numbers (already in "+E164" form), mirroring Baileys' onWhatsApp:
//
//	<iq to=@s.whatsapp.net type=get xmlns=usync id=...>
//	  <usync context=interactive mode=query sid=... last=true index=0>
//	    <query><contact/></query>
//	    <list><user><contact>+phone</contact></user> ...</list>
//	  </usync>
//	</iq>
func usyncContactQueryNode(id, sid string, phones []string) wire.Node {
	users := make([]wire.Node, 0, len(phones))
	for _, p := range phones {
		users = append(users, wire.Node{
			Tag: "user",
			Content: []wire.Node{
				{Tag: "contact", Content: []byte(p)},
			},
		})
	}
	return wire.Node{
		Tag: "iq",
		Attrs: map[string]string{
			"to":    sWhatsAppNet,
			"type":  "get",
			"xmlns": "usync",
			"id":    id,
		},
		Content: []wire.Node{
			{
				Tag: "usync",
				Attrs: map[string]string{
					"context": "interactive",
					"mode":    "query",
					"sid":     sid,
					"last":    "true",
					"index":   "0",
				},
				Content: []wire.Node{
					{
						Tag:   "query",
						Attrs: map[string]string{},
						Content: []wire.Node{
							{Tag: "contact"},
						},
					},
					{
						Tag:     "list",
						Attrs:   map[string]string{},
						Content: users,
					},
				},
			},
		},
	}
}

// parseOnWhatsApp extracts per-user results from a USync contact reply, mirroring
// USyncContactProtocol.parser + onWhatsApp's map: each <user> carries the
// resolved jid (when registered) and a <contact type=in|out> node. exists is
// true when a jid is present (Baileys uses contactType === 'in', which coincides
// with the jid being populated).
func parseOnWhatsApp(reply wire.Node) ([]OnWhatsAppResult, error) {
	usync, ok := childByTag(reply, "usync")
	if !ok {
		return nil, errors.New("client: onWhatsApp reply missing <usync>")
	}
	list, ok := childByTag(usync, "list")
	if !ok {
		return nil, errors.New("client: onWhatsApp reply missing <list>")
	}
	var out []OnWhatsAppResult
	for _, user := range childrenByTag(list, "user") {
		res := OnWhatsAppResult{JID: user.Attrs["jid"]}
		if contact, ok := childByTag(user, "contact"); ok {
			res.ContactType = contact.Attrs["type"]
			// The queried number is echoed as the <contact> leaf content.
			res.Query = strings.TrimSpace(string(nodeBytes(contact)))
		}
		// Registered when the server resolved a jid (equivalently type=="in").
		res.Exists = res.JID != "" || res.ContactType == "in"
		out = append(out, res)
	}
	return out, nil
}

// OnWhatsApp checks whether each phone number is a registered WhatsApp user via a
// USync contact query. Numbers may be passed with or without a leading "+"
// (normalised internally). The results echo the queried number (Query), the
// resolved JID and Exists.
func (c *Client) OnWhatsApp(ctx context.Context, phones []string) ([]OnWhatsAppResult, error) {
	normalised := make([]string, 0, len(phones))
	for _, p := range phones {
		if n := normalisePhone(p); n != "" {
			normalised = append(normalised, n)
		}
	}
	if len(normalised) == 0 {
		return nil, errors.New("client: OnWhatsApp requires at least one phone number")
	}
	sess, ok := c.activeSession()
	if !ok {
		return nil, errors.New("client: OnWhatsApp requires a live session")
	}
	id := c.nextIQID("wa-go-usync-")
	sid := c.nextIQID("")
	reply, err := c.sendIQ(ctx, sess, usyncContactQueryNode(id, sid, normalised))
	if err != nil {
		return nil, err
	}
	return parseOnWhatsApp(reply)
}
