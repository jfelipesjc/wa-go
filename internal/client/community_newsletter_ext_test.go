package client

import (
	"testing"

	"github.com/jfelipesjc/wa-go/internal/wire"
)

func TestNewsletterExtConstants(t *testing.T) {
	// Guard the verbatim Baileys query ID / data path against accidental edits.
	if nlQueryDelete != "30062808666639665" {
		t.Errorf("nlQueryDelete = %q", nlQueryDelete)
	}
	if nlPathDelete != "xwa2_newsletter_delete_v2" {
		t.Errorf("nlPathDelete = %q", nlPathDelete)
	}
}

func TestIsNewsletterJID(t *testing.T) {
	cases := map[string]bool{
		"123@newsletter":         true,
		"123@g.us":               false,
		"5512999@s.whatsapp.net": false,
		"":                       false,
		"newsletter":             false,
	}
	for jid, want := range cases {
		if got := isNewsletterJID(jid); got != want {
			t.Errorf("isNewsletterJID(%q) = %v, want %v", jid, got, want)
		}
	}
}

func TestBuildNewsletterReaction_Add(t *testing.T) {
	n := buildNewsletterReaction("tag-1", "120@newsletter", "99", "👍")
	if n.Tag != "message" {
		t.Fatalf("tag = %q, want message", n.Tag)
	}
	for k, want := range map[string]string{
		"to":        "120@newsletter",
		"id":        "tag-1",
		"server_id": "99",
		"type":      "reaction",
	} {
		if n.Attrs[k] != want {
			t.Errorf("attr %s = %q, want %q", k, n.Attrs[k], want)
		}
	}
	if _, has := n.Attrs["edit"]; has {
		t.Error("add reaction must not carry edit attr")
	}
	r, ok := childByTag(n, "reaction")
	if !ok {
		t.Fatal("missing <reaction> child")
	}
	if r.Attrs["code"] != "👍" {
		t.Errorf("reaction code = %q, want 👍", r.Attrs["code"])
	}
}

func TestBuildNewsletterReaction_Remove(t *testing.T) {
	n := buildNewsletterReaction("tag-2", "120@newsletter", "99", "")
	if n.Attrs["edit"] != "7" {
		t.Errorf("remove reaction edit = %q, want 7", n.Attrs["edit"])
	}
	r, ok := childByTag(n, "reaction")
	if !ok {
		t.Fatal("missing <reaction> child")
	}
	if code, has := r.Attrs["code"]; has {
		t.Errorf("removal <reaction> must be empty, got code=%q", code)
	}
}

// TestParseAllCommunities feeds a w:g2 participating reply that mixes regular
// groups and communities (the latter carrying a <parent> marker) under <groups>,
// the real shape observed on the wire, and asserts only the communities surface.
func TestParseAllCommunities(t *testing.T) {
	reply := wire.Node{Content: []wire.Node{
		{Tag: "groups", Content: []wire.Node{
			{Tag: "group", Attrs: map[string]string{"id": "123", "subject": "Comm A"},
				Content: []wire.Node{{Tag: "parent", Attrs: map[string]string{}}}},
			{Tag: "group", Attrs: map[string]string{"id": "999", "subject": "Regular group — skipped"}},
			{Tag: "group", Attrs: map[string]string{"jid": "456@g.us", "subject": "Comm B"},
				Content: []wire.Node{{Tag: "parent", Attrs: map[string]string{}}}},
		}},
	}}
	got := parseAllCommunities(reply)
	if len(got) != 2 {
		t.Fatalf("got %d communities, want 2: %+v", len(got), got)
	}
	if got[0].JID != "123@g.us" || got[0].Subject != "Comm A" {
		t.Errorf("community[0] = %+v, want {123@g.us, Comm A}", got[0])
	}
	if got[1].JID != "456@g.us" || got[1].Subject != "Comm B" {
		t.Errorf("community[1] = %+v, want {456@g.us, Comm B}", got[1])
	}
}

func TestParseAllCommunities_NoContainer(t *testing.T) {
	if got := parseAllCommunities(wire.Node{}); got != nil {
		t.Errorf("missing <groups> should yield nil, got %+v", got)
	}
}
