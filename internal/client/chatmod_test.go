package client

import (
	"context"
	"encoding/base64"
	"testing"
	"time"

	"github.com/jfelipesjc/wa-go/internal/appstate"
	waproto "github.com/jfelipesjc/wa-go/internal/waproto"
	"github.com/jfelipesjc/wa-go/internal/wire"
	"google.golang.org/protobuf/proto"
)

const testChatJID = "5511999999999@s.whatsapp.net"

func TestBuildAppStatePatchIQ(t *testing.T) {
	n := buildAppStatePatchIQ("ID1", collRegularLow, 4, []byte{0xaa, 0xbb})
	if n.Tag != "iq" {
		t.Fatalf("tag = %q", n.Tag)
	}
	if n.Attrs["xmlns"] != "w:sync:app:state" || n.Attrs["type"] != "set" ||
		n.Attrs["to"] != sWhatsAppNet || n.Attrs["id"] != "ID1" {
		t.Fatalf("iq attrs wrong: %+v", n.Attrs)
	}
	sync, ok := childByTag(n, "sync")
	if !ok {
		t.Fatal("missing <sync>")
	}
	coll, ok := childByTag(sync, "collection")
	if !ok {
		t.Fatal("missing <collection>")
	}
	if coll.Attrs["name"] != collRegularLow {
		t.Fatalf("collection name = %q", coll.Attrs["name"])
	}
	if coll.Attrs["version"] != "4" {
		t.Fatalf("version attr = %q, want 4 (pre-increment)", coll.Attrs["version"])
	}
	if coll.Attrs["return_snapshot"] != "false" {
		t.Fatalf("return_snapshot = %q", coll.Attrs["return_snapshot"])
	}
	patch, ok := childByTag(coll, "patch")
	if !ok {
		t.Fatal("missing <patch>")
	}
	b, ok := patch.Content.([]byte)
	if !ok || len(b) != 2 || b[0] != 0xaa {
		t.Fatalf("patch content wrong: %v", patch.Content)
	}
}

// testClientWithAppState returns a logged-in Client (fake session capturing the
// outgoing node) with the app-state key configured, plus a function that runs an
// action and returns the captured iq.
func testClientWithAppState(t *testing.T) (*Client, func(run func() error) wire.Node) {
	t.Helper()
	c := NewWithDialer(nil, nil)
	// rawKey: 32 deterministic bytes.
	rawKey := make([]byte, 32)
	for i := range rawKey {
		rawKey[i] = byte(i + 1)
	}
	keyID := base64.StdEncoding.EncodeToString([]byte("appstatekeyid01"))
	c.ConfigureAppState(keyID, rawKey)

	capturedCh := make(chan wire.Node, 1)
	sess := &session{send: func(n wire.Node) error {
		capturedCh <- n
		return nil
	}}
	c.mu.Lock()
	c.active = sess
	c.mu.Unlock()

	exec := func(run func() error) wire.Node {
		errCh := make(chan error, 1)
		go func() { errCh <- run() }()
		// ChatModify resyncs the collection before mutating (Baileys' appPatch),
		// so it sends TWO iqs: the resync first, then the SET patch. Ack each one
		// with a result reply (the resync result lacks <sync>, so ResyncAppState
		// returns a harmless ignored error) and return the SET iq (the one with a
		// <patch> child) for the caller's assertions.
		var setIQ wire.Node
		for {
			select {
			case captured := <-capturedCh:
				c.deliverIQ(wire.Node{Tag: "iq", Attrs: map[string]string{
					"id": captured.Attrs["id"], "type": "result",
				}})
				if sync, ok := childByTag(captured, "sync"); ok {
					if coll, ok := childByTag(sync, "collection"); ok {
						if _, ok := childByTag(coll, "patch"); ok {
							setIQ = captured
						}
					}
				}
			case err := <-errCh:
				if err != nil {
					t.Fatalf("action error: %v", err)
				}
				return setIQ
			case <-time.After(3 * time.Second):
				t.Fatal("timed out waiting for iq to be sent")
			}
		}
	}
	return c, exec
}

func collectionOf(t *testing.T, n wire.Node) string {
	t.Helper()
	sync, _ := childByTag(n, "sync")
	coll, ok := childByTag(sync, "collection")
	if !ok {
		t.Fatal("no collection")
	}
	return coll.Attrs["name"]
}

func TestChatActionsCollections(t *testing.T) {
	cases := []struct {
		name     string
		run      func(c *Client) error
		wantColl string
	}{
		{"archive", func(c *Client) error { return c.ArchiveChat(context.Background(), testChatJID, true) }, collRegularLow},
		{"pin", func(c *Client) error { return c.PinChat(context.Background(), testChatJID, true) }, collRegularLow},
		{"mute", func(c *Client) error { return c.MuteChat(context.Background(), testChatJID, time.Hour) }, collRegularHigh},
		{"markRead", func(c *Client) error { return c.MarkRead(context.Background(), testChatJID, true) }, collRegularLow},
		{"star", func(c *Client) error {
			return c.StarMessage(context.Background(), testChatJID, MsgKey{ID: "M1", FromMe: true}, true)
		}, collRegularLow},
		{"delete", func(c *Client) error { return c.DeleteChat(context.Background(), testChatJID) }, collRegularHigh},
		{"clear", func(c *Client) error { return c.ClearChat(context.Background(), testChatJID) }, collRegularHigh},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, exec := testClientWithAppState(t)
			n := exec(func() error { return tc.run(c) })
			if got := collectionOf(t, n); got != tc.wantColl {
				t.Fatalf("%s collection = %q, want %q", tc.name, got, tc.wantColl)
			}
			if n.Attrs["xmlns"] != "w:sync:app:state" {
				t.Fatalf("xmlns = %q", n.Attrs["xmlns"])
			}
		})
	}
}

// TestChatModifyRoundTripDecode verifies the emitted patch decodes back to the
// expected mutation (archive), proving the client wires EncodePatch correctly.
func TestChatModifyRoundTripDecode(t *testing.T) {
	c, exec := testClientWithAppState(t)
	n := exec(func() error { return c.ArchiveChat(context.Background(), testChatJID, true) })

	sync, _ := childByTag(n, "sync")
	coll, _ := childByTag(sync, "collection")
	patchNode, _ := childByTag(coll, "patch")
	patchBytes := patchNode.Content.([]byte)

	var patch waproto.SyncdPatch
	if err := proto.Unmarshal(patchBytes, &patch); err != nil {
		t.Fatalf("unmarshal patch: %v", err)
	}
	// Uploaded patches omit the version (the server assigns it from the
	// <collection version> attr); re-add it (fresh state -> 1) before decoding.
	patch.Version = &waproto.SyncdVersion{Version: proto.Uint64(1)}

	rawKey := make([]byte, 32)
	for i := range rawKey {
		rawKey[i] = byte(i + 1)
	}
	keyID := base64.StdEncoding.EncodeToString([]byte("appstatekeyid01"))
	resolve := func(id string) ([]byte, bool) {
		if id == keyID {
			return rawKey, true
		}
		return nil, false
	}
	res, err := appstate.DecodePatch(collRegularLow, &patch, appstate.NewHashState(), resolve)
	if err != nil {
		t.Fatalf("DecodePatch: %v", err)
	}
	if len(res.Mutations) != 1 {
		t.Fatalf("mutations = %d", len(res.Mutations))
	}
	m := res.Mutations[0]
	if len(m.Index) != 2 || m.Index[0] != "archive" || m.Index[1] != testChatJID {
		t.Fatalf("index = %v", m.Index)
	}
	if ac := m.Action.GetArchiveChatAction(); ac == nil || !ac.GetArchived() {
		t.Fatalf("archiveChatAction wrong: %v", m.Action)
	}
}

// TestChatModifyNotConfigured ensures ChatModify fails clearly without a key.
func TestChatModifyNotConfigured(t *testing.T) {
	c := NewWithDialer(nil, nil)
	sess := &session{send: func(wire.Node) error { return nil }}
	c.mu.Lock()
	c.active = sess
	c.mu.Unlock()
	err := c.ArchiveChat(context.Background(), testChatJID, true)
	if err == nil {
		t.Fatal("expected error when app state key not configured")
	}
}
