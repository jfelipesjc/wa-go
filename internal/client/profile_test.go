package client

import (
	"bytes"
	"testing"

	"github.com/jfelipesjc/wa-go/internal/store"
	"github.com/jfelipesjc/wa-go/internal/wire"
)

func attr(t *testing.T, n wire.Node, k, want string) {
	t.Helper()
	if got := n.Attrs[k]; got != want {
		t.Fatalf("attr %q = %q, want %q", k, got, want)
	}
}

func TestUpdateProfileStatusNode(t *testing.T) {
	n := updateProfileStatusNode("id1", "hello world")
	if n.Tag != "iq" {
		t.Fatalf("tag = %q", n.Tag)
	}
	attr(t, n, "to", sWhatsAppNet)
	attr(t, n, "type", "set")
	attr(t, n, "xmlns", "status")
	attr(t, n, "id", "id1")
	st, ok := childByTag(n, "status")
	if !ok {
		t.Fatal("missing <status>")
	}
	if string(nodeBytes(st)) != "hello world" {
		t.Fatalf("status content = %q", nodeBytes(st))
	}
}

func TestSetProfilePictureNode_Self(t *testing.T) {
	jpeg := []byte{0xFF, 0xD8, 0xFF, 0x01}
	n := setProfilePictureNode("id2", "", jpeg)
	attr(t, n, "to", sWhatsAppNet)
	attr(t, n, "type", "set")
	attr(t, n, "xmlns", "w:profile:picture")
	attr(t, n, "id", "id2")
	if _, has := n.Attrs["target"]; has {
		t.Fatal("self update must omit target attr")
	}
	pic, ok := childByTag(n, "picture")
	if !ok {
		t.Fatal("missing <picture>")
	}
	attr(t, pic, "type", "image")
	if !bytes.Equal(nodeBytes(pic), jpeg) {
		t.Fatalf("picture bytes = %x", nodeBytes(pic))
	}
}

func TestSetProfilePictureNode_Target(t *testing.T) {
	n := setProfilePictureNode("id3", "555@s.whatsapp.net", []byte{1})
	attr(t, n, "target", "555@s.whatsapp.net")
}

func TestRemoveProfilePictureNode(t *testing.T) {
	n := removeProfilePictureNode("id4", "555@g.us")
	attr(t, n, "to", sWhatsAppNet)
	attr(t, n, "type", "set")
	attr(t, n, "xmlns", "w:profile:picture")
	attr(t, n, "target", "555@g.us")
	if children(n) != nil {
		t.Fatal("remove picture must have no children")
	}
}

func TestRemoveProfilePictureNode_Self(t *testing.T) {
	n := removeProfilePictureNode("id5", "")
	if _, has := n.Attrs["target"]; has {
		t.Fatal("self remove must omit target")
	}
}

func TestProfilePictureURLNode(t *testing.T) {
	prev := profilePictureURLNode("id6", "555@s.whatsapp.net", true)
	attr(t, prev, "target", "555@s.whatsapp.net")
	attr(t, prev, "to", sWhatsAppNet)
	attr(t, prev, "type", "get")
	attr(t, prev, "xmlns", "w:profile:picture")
	pic, ok := childByTag(prev, "picture")
	if !ok {
		t.Fatal("missing <picture>")
	}
	attr(t, pic, "type", "preview")
	attr(t, pic, "query", "url")

	full := profilePictureURLNode("id7", "555@s.whatsapp.net", false)
	fpic, _ := childByTag(full, "picture")
	attr(t, fpic, "type", "image")
}

func TestFetchStatusNode(t *testing.T) {
	n := fetchStatusNode("id8", "555@s.whatsapp.net")
	attr(t, n, "to", "555@s.whatsapp.net")
	attr(t, n, "type", "get")
	attr(t, n, "xmlns", "status")
	if _, ok := childByTag(n, "status"); !ok {
		t.Fatal("missing <status>")
	}
}

func TestPresenceNameNode(t *testing.T) {
	n := presenceNameNode("me@s.whatsapp.net", "Bruno")
	if n.Tag != "presence" {
		t.Fatalf("tag = %q", n.Tag)
	}
	attr(t, n, "type", "available")
	attr(t, n, "name", "Bruno")
	attr(t, n, "from", "me@s.whatsapp.net")
}

// --- parse tests ---

func TestParseProfilePictureURL(t *testing.T) {
	reply := wire.Node{
		Tag: "iq",
		Content: []wire.Node{
			{Tag: "picture", Attrs: map[string]string{"url": "https://pps.example/abc.jpg", "type": "preview"}},
		},
	}
	if got := parseProfilePictureURL(reply); got != "https://pps.example/abc.jpg" {
		t.Fatalf("url = %q", got)
	}
	// no picture node -> empty
	if got := parseProfilePictureURL(wire.Node{Tag: "iq"}); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestParseFetchStatus(t *testing.T) {
	reply := wire.Node{
		Tag: "iq",
		Content: []wire.Node{
			{Tag: "status", Content: []byte("Busy now")},
		},
	}
	if got := parseFetchStatus(reply); got != "Busy now" {
		t.Fatalf("status = %q", got)
	}
	if got := parseFetchStatus(wire.Node{Tag: "iq"}); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestProfilePicTarget(t *testing.T) {
	sess := &session{creds: &store.Creds{Me: "me@s.whatsapp.net"}}
	if got := profilePicTarget(sess, ""); got != "" {
		t.Fatalf("empty jid -> %q", got)
	}
	if got := profilePicTarget(sess, "me@s.whatsapp.net"); got != "" {
		t.Fatalf("self jid -> %q (want empty)", got)
	}
	if got := profilePicTarget(sess, "other@s.whatsapp.net"); got != "other@s.whatsapp.net" {
		t.Fatalf("other jid -> %q", got)
	}
}
