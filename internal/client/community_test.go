package client

import (
	"testing"

	"github.com/jfelipesjc/wa-go/internal/wire"
)

func TestBuildSubGroupsQuery(t *testing.T) {
	n := buildSubGroupsQuery("id-1", "comm@g.us")
	for k, want := range map[string]string{
		"id":    "id-1",
		"xmlns": "w:g2",
		"type":  "get",
		"to":    "comm@g.us",
	} {
		if n.Attrs[k] != want {
			t.Errorf("attr %s = %q, want %q", k, n.Attrs[k], want)
		}
	}
	if _, ok := childByTag(n, "sub_groups"); !ok {
		t.Error("missing <sub_groups> child")
	}
}

func TestBuildLinkGroupQuery(t *testing.T) {
	n := buildLinkGroupQuery("id-2", "comm@g.us", "sub@g.us")
	if n.Attrs["type"] != "set" || n.Attrs["to"] != "comm@g.us" || n.Attrs["xmlns"] != "w:g2" {
		t.Fatalf("envelope attrs = %v", n.Attrs)
	}
	links, ok := childByTag(n, "links")
	if !ok {
		t.Fatal("missing <links>")
	}
	link, ok := childByTag(links, "link")
	if !ok {
		t.Fatal("missing <link>")
	}
	if link.Attrs["link_type"] != "sub_group" {
		t.Errorf("link_type = %q", link.Attrs["link_type"])
	}
	group, ok := childByTag(link, "group")
	if !ok {
		t.Fatal("missing <group>")
	}
	if group.Attrs["id"] != "sub@g.us" {
		t.Errorf("group id = %q", group.Attrs["id"])
	}
}

func TestBuildUnlinkGroupQuery(t *testing.T) {
	n := buildUnlinkGroupQuery("id-3", "comm@g.us", "sub@g.us")
	unlink, ok := childByTag(n, "unlink")
	if !ok {
		t.Fatal("missing <unlink>")
	}
	if unlink.Attrs["unlink_type"] != "sub_group" {
		t.Errorf("unlink_type = %q", unlink.Attrs["unlink_type"])
	}
	group, ok := childByTag(unlink, "group")
	if !ok {
		t.Fatal("missing <group>")
	}
	if group.Attrs["id"] != "sub@g.us" {
		t.Errorf("group id = %q", group.Attrs["id"])
	}
}

func TestParseSubGroups(t *testing.T) {
	reply := wire.Node{
		Tag: "iq",
		Content: []wire.Node{
			{
				Tag: "sub_groups",
				Content: []wire.Node{
					{Tag: "group", Attrs: map[string]string{"jid": "a@g.us", "subject": "First"}},
					// bare id without suffix should be normalized to @g.us
					{Tag: "group", Attrs: map[string]string{"id": "12345", "subject": "Second"}},
					// no jid/id -> skipped
					{Tag: "group", Attrs: map[string]string{"subject": "ghost"}},
				},
			},
		},
	}
	got := parseSubGroups(reply)
	if len(got) != 2 {
		t.Fatalf("got %d sub-groups, want 2: %+v", len(got), got)
	}
	if got[0].JID != "a@g.us" || got[0].Subject != "First" {
		t.Errorf("got[0] = %+v", got[0])
	}
	if got[1].JID != "12345@g.us" || got[1].Subject != "Second" {
		t.Errorf("got[1] = %+v", got[1])
	}
}

func TestParseSubGroupsEmpty(t *testing.T) {
	if got := parseSubGroups(wire.Node{Tag: "iq"}); got != nil {
		t.Errorf("expected nil for missing <sub_groups>, got %+v", got)
	}
}

func TestParseLinkResultSuccess(t *testing.T) {
	// Bare result -> nil.
	if err := parseLinkResult(wire.Node{Tag: "iq"}, "links"); err != nil {
		t.Errorf("bare result: %v", err)
	}
	// Container present, no error -> nil.
	reply := wire.Node{
		Tag: "iq",
		Content: []wire.Node{
			{Tag: "links", Content: []wire.Node{
				{Tag: "link", Attrs: map[string]string{"link_type": "sub_group"}},
			}},
		},
	}
	if err := parseLinkResult(reply, "links"); err != nil {
		t.Errorf("success reply: %v", err)
	}
}

func TestParseLinkResultError(t *testing.T) {
	reply := wire.Node{
		Tag: "iq",
		Content: []wire.Node{
			{Tag: "links", Content: []wire.Node{
				{Tag: "link", Content: []wire.Node{
					{Tag: "error", Attrs: map[string]string{"code": "403"}},
				}},
			}},
		},
	}
	if err := parseLinkResult(reply, "links"); err == nil {
		t.Fatal("expected error for code 403")
	}
	// error code 200 should be treated as success.
	ok200 := wire.Node{
		Tag: "iq",
		Content: []wire.Node{
			{Tag: "unlink", Content: []wire.Node{
				{Tag: "error", Attrs: map[string]string{"code": "200"}},
			}},
		},
	}
	if err := parseLinkResult(ok200, "unlink"); err != nil {
		t.Errorf("code 200 should be success: %v", err)
	}
}
