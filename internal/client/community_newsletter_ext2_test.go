package client

import (
	"testing"

	"github.com/jfelipesjc/wa-go/internal/wire"
)

func TestBuildCommunityCreateGroup(t *testing.T) {
	const community = "120363000000000001@g.us"
	n := buildCommunityCreateGroup("id-1", "key-1", "Sub Group", community, []string{
		"5511999990000",                // bare -> normalized
		"5511888880000@s.whatsapp.net", // already addressed -> unchanged
	})
	if n.Tag != "iq" {
		t.Fatalf("tag = %q", n.Tag)
	}
	for k, want := range map[string]string{
		"type": "set",
		"to":   "@g.us",
		"id":   "id-1",
	} {
		if n.Attrs[k] != want {
			t.Errorf("attr %s = %q, want %q", k, n.Attrs[k], want)
		}
	}
	create, ok := childByTag(n, "create")
	if !ok {
		t.Fatal("missing <create> node")
	}
	if create.Attrs["subject"] != "Sub Group" {
		t.Errorf("subject = %q", create.Attrs["subject"])
	}
	if create.Attrs["key"] != "key-1" {
		t.Errorf("key = %q", create.Attrs["key"])
	}
	parts := childrenByTag(create, "participant")
	if len(parts) != 2 {
		t.Fatalf("participants = %d, want 2", len(parts))
	}
	if parts[0].Attrs["jid"] != "5511999990000@s.whatsapp.net" {
		t.Errorf("participant[0] jid = %q, want normalized", parts[0].Attrs["jid"])
	}
	if parts[1].Attrs["jid"] != "5511888880000@s.whatsapp.net" {
		t.Errorf("participant[1] jid = %q", parts[1].Attrs["jid"])
	}
	parent, ok := childByTag(create, "linked_parent")
	if !ok {
		t.Fatal("missing <linked_parent> node")
	}
	if parent.Attrs["jid"] != community {
		t.Errorf("linked_parent jid = %q, want %q", parent.Attrs["jid"], community)
	}
}

func TestBuildLinkedGroupsParticipants(t *testing.T) {
	const community = "120363000000000001@g.us"
	n := buildLinkedGroupsParticipants("id-2", community)
	for k, want := range map[string]string{
		"type": "get",
		"to":   community,
		"id":   "id-2",
	} {
		if n.Attrs[k] != want {
			t.Errorf("attr %s = %q, want %q", k, n.Attrs[k], want)
		}
	}
	if _, ok := childByTag(n, "linked_groups_participants"); !ok {
		t.Fatal("missing <linked_groups_participants> child")
	}
}

func TestParseLinkedGroupsParticipants(t *testing.T) {
	reply := wire.Node{
		Tag: "iq",
		Content: []wire.Node{
			{
				Tag: "linked_groups_participants",
				Content: []wire.Node{
					{Tag: "participant", Attrs: map[string]string{"jid": "5511999990000@s.whatsapp.net"}},
					{Tag: "participant", Attrs: map[string]string{"jid": "5511888880000@s.whatsapp.net"}},
					{Tag: "participant", Attrs: map[string]string{}}, // missing jid -> skipped
				},
			},
		},
	}
	got := parseLinkedGroupsParticipants(reply)
	if len(got) != 2 {
		t.Fatalf("participants = %v, want 2", got)
	}
	if got[0] != "5511999990000@s.whatsapp.net" || got[1] != "5511888880000@s.whatsapp.net" {
		t.Errorf("participants = %v", got)
	}

	// Empty reply -> nil/empty slice, no panic.
	if p := parseLinkedGroupsParticipants(wire.Node{Tag: "iq"}); len(p) != 0 {
		t.Errorf("empty reply gave %v", p)
	}
}

func TestBuildAcceptTOSNotice(t *testing.T) {
	n := buildAcceptTOSNotice("id-3")
	for k, want := range map[string]string{
		"id":    "id-3",
		"xmlns": "tos",
		"type":  "set",
		"to":    sWhatsAppNet,
	} {
		if n.Attrs[k] != want {
			t.Errorf("attr %s = %q, want %q", k, n.Attrs[k], want)
		}
	}
	notice, ok := childByTag(n, "notice")
	if !ok {
		t.Fatal("missing <notice> child")
	}
	if notice.Attrs["id"] != "20601218" {
		t.Errorf("notice id = %q, want 20601218", notice.Attrs["id"])
	}
	if notice.Attrs["stage"] != "5" {
		t.Errorf("notice stage = %q, want 5", notice.Attrs["stage"])
	}
}

func TestBuildNewsletterMarkViewed(t *testing.T) {
	const jid = "120363000000000002@newsletter"
	n := buildNewsletterMarkViewed("id-4", jid, []string{"101", "102", "103"})
	if n.Tag != "receipt" {
		t.Fatalf("tag = %q, want receipt", n.Tag)
	}
	for k, want := range map[string]string{
		"to":   jid,
		"type": "view",
		"id":   "id-4",
	} {
		if n.Attrs[k] != want {
			t.Errorf("attr %s = %q, want %q", k, n.Attrs[k], want)
		}
	}
	list, ok := childByTag(n, "list")
	if !ok {
		t.Fatal("missing <list> child")
	}
	items := childrenByTag(list, "item")
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3", len(items))
	}
	for i, want := range []string{"101", "102", "103"} {
		if items[i].Attrs["server_id"] != want {
			t.Errorf("item[%d] server_id = %q, want %q", i, items[i].Attrs["server_id"], want)
		}
	}
}

func TestBuildNewsletterFetchMessagesAfter(t *testing.T) {
	// after != 0 -> attribute present.
	n := buildNewsletterFetchMessages("id-5", "j@newsletter", 20, 0, 1700009999)
	mu, ok := childByTag(n, "message_updates")
	if !ok {
		t.Fatal("missing message_updates child")
	}
	if mu.Attrs["after"] != "1700009999" {
		t.Errorf("after = %q, want 1700009999", mu.Attrs["after"])
	}

	// after == 0 -> attribute omitted.
	n2 := buildNewsletterFetchMessages("id-6", "j@newsletter", 20, 0, 0)
	mu2, _ := childByTag(n2, "message_updates")
	if _, ok := mu2.Attrs["after"]; ok {
		t.Errorf("after should be omitted when 0")
	}
}

func TestEnsureUserJID(t *testing.T) {
	cases := map[string]string{
		"5511999990000":                "5511999990000@s.whatsapp.net",
		"5511999990000@s.whatsapp.net": "5511999990000@s.whatsapp.net",
		"123@lid":                      "123@lid",
	}
	for in, want := range cases {
		if got := ensureUserJID(in); got != want {
			t.Errorf("ensureUserJID(%q) = %q, want %q", in, got, want)
		}
	}
}
