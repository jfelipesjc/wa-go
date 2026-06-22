package client

import (
	"testing"

	"github.com/felipeleal/wa-go/internal/wire"
)

func TestFetchPrivacySettingsNode(t *testing.T) {
	n := fetchPrivacySettingsNode("p1")
	if n.Tag != "iq" {
		t.Fatalf("tag = %q", n.Tag)
	}
	attr(t, n, "xmlns", "privacy")
	attr(t, n, "to", sWhatsAppNet)
	attr(t, n, "type", "get")
	attr(t, n, "id", "p1")
	if _, ok := childByTag(n, "privacy"); !ok {
		t.Fatal("missing <privacy>")
	}
}

func TestUpdatePrivacyNode(t *testing.T) {
	n := updatePrivacyNode("p2", "last", "contacts")
	attr(t, n, "xmlns", "privacy")
	attr(t, n, "type", "set")
	priv, ok := childByTag(n, "privacy")
	if !ok {
		t.Fatal("missing <privacy>")
	}
	cat, ok := childByTag(priv, "category")
	if !ok {
		t.Fatal("missing <category>")
	}
	attr(t, cat, "name", "last")
	attr(t, cat, "value", "contacts")
}

func TestPrivacyCategoryName(t *testing.T) {
	cases := map[PrivacySetting]string{
		PrivacyLastSeen:       "last",
		PrivacyOnline:         "online",
		PrivacyProfilePicture: "profile",
		PrivacyStatus:         "status",
		PrivacyReadReceipts:   "readreceipts",
		PrivacyGroupsAdd:      "groupadd",
	}
	for setting, want := range cases {
		got, ok := privacyCategoryName(setting)
		if !ok || got != want {
			t.Fatalf("%s -> %q,%v want %q", setting, got, ok, want)
		}
	}
	if _, ok := privacyCategoryName("bogus"); ok {
		t.Fatal("bogus setting should not map")
	}
}

func TestFetchBlocklistNode(t *testing.T) {
	n := fetchBlocklistNode("b1")
	attr(t, n, "xmlns", "blocklist")
	attr(t, n, "to", sWhatsAppNet)
	attr(t, n, "type", "get")
	attr(t, n, "id", "b1")
	if children(n) != nil {
		t.Fatal("fetch blocklist must have no children")
	}
}

func TestBlockStatusNode(t *testing.T) {
	for _, action := range []string{"block", "unblock"} {
		n := blockStatusNode("b2", "555@s.whatsapp.net", action)
		attr(t, n, "xmlns", "blocklist")
		attr(t, n, "type", "set")
		it, ok := childByTag(n, "item")
		if !ok {
			t.Fatal("missing <item>")
		}
		attr(t, it, "action", action)
		attr(t, it, "jid", "555@s.whatsapp.net")
	}
}

// --- parse tests ---

func TestParsePrivacySettings(t *testing.T) {
	reply := wire.Node{
		Tag: "iq",
		Content: []wire.Node{
			{
				Tag: "privacy",
				Content: []wire.Node{
					{Tag: "category", Attrs: map[string]string{"name": "last", "value": "contacts"}},
					{Tag: "category", Attrs: map[string]string{"name": "online", "value": "all"}},
					{Tag: "category", Attrs: map[string]string{"value": "ignored"}}, // no name -> skipped
				},
			},
		},
	}
	got := parsePrivacySettings(reply)
	if got["last"] != "contacts" {
		t.Fatalf("last = %q", got["last"])
	}
	if got["online"] != "all" {
		t.Fatalf("online = %q", got["online"])
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d: %v", len(got), got)
	}
	// missing privacy node -> empty (non-nil) map
	if m := parsePrivacySettings(wire.Node{Tag: "iq"}); m == nil || len(m) != 0 {
		t.Fatalf("expected empty map, got %v", m)
	}
}

func TestParseBlocklist(t *testing.T) {
	reply := wire.Node{
		Tag: "iq",
		Content: []wire.Node{
			{
				Tag: "list",
				Content: []wire.Node{
					{Tag: "item", Attrs: map[string]string{"jid": "111@s.whatsapp.net"}},
					{Tag: "item", Attrs: map[string]string{"jid": "222@s.whatsapp.net"}},
					{Tag: "item", Attrs: map[string]string{}}, // no jid -> skipped
				},
			},
		},
	}
	got := parseBlocklist(reply)
	if len(got) != 2 || got[0] != "111@s.whatsapp.net" || got[1] != "222@s.whatsapp.net" {
		t.Fatalf("blocklist = %v", got)
	}
	if got := parseBlocklist(wire.Node{Tag: "iq"}); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}
