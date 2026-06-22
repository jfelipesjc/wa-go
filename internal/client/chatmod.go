package client

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/felipeleal/wa-go/internal/appstate"
	waproto "github.com/felipeleal/wa-go/internal/waproto"
	"github.com/felipeleal/wa-go/internal/wire"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

// --- app-state collection names (WAPatchName) ---
//
// Each chat action lives in a named collection; the server tracks an independent
// version + LTHash per collection. These names mirror Baileys'
// chatModificationToAppPatch `type` field.
const (
	collRegular            = "regular"
	collRegularHigh        = "regular_high"
	collRegularLow         = "regular_low"
	collCriticalBlock      = "critical_block"
	collCriticalUnblockLow = "critical_unblock_low"
)

// MsgKey identifies a single message for star/delete-for-me style actions.
type MsgKey struct {
	ID        string
	FromMe    bool
	RemoteJID string
}

// --- per-Client app-state store ---
//
// The full picture (versions + LTHash per collection) is owned by the server and
// fetched via the w:sync:app:state resync iq, which is a separate work item.
// Until that lands, ChatModify needs somewhere to keep the running HashState so
// successive patches chain (version N -> N+1). appStateStore is that minimal
// in-memory store, plus the myAppStateKeyId / raw appStateSyncKey the encoder
// needs.
//
// It is attached to a Client out-of-band (a package-level map keyed by *Client)
// because the Client struct itself is defined in another file we do not edit.
type appStateStore struct {
	mu       sync.Mutex
	keyIDB64 string
	rawKey   []byte
	states   map[string]*appstate.HashState
}

var (
	appStateStoresMu sync.Mutex
	appStateStores   = map[*Client]*appStateStore{}
)

func (c *Client) appState() *appStateStore {
	appStateStoresMu.Lock()
	defer appStateStoresMu.Unlock()
	st, ok := appStateStores[c]
	if !ok {
		st = &appStateStore{states: map[string]*appstate.HashState{}}
		appStateStores[c] = st
	}
	return st
}

// ConfigureAppState sets the app-state sync key (raw 32 bytes) and its base64
// keyId used to encrypt outgoing chat-modify patches. This is normally populated
// from the APP_STATE_SYNC_KEY_SHARE received during/after pairing. ChatModify
// returns an error until this is set.
func (c *Client) ConfigureAppState(keyIDB64 string, rawKey []byte) {
	st := c.appState()
	st.mu.Lock()
	defer st.mu.Unlock()
	st.keyIDB64 = keyIDB64
	st.rawKey = append([]byte(nil), rawKey...)
}

// SetAppStateCollectionState seeds the known version+hash for a collection. This
// is what the resync layer will call once it fetches snapshots from the server;
// exposed now so callers/tests can establish a starting point.
func (c *Client) SetAppStateCollectionState(collection string, state *appstate.HashState) {
	st := c.appState()
	st.mu.Lock()
	defer st.mu.Unlock()
	st.states[collection] = state
}

// --- public chat actions ---

// ArchiveChat archives (or un-archives) a chat.
func (c *Client) ArchiveChat(ctx context.Context, jid string, archive bool) error {
	return c.ChatModify(ctx, jid, ChatAction{
		collection: collRegularLow,
		apiVersion: 3,
		index:      []string{"archive", jid},
		value: &waproto.SyncActionValue{
			ArchiveChatAction: &waproto.SyncActionValue_ArchiveChatAction{Archived: proto.Bool(archive)},
		},
	})
}

// PinChat pins (or unpins) a chat.
func (c *Client) PinChat(ctx context.Context, jid string, pin bool) error {
	return c.ChatModify(ctx, jid, ChatAction{
		collection: collRegularLow,
		apiVersion: 5,
		index:      []string{"pin_v1", jid},
		value: &waproto.SyncActionValue{
			PinAction: &waproto.SyncActionValue_PinAction{Pinned: proto.Bool(pin)},
		},
	})
}

// MuteChat mutes a chat until now+duration. A zero duration unmutes it.
func (c *Client) MuteChat(ctx context.Context, jid string, duration time.Duration) error {
	muted := duration > 0
	ma := &waproto.SyncActionValue_MuteAction{Muted: proto.Bool(muted)}
	if muted {
		ma.MuteEndTimestamp = proto.Int64(time.Now().Add(duration).UnixMilli())
	}
	return c.ChatModify(ctx, jid, ChatAction{
		collection: collRegularHigh,
		apiVersion: 2,
		index:      []string{"mute", jid},
		value:      &waproto.SyncActionValue{MuteAction: ma},
	})
}

// MarkRead marks a chat as read (read=true) or unread (read=false).
//
// markChatAsReadAction (field 20) is absent from the generated waproto, so its
// sub-message is hand-encoded and attached as an unknown field. See withUnknownMessageField.
func (c *Client) MarkRead(ctx context.Context, jid string, read bool) error {
	// markChatAsReadAction { read = 1 (bool, field 1) }
	sub := protowire.AppendTag(nil, 1, protowire.VarintType)
	sub = protowire.AppendVarint(sub, boolToVarint(read))
	val := withUnknownMessageField(&waproto.SyncActionValue{}, 20, sub)
	return c.ChatModify(ctx, jid, ChatAction{
		collection: collRegularLow,
		apiVersion: 3,
		index:      []string{"markChatAsRead", jid},
		value:      val,
	})
}

// StarMessage stars (or unstars) a single message in a chat.
func (c *Client) StarMessage(ctx context.Context, jid string, key MsgKey, star bool) error {
	return c.ChatModify(ctx, jid, ChatAction{
		collection: collRegularLow,
		apiVersion: 2,
		index:      []string{"star", jid, key.ID, boolDigit(key.FromMe), "0"},
		value: &waproto.SyncActionValue{
			StarAction: &waproto.SyncActionValue_StarAction{Starred: proto.Bool(star)},
		},
	})
}

// DeleteChat deletes a chat for this account.
//
// deleteChatAction (field 22) is absent from the generated waproto; the
// (empty-but-present) sub-message is hand-encoded as an unknown field.
func (c *Client) DeleteChat(ctx context.Context, jid string) error {
	// deleteChatAction {} — present but empty (messageRange omitted).
	val := withUnknownMessageField(&waproto.SyncActionValue{}, 22, nil)
	return c.ChatModify(ctx, jid, ChatAction{
		collection: collRegularHigh,
		apiVersion: 6,
		index:      []string{"deleteChat", jid, "1"},
		value:      val,
	})
}

// ClearChat clears a chat's messages for this account (keeping the chat).
//
// clearChatAction (field 21) is absent from the generated waproto; hand-encoded.
func (c *Client) ClearChat(ctx context.Context, jid string) error {
	// clearChatAction {} — present but empty (messageRange omitted).
	val := withUnknownMessageField(&waproto.SyncActionValue{}, 21, nil)
	return c.ChatModify(ctx, jid, ChatAction{
		collection: collRegularHigh,
		apiVersion: 6,
		index:      []string{"clearChat", jid, "1", "0"},
		value:      val,
	})
}

// ChatAction is a fully-resolved app-state mutation: the target collection, the
// SyncActionData.version (apiVersion), the JSON index array and the
// SyncActionValue. The Operation is SET unless set otherwise.
type ChatAction struct {
	collection string
	apiVersion int32
	index      []string
	value      *waproto.SyncActionValue
	operation  waproto.SyncdMutation_SyncdOperation
}

// ChatModify encodes a single SET (or REMOVE) app-state patch for the action and
// sends it via the w:sync:app:state iq. It chains the per-collection HashState so
// repeated calls advance the version. The current implementation always builds a
// single-mutation patch (matching Baileys' chatModify).
func (c *Client) ChatModify(ctx context.Context, jid string, action ChatAction) error {
	sess, ok := c.activeSession()
	if !ok {
		return errors.New("client: not logged in (no active session)")
	}
	st := c.appState()
	st.mu.Lock()
	if len(st.rawKey) == 0 || st.keyIDB64 == "" {
		st.mu.Unlock()
		return errors.New("client: app state sync key not configured (call ConfigureAppState)")
	}
	keyIDB64 := st.keyIDB64
	mk := appstate.DeriveMutationKeys(st.rawKey)
	prev := st.states[action.collection]
	if prev == nil {
		prev = appstate.NewHashState()
	}
	prevVersion := prev.Version
	st.mu.Unlock()

	patchBytes, newState, err := appstate.EncodePatch(
		action.collection, prevVersion, keyIDB64, mk, prev,
		[]appstate.MutationToEncode{{
			Operation:  action.operation,
			Index:      action.index,
			Action:     action.value,
			APIVersion: action.apiVersion,
		}},
		nil,
	)
	if err != nil {
		return fmt.Errorf("client: encode app patch: %w", err)
	}

	req := buildAppStatePatchIQ(c.nextIQID("wa-go-appstate-"), action.collection, prevVersion, patchBytes)
	if _, err := c.sendIQ(ctx, sess, req); err != nil {
		return err
	}

	// Commit the advanced state only after the server accepted the patch.
	st.mu.Lock()
	st.states[action.collection] = newState
	st.mu.Unlock()
	return nil
}

// buildAppStatePatchIQ assembles the set iq carrying a single encoded patch:
//
//	<iq to=@s.whatsapp.net type=set xmlns=w:sync:app:state id=...>
//	  <sync>
//	    <collection name=<col> version=<prevVersion> return_snapshot=false>
//	      <patch>{patchBytes}</patch>
//	    </collection>
//	  </sync>
//	</iq>
//
// version is the collection's current (pre-increment) version, matching Baileys'
// `(state.version - 1)` attribute.
func buildAppStatePatchIQ(id, collection string, version uint64, patchBytes []byte) wire.Node {
	return wire.Node{
		Tag: "iq",
		Attrs: map[string]string{
			"to":    sWhatsAppNet,
			"type":  "set",
			"xmlns": "w:sync:app:state",
			"id":    id,
		},
		Content: []wire.Node{{
			Tag: "sync",
			Content: []wire.Node{{
				Tag: "collection",
				Attrs: map[string]string{
					"name":            collection,
					"version":         uintToStr(version),
					"return_snapshot": "false",
				},
				Content: []wire.Node{{
					Tag:     "patch",
					Content: patchBytes,
				}},
			}},
		}},
	}
}

// --- helpers ---

func boolToVarint(b bool) uint64 {
	if b {
		return 1
	}
	return 0
}

func boolDigit(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func uintToStr(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// withUnknownMessageField returns sav with a length-delimited message field of
// the given number appended to its unknown fields. This is how chat actions that
// are not (yet) present in the generated waproto (markChatAsRead/clear/delete)
// are carried inside SyncActionValue: proto.Marshal preserves unknown fields, so
// the wire bytes are byte-identical to what a generated field would produce.
func withUnknownMessageField(sav *waproto.SyncActionValue, fieldNum protowire.Number, msgBytes []byte) *waproto.SyncActionValue {
	enc := protowire.AppendTag(nil, fieldNum, protowire.BytesType)
	enc = protowire.AppendBytes(enc, msgBytes)
	ref := sav.ProtoReflect()
	ref.SetUnknown(append(ref.GetUnknown(), enc...))
	return sav
}
