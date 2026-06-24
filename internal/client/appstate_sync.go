// Package client: appstate_sync.go implements app-state RESYNC — fetching and
// applying the server's app-state patches/snapshots for a set of collections.
// A successful resync unlocks contacts/chats/device-list state and, crucially,
// advances the per-collection version+LTHash the chatmod (ChatModify) layer
// chains its outgoing patches from. Both layers share the same per-Client
// appStateStore, so this is a single, unified app-state machine.
//
// It mirrors Baileys' resyncAppState (Socket/chats.js) and the decode helpers in
// Utils/chat-utils.js (decodePatches / decodeSyncdSnapshot / parseSyncdCollection).
//
// Outbound sync iq (one collection shown; ResyncAppState batches all collections
// in a single <sync>):
//
//	<iq to=@s.whatsapp.net xmlns=w:sync:app:state type=set id=...>
//	  <sync>
//	    <collection name=<col> version=<v> return_snapshot=<fresh>>
//	      <patch/>
//	    </collection>
//	  </sync>
//	</iq>
//
// Inbound result:
//
//	<iq type=result>
//	  <sync>
//	    <collection name=<col>>
//	      [<snapshot>(ExternalBlobReference bytes)</snapshot>]
//	      <patches>
//	        <patch>(SyncdPatch bytes)</patch> ...
//	      </patches>
//	    </collection>
//	  </sync>
//	</iq>
//
// A <snapshot> carries an ExternalBlobReference (directPath/mediaKey/...). The
// referenced blob is downloaded via the media transfer (media.AppState type) and
// decoded with decodeSnapshot into a SyncdSnapshot. The download is the only live
// step; decodeSnapshot itself is pure and unit-tested offline.
package client

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"

	"github.com/felipeleal/wa-go/internal/appstate"
	"github.com/felipeleal/wa-go/internal/media"
	waproto "github.com/felipeleal/wa-go/internal/waproto"
	"github.com/felipeleal/wa-go/internal/wire"
	"google.golang.org/protobuf/proto"
)

// ResyncAppState fetches the server's app-state for each named collection and
// applies the returned snapshot (when fresh) and patches, advancing the unified
// per-collection HashState/version and emitting an event per decoded mutation.
//
// fresh requests a full snapshot (return_snapshot=true); pass false to fetch only
// the patches newer than the version currently held for each collection. The
// app-state sync key is resolved automatically (manual ConfigureAppState or the
// store key persisted at pairing) — no manual wiring needed.
func (c *Client) ResyncAppState(ctx context.Context, collections []string, fresh bool) error {
	sess, ok := c.activeSession()
	if !ok {
		return fmt.Errorf("client: ResyncAppState requires a live session")
	}
	if len(collections) == 0 {
		return fmt.Errorf("client: ResyncAppState requires at least one collection")
	}
	rawKey, _, err := c.ensureKey()
	if err != nil {
		return err
	}
	resolve := c.keyResolver(rawKey)

	req := c.buildResyncIQ(collections, fresh)
	reply, err := c.sendIQ(ctx, sess, req)
	if err != nil {
		return err
	}
	return c.applyResyncReply(ctx, reply, resolve)
}

// keyResolver returns an appstate.KeyResolver that prefers the in-memory rawKey
// (matching the configured/loaded keyId) and falls back to the store for any
// other keyId a patch/snapshot references.
func (c *Client) keyResolver(rawKey []byte) appstate.KeyResolver {
	st := c.appState()
	st.mu.Lock()
	keyIDB64 := st.keyIDB64
	st.mu.Unlock()
	return func(idB64 string) ([]byte, bool) {
		if idB64 == keyIDB64 && len(rawKey) > 0 {
			return rawKey, true
		}
		if c.store == nil {
			return nil, false
		}
		raw, err := base64.StdEncoding.DecodeString(idB64)
		if err != nil {
			return nil, false
		}
		data, ok, lerr := c.store.LoadAppStateSyncKey(raw)
		if lerr != nil || !ok || len(data) == 0 {
			return nil, false
		}
		return data, true
	}
}

// buildResyncIQ assembles the w:sync:app:state get-patch iq for the collections.
// For each collection the requested version is the one currently held in the
// unified state (0 if unknown), so the server returns only newer patches.
func (c *Client) buildResyncIQ(collections []string, fresh bool) wire.Node {
	collNodes := make([]wire.Node, 0, len(collections))
	for _, col := range collections {
		ver := c.AppStateCollectionState(col).Version
		// return_snapshot is true when forcing a fresh sync OR when we have no
		// version yet (Baileys: shouldForceSnapshot || !state.version).
		returnSnapshot := "false"
		if fresh || ver == 0 {
			returnSnapshot = "true"
		}
		// Baileys' <collection> is a LEAF (name/version/return_snapshot attrs, no
		// children). A stray <patch/> child made the server reject/stall the
		// w:sync:app:state iq (resync timed out).
		collNodes = append(collNodes, wire.Node{
			Tag: "collection",
			Attrs: map[string]string{
				"name":            col,
				"version":         uintToStr(ver),
				"return_snapshot": returnSnapshot,
			},
		})
	}
	return wire.Node{
		Tag: "iq",
		Attrs: map[string]string{
			"to":    sWhatsAppNet,
			"xmlns": "w:sync:app:state",
			"type":  "set",
			"id":    c.nextIQID("wa-go-resync-"),
		},
		Content: []wire.Node{{Tag: "sync", Attrs: map[string]string{}, Content: collNodes}},
	}
}

// applyResyncReply walks the <sync><collection> children of a resync result and
// applies each collection's snapshot (downloaded + decoded) then its patches,
// committing the advanced HashState and emitting mutation events.
func (c *Client) applyResyncReply(ctx context.Context, reply wire.Node, resolve appstate.KeyResolver) error {
	sync, ok := childByTag(reply, "sync")
	if !ok {
		return fmt.Errorf("client: resync reply missing <sync>")
	}
	var firstErr error
	for _, coll := range childrenByTag(sync, "collection") {
		name := coll.Attrs["name"]
		if name == "" {
			continue
		}
		if err := c.applyCollection(ctx, name, coll, resolve); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// applyCollection applies one collection node: snapshot first (when present),
// then any patches, in order. State advances under the unified store lock.
func (c *Client) applyCollection(ctx context.Context, name string, coll wire.Node, resolve appstate.KeyResolver) error {
	state := c.AppStateCollectionState(name)

	if snapNode, ok := childByTag(coll, "snapshot"); ok {
		snapBytes := nodeBytes(snapNode)
		res, err := c.resolveSnapshot(ctx, name, snapBytes, resolve)
		if err != nil {
			return fmt.Errorf("client: resync snapshot %s: %w", name, err)
		}
		state = res.State
		c.commitCollectionState(name, state)
		c.emitMutations(name, res.Mutations)
	}

	if patchesNode, ok := childByTag(coll, "patches"); ok {
		for _, pNode := range childrenByTag(patchesNode, "patch") {
			var patch waproto.SyncdPatch
			if err := proto.Unmarshal(nodeBytes(pNode), &patch); err != nil {
				return fmt.Errorf("client: resync unmarshal patch %s: %w", name, err)
			}
			res, err := appstate.DecodePatch(name, &patch, state, resolve)
			if err != nil {
				return fmt.Errorf("client: resync decode patch %s: %w", name, err)
			}
			state = res.State
			c.commitCollectionState(name, state)
			c.emitMutations(name, res.Mutations)
		}
	}
	return nil
}

// resolveSnapshot decodes an inline SyncdSnapshot, or — when the <snapshot> node
// carries an ExternalBlobReference — downloads the external blob (media.AppState)
// and decodes that. The decode itself is the pure decodeSnapshot helper.
func (c *Client) resolveSnapshot(ctx context.Context, name string, snapBytes []byte, resolve appstate.KeyResolver) (*appstate.DecodeResult, error) {
	// A <snapshot> body is an ExternalBlobReference. If the bytes happen to
	// unmarshal as a SyncdSnapshot with records (an inline snapshot, used by
	// tests and some servers) decode them directly; otherwise treat them as the
	// external reference and download.
	if res, err := decodeSnapshot(name, snapBytes, resolve); err == nil {
		return res, nil
	}
	var ref waproto.ExternalBlobReference
	if err := proto.Unmarshal(snapBytes, &ref); err != nil {
		return nil, fmt.Errorf("decode external blob reference: %w", err)
	}
	blob, err := c.downloadExternalBlob(ctx, &ref)
	if err != nil {
		return nil, err
	}
	return decodeSnapshot(name, blob, resolve)
}

// decodeSnapshot is the pure decode of raw SyncdSnapshot bytes into a verified
// DecodeResult (new HashState + mutations). It is the testable seam: tests feed a
// synthetic snapshot built with appstate.EncodeSnapshot and assert the result.
func decodeSnapshot(name string, snapBytes []byte, resolve appstate.KeyResolver) (*appstate.DecodeResult, error) {
	var snap waproto.SyncdSnapshot
	if err := proto.Unmarshal(snapBytes, &snap); err != nil {
		return nil, fmt.Errorf("unmarshal SyncdSnapshot: %w", err)
	}
	if snap.GetKeyId() == nil || len(snap.GetRecords()) == 0 {
		return nil, fmt.Errorf("client: not a SyncdSnapshot")
	}
	return appstate.DecodeSnapshot(name, &snap, resolve)
}

// downloadExternalBlob fetches and decrypts the app-state external blob the
// snapshot references, using the media transfer with the AppState media type.
func (c *Client) downloadExternalBlob(ctx context.Context, ref *waproto.ExternalBlobReference) ([]byte, error) {
	if len(ref.GetMediaKey()) != 32 {
		return nil, fmt.Errorf("client: external blob missing mediaKey")
	}
	loc := ref.GetDirectPath()
	if loc == "" {
		return nil, fmt.Errorf("client: external blob missing directPath")
	}
	conn, err := c.mediaConn(ctx)
	if err != nil {
		return nil, err
	}
	return media.Download(ctx, http.DefaultClient, loc, ref.GetMediaKey(), media.AppState, conn.Hosts, conn.Auth)
}

// commitCollectionState advances the unified per-collection HashState (the same
// state ChatModify reads), so a later outgoing patch chains from the resynced
// version.
func (c *Client) commitCollectionState(name string, state *appstate.HashState) {
	st := c.appState()
	st.mu.Lock()
	st.states[name] = state
	st.mu.Unlock()
}

// emitMutations turns decoded app-state mutations into Client events: a
// contactAction becomes a ContactEvent; everything else becomes the generic
// AppStateMutationEvent.
func (c *Client) emitMutations(collection string, muts []appstate.Mutation) {
	for _, m := range muts {
		if ca := m.Action.GetContactAction(); ca != nil && len(m.Index) >= 2 {
			c.emit(ContactEvent{
				JID:       m.Index[1],
				FullName:  ca.GetFullName(),
				FirstName: ca.GetFirstName(),
			})
			continue
		}
		c.emit(AppStateMutationEvent{
			Collection: collection,
			Index:      m.Index,
			Action:     m.Action,
			Operation:  m.Operation,
		})
	}
}
