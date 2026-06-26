package client

import (
	"testing"

	"github.com/jfelipesjc/wa-go/internal/wire"
)

// assertEnvelope checks the shared w:g2 iq envelope attrs.
func assertEnvelope(t *testing.T, n wire.Node, typ, to string) {
	t.Helper()
	if n.Tag != "iq" {
		t.Fatalf("tag = %q, want iq", n.Tag)
	}
	if n.Attrs["xmlns"] != "w:g2" {
		t.Errorf("xmlns = %q, want w:g2", n.Attrs["xmlns"])
	}
	if n.Attrs["type"] != typ {
		t.Errorf("type = %q, want %q", n.Attrs["type"], typ)
	}
	if n.Attrs["to"] != to {
		t.Errorf("to = %q, want %q", n.Attrs["to"], to)
	}
}

func TestBuildCommunityCreate(t *testing.T) {
	n := buildCommunityCreate("id-1", "DESC12345678", "My Comm", "hello")
	assertEnvelope(t, n, "set", "@g.us")

	create, ok := childByTag(n, "create")
	if !ok {
		t.Fatal("missing <create>")
	}
	if create.Attrs["subject"] != "My Comm" {
		t.Errorf("subject = %q", create.Attrs["subject"])
	}
	desc, ok := childByTag(create, "description")
	if !ok {
		t.Fatal("missing <description>")
	}
	if desc.Attrs["id"] != "DESC12345678" {
		t.Errorf("desc id = %q", desc.Attrs["id"])
	}
	body, ok := childByTag(desc, "body")
	if !ok {
		t.Fatal("missing <body>")
	}
	if string(nodeBytes(body)) != "hello" {
		t.Errorf("body = %q", string(nodeBytes(body)))
	}
	parent, ok := childByTag(create, "parent")
	if !ok {
		t.Fatal("missing <parent> (marks a community)")
	}
	if parent.Attrs["default_membership_approval_mode"] != "request_required" {
		t.Errorf("parent approval mode = %q", parent.Attrs["default_membership_approval_mode"])
	}
	if _, ok := childByTag(create, "allow_non_admin_sub_group_creation"); !ok {
		t.Error("missing <allow_non_admin_sub_group_creation>")
	}
	if _, ok := childByTag(create, "create_general_chat"); !ok {
		t.Error("missing <create_general_chat>")
	}
}

func TestBuildCommunityParticipantsUpdate(t *testing.T) {
	n := buildCommunityParticipantsUpdate("id-2", "comm@g.us", "add", []string{"a@s.whatsapp.net", "b@s.whatsapp.net"})
	assertEnvelope(t, n, "set", "comm@g.us")
	add, ok := childByTag(n, "add")
	if !ok {
		t.Fatal("missing <add>")
	}
	if _, has := add.Attrs["linked_groups"]; has {
		t.Error("add must not carry linked_groups")
	}
	parts := childrenByTag(add, "participant")
	if len(parts) != 2 {
		t.Fatalf("participants = %d, want 2", len(parts))
	}
	if parts[0].Attrs["jid"] != "a@s.whatsapp.net" {
		t.Errorf("part0 jid = %q", parts[0].Attrs["jid"])
	}
}

func TestBuildCommunityParticipantsUpdateRemoveLinkedGroups(t *testing.T) {
	n := buildCommunityParticipantsUpdate("id-3", "comm@g.us", "remove", []string{"a@s.whatsapp.net"})
	rm, ok := childByTag(n, "remove")
	if !ok {
		t.Fatal("missing <remove>")
	}
	if rm.Attrs["linked_groups"] != "true" {
		t.Errorf("remove linked_groups = %q, want true", rm.Attrs["linked_groups"])
	}
}

func TestBuildCommunityRequestList(t *testing.T) {
	n := buildCommunityRequestList("id-4", "comm@g.us")
	assertEnvelope(t, n, "get", "comm@g.us")
	if _, ok := childByTag(n, "membership_approval_requests"); !ok {
		t.Error("missing <membership_approval_requests>")
	}
}

func TestBuildCommunityRequestUpdate(t *testing.T) {
	n := buildCommunityRequestUpdate("id-5", "comm@g.us", "approve", []string{"a@s.whatsapp.net"})
	assertEnvelope(t, n, "set", "comm@g.us")
	mra, ok := childByTag(n, "membership_requests_action")
	if !ok {
		t.Fatal("missing <membership_requests_action>")
	}
	approve, ok := childByTag(mra, "approve")
	if !ok {
		t.Fatal("missing <approve>")
	}
	parts := childrenByTag(approve, "participant")
	if len(parts) != 1 || parts[0].Attrs["jid"] != "a@s.whatsapp.net" {
		t.Errorf("participants = %v", parts)
	}
}

func TestBuildCommunityLeave(t *testing.T) {
	n := buildCommunityLeave("id-6", "comm@g.us")
	assertEnvelope(t, n, "set", "@g.us")
	leave, ok := childByTag(n, "leave")
	if !ok {
		t.Fatal("missing <leave>")
	}
	comm, ok := childByTag(leave, "community")
	if !ok {
		t.Fatal("missing <community> under leave (not <group>)")
	}
	if comm.Attrs["id"] != "comm@g.us" {
		t.Errorf("community id = %q", comm.Attrs["id"])
	}
}

func TestBuildCommunityUpdateSubject(t *testing.T) {
	n := buildCommunityUpdateSubject("id-7", "comm@g.us", "New Name")
	assertEnvelope(t, n, "set", "comm@g.us")
	subj, ok := childByTag(n, "subject")
	if !ok {
		t.Fatal("missing <subject>")
	}
	if string(nodeBytes(subj)) != "New Name" {
		t.Errorf("subject bytes = %q", string(nodeBytes(subj)))
	}
}

func TestBuildCommunityUpdateDescriptionSet(t *testing.T) {
	n := buildCommunityUpdateDescription("id-8", "MSGID", "PREVID", "comm@g.us", "the desc")
	desc, ok := childByTag(n, "description")
	if !ok {
		t.Fatal("missing <description>")
	}
	if desc.Attrs["id"] != "MSGID" {
		t.Errorf("desc id = %q", desc.Attrs["id"])
	}
	if desc.Attrs["prev"] != "PREVID" {
		t.Errorf("prev = %q", desc.Attrs["prev"])
	}
	if _, del := desc.Attrs["delete"]; del {
		t.Error("set description must not carry delete")
	}
	body, ok := childByTag(desc, "body")
	if !ok || string(nodeBytes(body)) != "the desc" {
		t.Errorf("body = %v", body)
	}
}

func TestBuildCommunityUpdateDescriptionDelete(t *testing.T) {
	n := buildCommunityUpdateDescription("id-9", "MSGID", "", "comm@g.us", "")
	desc, ok := childByTag(n, "description")
	if !ok {
		t.Fatal("missing <description>")
	}
	if desc.Attrs["delete"] != "true" {
		t.Errorf("delete = %q, want true", desc.Attrs["delete"])
	}
	if _, has := desc.Attrs["id"]; has {
		t.Error("delete description must not carry id")
	}
	if _, ok := childByTag(desc, "body"); ok {
		t.Error("delete description must not carry <body>")
	}
}

func TestBuildCommunityToggleEphemeral(t *testing.T) {
	on := buildCommunityToggleEphemeral("id-10", "comm@g.us", 86400)
	assertEnvelope(t, on, "set", "comm@g.us")
	eph, ok := childByTag(on, "ephemeral")
	if !ok {
		t.Fatal("missing <ephemeral>")
	}
	if eph.Attrs["expiration"] != "86400" {
		t.Errorf("expiration = %q", eph.Attrs["expiration"])
	}

	off := buildCommunityToggleEphemeral("id-11", "comm@g.us", 0)
	if _, ok := childByTag(off, "not_ephemeral"); !ok {
		t.Error("disable must use <not_ephemeral>")
	}
	if _, ok := childByTag(off, "ephemeral"); ok {
		t.Error("disable must not carry <ephemeral>")
	}
}

func TestBuildCommunitySettingUpdate(t *testing.T) {
	n := buildCommunitySettingUpdate("id-12", "comm@g.us", "modify_only_admins")
	assertEnvelope(t, n, "set", "comm@g.us")
	if _, ok := childByTag(n, "modify_only_admins"); !ok {
		t.Error("missing <modify_only_admins>")
	}
}

func TestBuildCommunityMemberAddMode(t *testing.T) {
	n := buildCommunityMemberAddMode("id-13", "comm@g.us", "all_member_add")
	assertEnvelope(t, n, "set", "comm@g.us")
	mode, ok := childByTag(n, "member_add_mode")
	if !ok {
		t.Fatal("missing <member_add_mode>")
	}
	if string(nodeBytes(mode)) != "all_member_add" {
		t.Errorf("mode bytes = %q", string(nodeBytes(mode)))
	}
}

func TestBuildCommunityJoinApprovalMode(t *testing.T) {
	n := buildCommunityJoinApprovalMode("id-14", "comm@g.us", "on")
	assertEnvelope(t, n, "set", "comm@g.us")
	mam, ok := childByTag(n, "membership_approval_mode")
	if !ok {
		t.Fatal("missing <membership_approval_mode>")
	}
	cj, ok := childByTag(mam, "community_join")
	if !ok {
		t.Fatal("missing <community_join>")
	}
	if cj.Attrs["state"] != "on" {
		t.Errorf("state = %q", cj.Attrs["state"])
	}
}

func TestItoa(t *testing.T) {
	cases := map[int]string{0: "0", 7: "7", 86400: "86400", -5: "-5"}
	for in, want := range cases {
		if got := itoa(in); got != want {
			t.Errorf("itoa(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestParseCommunityRequestList(t *testing.T) {
	reply := wire.Node{
		Tag: "iq",
		Content: []wire.Node{
			{
				Tag: "membership_approval_requests",
				Content: []wire.Node{
					{Tag: "membership_approval_request", Attrs: map[string]string{"jid": "a@s.whatsapp.net"}},
					{Tag: "membership_approval_request", Attrs: map[string]string{"jid": "b@s.whatsapp.net"}},
				},
			},
		},
	}
	got := parseCommunityRequestList(reply)
	if len(got) != 2 {
		t.Fatalf("got %d requests, want 2", len(got))
	}
	if got[0].JID != "a@s.whatsapp.net" {
		t.Errorf("req0 jid = %q", got[0].JID)
	}
	if got[1].Attrs["jid"] != "b@s.whatsapp.net" {
		t.Errorf("req1 attrs jid = %q", got[1].Attrs["jid"])
	}
}

func TestParseCommunityRequestListEmpty(t *testing.T) {
	if got := parseCommunityRequestList(wire.Node{Tag: "iq"}); got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestParseCommunityRequestUpdate(t *testing.T) {
	reply := wire.Node{
		Tag: "iq",
		Content: []wire.Node{
			{
				Tag: "membership_requests_action",
				Content: []wire.Node{
					{
						Tag: "approve",
						Content: []wire.Node{
							{Tag: "participant", Attrs: map[string]string{"jid": "a@s.whatsapp.net"}},
							{Tag: "participant", Attrs: map[string]string{"jid": "b@s.whatsapp.net", "error": "403"}},
						},
					},
				},
			},
		},
	}
	got := parseCommunityRequestUpdate(reply, "approve")
	if len(got) != 2 {
		t.Fatalf("got %d, want 2", len(got))
	}
	if got[0].Status != "200" {
		t.Errorf("ok status = %q, want 200", got[0].Status)
	}
	if got[1].Status != "403" {
		t.Errorf("err status = %q, want 403", got[1].Status)
	}
}
