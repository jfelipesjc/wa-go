package client

import (
	"testing"

	"github.com/felipeleal/wa-go/internal/wire"
)

// --- builder tests ---

func TestBuildGroupRequestList(t *testing.T) {
	n := buildGroupRequestList("ID1", testGroupJID)
	if n.Tag != "iq" {
		t.Fatalf("tag = %q", n.Tag)
	}
	if n.Attrs["xmlns"] != xmlnsGroup || n.Attrs["type"] != "get" || n.Attrs["to"] != testGroupJID || n.Attrs["id"] != "ID1" {
		t.Fatalf("iq attrs wrong: %+v", n.Attrs)
	}
	if _, ok := childByTag(n, "membership_approval_requests"); !ok {
		t.Fatal("missing <membership_approval_requests>")
	}
}

func TestBuildGroupRequestUpdate(t *testing.T) {
	jids := []string{"5551@s.whatsapp.net", "5552@s.whatsapp.net"}
	for _, action := range []string{"approve", "reject"} {
		n := buildGroupRequestUpdate("ID2", testGroupJID, action, jids)
		if n.Attrs["xmlns"] != xmlnsGroup || n.Attrs["type"] != "set" || n.Attrs["to"] != testGroupJID {
			t.Fatalf("[%s] iq attrs wrong: %+v", action, n.Attrs)
		}
		container, ok := childByTag(n, "membership_requests_action")
		if !ok {
			t.Fatalf("[%s] missing <membership_requests_action>", action)
		}
		act, ok := childByTag(container, action)
		if !ok {
			t.Fatalf("[%s] missing <%s>", action, action)
		}
		parts := childrenByTag(act, "participant")
		if len(parts) != 2 {
			t.Fatalf("[%s] participants = %d, want 2", action, len(parts))
		}
		if parts[0].Attrs["jid"] != jids[0] || parts[1].Attrs["jid"] != jids[1] {
			t.Fatalf("[%s] participant jids wrong: %+v %+v", action, parts[0].Attrs, parts[1].Attrs)
		}
	}
}

func TestBuildGroupJoinApprovalMode(t *testing.T) {
	cases := map[bool]string{true: "on", false: "off"}
	for on, want := range cases {
		n := buildGroupJoinApprovalMode("ID3", testGroupJID, on)
		if n.Attrs["type"] != "set" || n.Attrs["to"] != testGroupJID {
			t.Fatalf("[%v] iq attrs wrong: %+v", on, n.Attrs)
		}
		mode, ok := childByTag(n, "membership_approval_mode")
		if !ok {
			t.Fatalf("[%v] missing <membership_approval_mode>", on)
		}
		gj, ok := childByTag(mode, "group_join")
		if !ok {
			t.Fatalf("[%v] missing <group_join>", on)
		}
		if gj.Attrs["state"] != want {
			t.Fatalf("[%v] state = %q, want %q", on, gj.Attrs["state"], want)
		}
	}
}

func TestBuildGroupMemberAddMode(t *testing.T) {
	n := buildGroupMemberAddMode("ID4", testGroupJID, "admin_add")
	if n.Attrs["type"] != "set" || n.Attrs["to"] != testGroupJID {
		t.Fatalf("iq attrs wrong: %+v", n.Attrs)
	}
	mode, ok := childByTag(n, "member_add_mode")
	if !ok {
		t.Fatal("missing <member_add_mode>")
	}
	if string(nodeBytes(mode)) != "admin_add" {
		t.Fatalf("member_add_mode content = %q", string(nodeBytes(mode)))
	}
}

// --- parser tests ---

func TestParseGroupRequestList(t *testing.T) {
	reply := wire.Node{
		Tag: "iq",
		Content: []wire.Node{
			{
				Tag: "membership_approval_requests",
				Content: []wire.Node{
					{Tag: "membership_approval_request", Attrs: map[string]string{
						"jid": "5551@s.whatsapp.net", "request_method": "InviteLink", "request_time": "1700000000",
					}},
					{Tag: "membership_approval_request", Attrs: map[string]string{
						"jid": "5552@s.whatsapp.net", "request_method": "NonAdminAdd",
					}},
				},
			},
		},
	}
	got := parseGroupRequestList(reply)
	if len(got) != 2 {
		t.Fatalf("got %d requests, want 2", len(got))
	}
	if got[0].JID != "5551@s.whatsapp.net" || got[0].RequestMethod != "InviteLink" || got[0].RequestTime != 1700000000 {
		t.Fatalf("req[0] = %+v", got[0])
	}
	if got[1].JID != "5552@s.whatsapp.net" || got[1].RequestMethod != "NonAdminAdd" || got[1].RequestTime != 0 {
		t.Fatalf("req[1] = %+v", got[1])
	}
}

func TestParseGroupRequestListEmpty(t *testing.T) {
	// Container present but no requests => empty list, not an error.
	reply := wire.Node{Tag: "iq", Content: []wire.Node{{Tag: "membership_approval_requests"}}}
	if got := parseGroupRequestList(reply); len(got) != 0 {
		t.Fatalf("got %d, want 0", len(got))
	}
	// Container missing => nil.
	if got := parseGroupRequestList(wire.Node{Tag: "iq"}); got != nil {
		t.Fatalf("missing container should yield nil, got %+v", got)
	}
}

func TestParseGroupRequestUpdate(t *testing.T) {
	reply := wire.Node{
		Tag: "iq",
		Content: []wire.Node{
			{
				Tag: "membership_requests_action",
				Content: []wire.Node{
					{
						Tag: "approve",
						Content: []wire.Node{
							{Tag: "participant", Attrs: map[string]string{"jid": "5551@s.whatsapp.net"}},
							{Tag: "participant", Attrs: map[string]string{"jid": "5552@s.whatsapp.net", "error": "403"}},
						},
					},
				},
			},
		},
	}
	got := parseGroupRequestUpdate(reply, "approve")
	if len(got) != 2 {
		t.Fatalf("got %d results, want 2", len(got))
	}
	if got[0].JID != "5551@s.whatsapp.net" || got[0].Status != "200" {
		t.Fatalf("res[0] = %+v (want success/200)", got[0])
	}
	if got[1].JID != "5552@s.whatsapp.net" || got[1].Status != "403" {
		t.Fatalf("res[1] = %+v (want error 403)", got[1])
	}
}

func TestParseGroupRequestUpdateMissing(t *testing.T) {
	if got := parseGroupRequestUpdate(wire.Node{Tag: "iq"}, "approve"); got != nil {
		t.Fatalf("missing container should yield nil, got %+v", got)
	}
	// Container present but wrong action child => nil.
	reply := wire.Node{Tag: "iq", Content: []wire.Node{{Tag: "membership_requests_action"}}}
	if got := parseGroupRequestUpdate(reply, "reject"); got != nil {
		t.Fatalf("missing action child should yield nil, got %+v", got)
	}
}
