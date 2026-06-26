package client

import (
	"testing"

	"github.com/jfelipesjc/wa-go/internal/wire"
)

const testGroupJID = "120363000000000000@g.us"

// --- builder tests ---

func TestBuildGroupMetadataQuery(t *testing.T) {
	n := buildGroupMetadataQuery("ID1", testGroupJID)
	if n.Tag != "iq" {
		t.Fatalf("tag = %q", n.Tag)
	}
	if n.Attrs["xmlns"] != xmlnsGroup || n.Attrs["type"] != "get" || n.Attrs["to"] != testGroupJID || n.Attrs["id"] != "ID1" {
		t.Fatalf("iq attrs wrong: %+v", n.Attrs)
	}
	q, ok := childByTag(n, "query")
	if !ok {
		t.Fatal("missing <query>")
	}
	if q.Attrs["request"] != "interactive" {
		t.Fatalf("query request = %q", q.Attrs["request"])
	}
}

func TestBuildGroupCreate(t *testing.T) {
	parts := []string{"5551@s.whatsapp.net", "5552@s.whatsapp.net"}
	n := buildGroupCreate("ID2", "KEY", "My Group", parts)
	if n.Attrs["xmlns"] != xmlnsGroup || n.Attrs["type"] != "set" || n.Attrs["to"] != groupEndpoint {
		t.Fatalf("iq attrs wrong: %+v", n.Attrs)
	}
	create, ok := childByTag(n, "create")
	if !ok {
		t.Fatal("missing <create>")
	}
	if create.Attrs["subject"] != "My Group" || create.Attrs["key"] != "KEY" {
		t.Fatalf("create attrs wrong: %+v", create.Attrs)
	}
	pnodes := childrenByTag(create, "participant")
	if len(pnodes) != 2 {
		t.Fatalf("participant count = %d", len(pnodes))
	}
	if pnodes[0].Attrs["jid"] != parts[0] || pnodes[1].Attrs["jid"] != parts[1] {
		t.Fatalf("participant jids wrong: %+v %+v", pnodes[0].Attrs, pnodes[1].Attrs)
	}
}

func TestBuildGroupParticipantsUpdate(t *testing.T) {
	for _, action := range []string{"add", "remove", "promote", "demote"} {
		n := buildGroupParticipantsUpdate("ID3", testGroupJID, action, []string{"5551@s.whatsapp.net"})
		if n.Attrs["type"] != "set" || n.Attrs["to"] != testGroupJID || n.Attrs["xmlns"] != xmlnsGroup {
			t.Fatalf("%s iq attrs wrong: %+v", action, n.Attrs)
		}
		act, ok := childByTag(n, action)
		if !ok {
			t.Fatalf("missing <%s>", action)
		}
		p, ok := childByTag(act, "participant")
		if !ok || p.Attrs["jid"] != "5551@s.whatsapp.net" {
			t.Fatalf("%s participant wrong: %+v", action, p.Attrs)
		}
	}
}

func TestBuildGroupUpdateSubject(t *testing.T) {
	n := buildGroupUpdateSubject("ID4", testGroupJID, "New Name")
	if n.Attrs["type"] != "set" || n.Attrs["to"] != testGroupJID {
		t.Fatalf("iq attrs wrong: %+v", n.Attrs)
	}
	subj, ok := childByTag(n, "subject")
	if !ok {
		t.Fatal("missing <subject>")
	}
	if string(nodeBytes(subj)) != "New Name" {
		t.Fatalf("subject bytes = %q", string(nodeBytes(subj)))
	}
}

func TestBuildGroupUpdateDescription_Set(t *testing.T) {
	n := buildGroupUpdateDescription("ID5", "DESCID", testGroupJID, "Hello there")
	desc, ok := childByTag(n, "description")
	if !ok {
		t.Fatal("missing <description>")
	}
	if desc.Attrs["id"] != "DESCID" {
		t.Fatalf("description id = %q", desc.Attrs["id"])
	}
	if _, del := desc.Attrs["delete"]; del {
		t.Fatal("set should not carry delete attr")
	}
	body, ok := childByTag(desc, "body")
	if !ok || string(nodeBytes(body)) != "Hello there" {
		t.Fatalf("body wrong: %q", string(nodeBytes(body)))
	}
}

func TestBuildGroupUpdateDescription_Delete(t *testing.T) {
	n := buildGroupUpdateDescription("ID6", "DESCID", testGroupJID, "")
	desc, ok := childByTag(n, "description")
	if !ok {
		t.Fatal("missing <description>")
	}
	if desc.Attrs["delete"] != "true" {
		t.Fatalf("delete attr = %q", desc.Attrs["delete"])
	}
	if _, ok := childByTag(desc, "body"); ok {
		t.Fatal("delete should not carry <body>")
	}
}

func TestBuildGroupLeave(t *testing.T) {
	n := buildGroupLeave("ID7", testGroupJID)
	if n.Attrs["type"] != "set" || n.Attrs["to"] != groupEndpoint {
		t.Fatalf("iq attrs wrong: %+v", n.Attrs)
	}
	leave, ok := childByTag(n, "leave")
	if !ok {
		t.Fatal("missing <leave>")
	}
	g, ok := childByTag(leave, "group")
	if !ok || g.Attrs["id"] != testGroupJID {
		t.Fatalf("group node wrong: %+v", g.Attrs)
	}
}

func TestBuildGroupInviteCode(t *testing.T) {
	n := buildGroupInviteCode("ID8", testGroupJID)
	if n.Attrs["type"] != "get" || n.Attrs["to"] != testGroupJID {
		t.Fatalf("iq attrs wrong: %+v", n.Attrs)
	}
	if _, ok := childByTag(n, "invite"); !ok {
		t.Fatal("missing <invite>")
	}
}

func TestBuildGroupRevokeInvite(t *testing.T) {
	n := buildGroupRevokeInvite("ID9", testGroupJID)
	if n.Attrs["type"] != "set" || n.Attrs["to"] != testGroupJID {
		t.Fatalf("iq attrs wrong: %+v", n.Attrs)
	}
	if _, ok := childByTag(n, "invite"); !ok {
		t.Fatal("missing <invite>")
	}
}

func TestBuildGroupAcceptInvite(t *testing.T) {
	n := buildGroupAcceptInvite("ID10", "ABC123")
	if n.Attrs["type"] != "set" || n.Attrs["to"] != groupEndpoint {
		t.Fatalf("iq attrs wrong: %+v", n.Attrs)
	}
	inv, ok := childByTag(n, "invite")
	if !ok || inv.Attrs["code"] != "ABC123" {
		t.Fatalf("invite node wrong: %+v", inv.Attrs)
	}
}

func TestBuildGroupSettingUpdate(t *testing.T) {
	for _, setting := range []string{"announce", "not_announce", "locked", "unlocked"} {
		n := buildGroupSettingUpdate("ID11", testGroupJID, setting)
		if n.Attrs["type"] != "set" || n.Attrs["to"] != testGroupJID {
			t.Fatalf("%s iq attrs wrong: %+v", setting, n.Attrs)
		}
		if _, ok := childByTag(n, setting); !ok {
			t.Fatalf("missing <%s>", setting)
		}
	}
}

// --- parse tests ---

// syntheticMetadataReply builds an <iq result> whose <group> child mirrors what
// the server returns for a w:g2 interactive query (the shape extractGroupMetadata
// consumes in Baileys).
func syntheticMetadataReply() wire.Node {
	group := wire.Node{
		Tag: "group",
		Attrs: map[string]string{
			"id":       "120363111@g.us",
			"subject":  "Test Subject",
			"creator":  "5551@s.whatsapp.net",
			"creation": "1700000000",
		},
		Content: []wire.Node{
			{
				Tag:     "description",
				Attrs:   map[string]string{"id": "D1"},
				Content: []wire.Node{{Tag: "body", Content: []byte("the desc")}},
			},
			{Tag: "participant", Attrs: map[string]string{"jid": "5551@s.whatsapp.net", "type": "superadmin"}},
			{Tag: "participant", Attrs: map[string]string{"jid": "5552@s.whatsapp.net", "type": "admin"}},
			{Tag: "participant", Attrs: map[string]string{"jid": "5553@s.whatsapp.net"}},
		},
	}
	return wire.Node{Tag: "iq", Attrs: map[string]string{"type": "result"}, Content: []wire.Node{group}}
}

func TestParseGroupMetadata(t *testing.T) {
	info, err := parseGroupMetadata(syntheticMetadataReply())
	if err != nil {
		t.Fatalf("parse err: %v", err)
	}
	if info.JID != "120363111@g.us" {
		t.Fatalf("jid = %q", info.JID)
	}
	if info.Subject != "Test Subject" {
		t.Fatalf("subject = %q", info.Subject)
	}
	if info.Owner != "5551@s.whatsapp.net" {
		t.Fatalf("owner = %q", info.Owner)
	}
	if info.Creation != 1700000000 {
		t.Fatalf("creation = %d", info.Creation)
	}
	if info.Desc != "the desc" {
		t.Fatalf("desc = %q", info.Desc)
	}
	if len(info.Participants) != 3 {
		t.Fatalf("participant count = %d", len(info.Participants))
	}
	if !info.Participants[0].IsAdmin || !info.Participants[0].IsSuperAdmin {
		t.Fatalf("p0 should be superadmin: %+v", info.Participants[0])
	}
	if !info.Participants[1].IsAdmin || info.Participants[1].IsSuperAdmin {
		t.Fatalf("p1 should be plain admin: %+v", info.Participants[1])
	}
	if info.Participants[2].IsAdmin || info.Participants[2].IsSuperAdmin {
		t.Fatalf("p2 should be member: %+v", info.Participants[2])
	}
}

func TestParseGroupMetadata_BareIDGetsSuffix(t *testing.T) {
	group := wire.Node{Tag: "group", Attrs: map[string]string{"id": "120363222"}}
	reply := wire.Node{Tag: "iq", Content: []wire.Node{group}}
	info, err := parseGroupMetadata(reply)
	if err != nil {
		t.Fatalf("parse err: %v", err)
	}
	if info.JID != "120363222@g.us" {
		t.Fatalf("jid = %q", info.JID)
	}
}

func TestParseGroupMetadata_MissingGroup(t *testing.T) {
	if _, err := parseGroupMetadata(wire.Node{Tag: "iq"}); err == nil {
		t.Fatal("expected error for missing <group>")
	}
}

func TestParseInviteCode(t *testing.T) {
	reply := wire.Node{
		Tag:     "iq",
		Content: []wire.Node{{Tag: "invite", Attrs: map[string]string{"code": "XYZ789"}}},
	}
	code, err := parseInviteCode(reply)
	if err != nil {
		t.Fatalf("parse err: %v", err)
	}
	if code != "XYZ789" {
		t.Fatalf("code = %q", code)
	}
}

func TestParseInviteCode_Missing(t *testing.T) {
	if _, err := parseInviteCode(wire.Node{Tag: "iq"}); err == nil {
		t.Fatal("expected error for missing <invite>")
	}
}

func TestParseAcceptInvite(t *testing.T) {
	reply := wire.Node{
		Tag:     "iq",
		Content: []wire.Node{{Tag: "group", Attrs: map[string]string{"jid": "120363333@g.us"}}},
	}
	jid, err := parseAcceptInvite(reply)
	if err != nil {
		t.Fatalf("parse err: %v", err)
	}
	if jid != "120363333@g.us" {
		t.Fatalf("jid = %q", jid)
	}
}

func TestParseParticipantsUpdate(t *testing.T) {
	reply := wire.Node{
		Tag: "iq",
		Content: []wire.Node{
			{
				Tag: "add",
				Content: []wire.Node{
					{Tag: "participant", Attrs: map[string]string{"jid": "5551@s.whatsapp.net"}},
					{Tag: "participant", Attrs: map[string]string{"jid": "5552@s.whatsapp.net", "error": "403"}},
				},
			},
		},
	}
	res := parseParticipantsUpdate(reply, "add")
	if len(res) != 2 {
		t.Fatalf("result count = %d", len(res))
	}
	if res[0].JID != "5551@s.whatsapp.net" || res[0].Status != "200" {
		t.Fatalf("res0 wrong: %+v", res[0])
	}
	if res[1].JID != "5552@s.whatsapp.net" || res[1].Status != "403" {
		t.Fatalf("res1 wrong: %+v", res[1])
	}
}
