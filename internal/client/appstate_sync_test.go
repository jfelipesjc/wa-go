package client

import (
	"context"
	"encoding/base64"
	"path/filepath"
	"testing"
	"time"

	"github.com/felipeleal/wa-go/internal/appstate"
	"github.com/felipeleal/wa-go/internal/store"
	waproto "github.com/felipeleal/wa-go/internal/waproto"
	"github.com/felipeleal/wa-go/internal/wire"
	"google.golang.org/protobuf/proto"
)

// fixedReader yields a deterministic byte stream so encoded patches/snapshots
// are reproducible (mirror of the appstate package test helper).
type fixedReader struct {
	b   []byte
	pos int
}

func (r *fixedReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.b[r.pos%len(r.b)]
		r.pos++
	}
	return len(p), nil
}

func newFixedReader() *fixedReader {
	return &fixedReader{b: []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}}
}

func testRawKey() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i + 1)
	}
	return k
}

// TestResyncAppStateAppliesPatch drives ResyncAppState against a scripted session
// that replies with a single patch (built via appstate.EncodePatch) for the
// "regular" collection. It asserts the unified HashState/version advances and the
// contact mutation is surfaced as a ContactEvent.
func TestResyncAppStateAppliesPatch(t *testing.T) {
	rawKey := testRawKey()
	keyIDB64 := base64.StdEncoding.EncodeToString([]byte("appstatekeyid01"))
	mk := appstate.DeriveMutationKeys(rawKey)

	const coll = "regular"
	patchBytes, _, err := appstate.EncodePatch(coll, 0, keyIDB64, mk, appstate.NewHashState(),
		[]appstate.MutationToEncode{{
			Operation:  waproto.SyncdMutation_SET,
			Index:      []string{"contact", "5511999999999@s.whatsapp.net"},
			APIVersion: 6,
			Action: &waproto.SyncActionValue{
				ContactAction: &waproto.SyncActionValue_ContactAction{
					FullName:  proto.String("Bob Example"),
					FirstName: proto.String("Bob"),
				},
			},
		}}, newFixedReader())
	if err != nil {
		t.Fatalf("EncodePatch: %v", err)
	}

	c := NewWithDialer(nil, nil)
	c.ConfigureAppState(keyIDB64, rawKey)

	// Capture the outgoing resync iq, then reply with the patch.
	capCh := make(chan wire.Node, 1)
	sess := &session{send: func(n wire.Node) error { capCh <- n; return nil }}
	c.mu.Lock()
	c.active = sess
	c.mu.Unlock()

	// Drain events.
	contactCh := make(chan ContactEvent, 1)
	go func() {
		for e := range c.events {
			if ce, ok := e.(ContactEvent); ok {
				contactCh <- ce
			}
		}
	}()

	errCh := make(chan error, 1)
	go func() { errCh <- c.ResyncAppState(context.Background(), []string{coll}, true) }()

	var req wire.Node
	select {
	case req = <-capCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for resync iq")
	}

	// Validate the outgoing iq shape.
	if req.Attrs["xmlns"] != "w:sync:app:state" || req.Attrs["type"] != "set" {
		t.Fatalf("resync iq attrs wrong: %+v", req.Attrs)
	}
	sync, ok := childByTag(req, "sync")
	if !ok {
		t.Fatal("missing <sync>")
	}
	cn, ok := childByTag(sync, "collection")
	if !ok || cn.Attrs["name"] != coll {
		t.Fatalf("collection node wrong: %+v", cn)
	}
	if cn.Attrs["return_snapshot"] != "true" {
		t.Fatalf("return_snapshot = %q, want true", cn.Attrs["return_snapshot"])
	}
	// The <collection> is a leaf (no <patch> child) — Baileys' resync iq format.
	if _, ok := childByTag(cn, "patch"); ok {
		t.Fatal("collection should not carry a <patch> child (server stalls)")
	}

	// Reply with the patch wrapped in <iq result><sync><collection><patches>.
	reply := wire.Node{
		Tag:   "iq",
		Attrs: map[string]string{"id": req.Attrs["id"], "type": "result"},
		Content: []wire.Node{{
			Tag: "sync",
			Content: []wire.Node{{
				Tag:   "collection",
				Attrs: map[string]string{"name": coll},
				Content: []wire.Node{{
					Tag: "patches",
					Content: []wire.Node{{
						Tag:     "patch",
						Content: patchBytes,
					}},
				}},
			}},
		}},
	}
	c.deliverIQ(reply)

	if err := <-errCh; err != nil {
		t.Fatalf("ResyncAppState: %v", err)
	}

	// Unified state advanced to version 1.
	st := c.AppStateCollectionState(coll)
	if st.Version != 1 {
		t.Fatalf("collection version = %d, want 1", st.Version)
	}

	// Contact mutation emitted.
	select {
	case ce := <-contactCh:
		if ce.FullName != "Bob Example" || ce.JID != "5511999999999@s.whatsapp.net" {
			t.Fatalf("ContactEvent wrong: %+v", ce)
		}
	case <-time.After(time.Second):
		t.Fatal("no ContactEvent emitted")
	}
}

// TestChatModifyUsesResyncedVersion verifies the chatmod layer chains from the
// version a resync established: after seeding the unified state at version 5, the
// next ChatModify emits a patch carrying version 6 and an iq attr of 5.
func TestChatModifyUsesResyncedVersion(t *testing.T) {
	rawKey := testRawKey()
	keyIDB64 := base64.StdEncoding.EncodeToString([]byte("appstatekeyid01"))
	mk := appstate.DeriveMutationKeys(rawKey)

	c := NewWithDialer(nil, nil)
	c.ConfigureAppState(keyIDB64, rawKey)

	// Simulate a resync that left collRegularLow at version 5 with a real LTHash.
	_, state, err := appstate.EncodePatch(collRegularLow, 4, keyIDB64, mk, appstate.NewHashState(),
		[]appstate.MutationToEncode{{
			Operation:  waproto.SyncdMutation_SET,
			Index:      []string{"pin_v1", "x@s.whatsapp.net"},
			APIVersion: 5,
			Action:     &waproto.SyncActionValue{PinAction: &waproto.SyncActionValue_PinAction{Pinned: proto.Bool(true)}},
		}}, newFixedReader())
	if err != nil {
		t.Fatalf("seed EncodePatch: %v", err)
	}
	if state.Version != 5 {
		t.Fatalf("seed version = %d, want 5", state.Version)
	}
	c.SetAppStateCollectionState(collRegularLow, state)

	capCh := make(chan wire.Node, 1)
	sess := &session{send: func(n wire.Node) error { capCh <- n; return nil }}
	c.mu.Lock()
	c.active = sess
	c.mu.Unlock()

	// ChatModify resyncs before mutating, so it sends two iqs (resync, then the
	// SET patch). Ack every iq: the resync gets an empty result (no <sync> -> a
	// harmless ignored error, version stays at the seeded 5), the SET succeeds.
	go func() {
		for req := range capCh {
			c.deliverIQ(wire.Node{Tag: "iq", Attrs: map[string]string{"id": req.Attrs["id"], "type": "result"}})
		}
	}()

	if err := c.ArchiveChat(context.Background(), testChatJID, true); err != nil {
		t.Fatalf("ArchiveChat: %v", err)
	}

	// After commit the unified version is 6.
	if v := c.AppStateCollectionState(collRegularLow).Version; v != 6 {
		t.Fatalf("post-modify version = %d, want 6", v)
	}
}

// TestChatModifyLoadsKeyFromStore verifies the bridge: with no manual
// ConfigureAppState, a key persisted in the store (via the share handler) is
// picked up automatically so ChatModify works.
func TestChatModifyLoadsKeyFromStore(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer st.Close()

	keyID := []byte("appstatekeyid01")
	rawKey := testRawKey()
	if err := st.StoreAppStateSyncKey(keyID, rawKey); err != nil {
		t.Fatalf("StoreAppStateSyncKey: %v", err)
	}

	c := New(st)
	// Bridge: simulate the share handler noting the keyId (no ConfigureAppState).
	c.noteAppStateKeyID(keyID)

	capCh := make(chan wire.Node, 1)
	sess := &session{send: func(n wire.Node) error { capCh <- n; return nil }}
	c.mu.Lock()
	c.active = sess
	c.mu.Unlock()

	// ChatModify resyncs before mutating -> two iqs (resync + SET); ack both.
	go func() {
		for req := range capCh {
			c.deliverIQ(wire.Node{Tag: "iq", Attrs: map[string]string{"id": req.Attrs["id"], "type": "result"}})
		}
	}()

	if err := c.PinChat(context.Background(), testChatJID, true); err != nil {
		t.Fatalf("PinChat with store-loaded key: %v", err)
	}
}
