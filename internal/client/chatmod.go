package client

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/felipeleal/wa-go/internal/appstate"
	waproto "github.com/felipeleal/wa-go/internal/waproto"
	"github.com/felipeleal/wa-go/internal/wire"
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
//
// This is the SINGLE source of truth for the app-state machinery: both ChatModify
// (outgoing patches) and ResyncAppState (incoming snapshots/patches) read and
// write the same keyIDB64 / rawKey / states here, so a resync advances the very
// version+hash a later ChatModify chains from.
type appStateStore struct {
	mu       sync.Mutex
	keyIDB64 string
	rawKey   []byte
	states   map[string]*appstate.HashState

	// lastKeyID is the binary keyId of the most recently received
	// APP_STATE_SYNC_KEY_SHARE. When the key was not set manually via
	// ConfigureAppState, ensureKey loads its material from the store on demand,
	// so the chatmod/resync layers work automatically once pairing delivers a key.
	lastKeyID []byte
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

// AppStateCollectionState returns the current HashState for a collection (a
// clone so callers cannot mutate the live state), or the zero state if none is
// known yet. Exposed so the resync layer and tests can read the unified version.
func (c *Client) AppStateCollectionState(collection string) *appstate.HashState {
	st := c.appState()
	st.mu.Lock()
	defer st.mu.Unlock()
	s := st.states[collection]
	if s == nil {
		return appstate.NewHashState()
	}
	return s.Clone()
}

// noteAppStateKeyID records the keyId of a freshly received
// APP_STATE_SYNC_KEY_SHARE so ensureKey can later load it from the store without
// a manual ConfigureAppState. The raw material itself lives in the store.
func (c *Client) noteAppStateKeyID(keyID []byte) {
	st := c.appState()
	st.mu.Lock()
	defer st.mu.Unlock()
	st.lastKeyID = append([]byte(nil), keyID...)
}

// ensureKey returns the app-state sync key (raw 32 bytes) and its base64 keyId,
// loading from the store when it was not configured manually. It is the bridge
// that lets ChatModify/ResyncAppState work automatically once pairing has
// persisted an APP_STATE_SYNC_KEY_SHARE: the key need not be wired in by hand.
//
// The store lookup is performed without holding st.mu (store access may block);
// the loaded material is cached back under the lock on success.
func (c *Client) ensureKey() (rawKey []byte, keyIDB64 string, err error) {
	st := c.appState()
	st.mu.Lock()
	if len(st.rawKey) > 0 && st.keyIDB64 != "" {
		rawKey = append([]byte(nil), st.rawKey...)
		keyIDB64 = st.keyIDB64
		st.mu.Unlock()
		return rawKey, keyIDB64, nil
	}
	keyID := append([]byte(nil), st.lastKeyID...)
	st.mu.Unlock()

	if c.store == nil {
		return nil, "", errors.New("client: app state sync key not configured (call ConfigureAppState or pair to receive APP_STATE_SYNC_KEY_SHARE)")
	}

	var data []byte
	var ok bool
	if len(keyID) > 0 {
		var lerr error
		data, ok, lerr = c.store.LoadAppStateSyncKey(keyID)
		if lerr != nil {
			return nil, "", fmt.Errorf("client: load app state sync key: %w", lerr)
		}
	}
	// Cross-session recovery: the APP_STATE_SYNC_KEY_SHARE arrives once (after
	// pairing) and only sets lastKeyID in memory, lost on relogin. When we don't
	// have the keyId in memory (or it didn't resolve), pull the latest persisted
	// app-state key directly from the store so resync/archive/pin/mute keep
	// working across reconnects.
	if !ok || len(data) == 0 {
		if latest, has := c.store.(interface {
			LatestAppStateSyncKey() (keyID, keyData []byte, ok bool, err error)
		}); has {
			lid, ldata, lok, lerr := latest.LatestAppStateSyncKey()
			if lerr != nil {
				return nil, "", fmt.Errorf("client: latest app state sync key: %w", lerr)
			}
			if lok && len(ldata) > 0 {
				keyID, data, ok = lid, ldata, true
			}
		}
	}
	if len(keyID) == 0 {
		return nil, "", errors.New("client: app state sync key not configured (call ConfigureAppState or pair to receive APP_STATE_SYNC_KEY_SHARE)")
	}
	if !ok || len(data) == 0 {
		return nil, "", errors.New("client: app state sync key not found in store")
	}
	keyIDB64 = base64.StdEncoding.EncodeToString(keyID)
	st.mu.Lock()
	st.rawKey = append([]byte(nil), data...)
	st.keyIDB64 = keyIDB64
	st.mu.Unlock()
	return append([]byte(nil), data...), keyIDB64, nil
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
func (c *Client) MarkRead(ctx context.Context, jid string, read bool) error {
	return c.ChatModify(ctx, jid, ChatAction{
		collection: collRegularLow,
		apiVersion: 3,
		index:      []string{"markChatAsRead", jid},
		value: &waproto.SyncActionValue{
			MarkChatAsReadAction: &waproto.SyncActionValue_MarkChatAsReadAction{
				Read: proto.Bool(read),
			},
		},
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
func (c *Client) DeleteChat(ctx context.Context, jid string) error {
	// deleteChatAction {} — present but empty (messageRange omitted).
	return c.ChatModify(ctx, jid, ChatAction{
		collection: collRegularHigh,
		apiVersion: 6,
		index:      []string{"deleteChat", jid, "1"},
		value: &waproto.SyncActionValue{
			DeleteChatAction: &waproto.SyncActionValue_DeleteChatAction{},
		},
	})
}

// ClearChat clears a chat's messages for this account (keeping the chat).
func (c *Client) ClearChat(ctx context.Context, jid string) error {
	// clearChatAction {} — present but empty (messageRange omitted).
	return c.ChatModify(ctx, jid, ChatAction{
		collection: collRegularHigh,
		apiVersion: 6,
		index:      []string{"clearChat", jid, "1", "0"},
		value: &waproto.SyncActionValue{
			ClearChatAction: &waproto.SyncActionValue_ClearChatAction{},
		},
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
	// Resync the collection first so we patch on top of the server's CURRENT
	// version (Baileys' appPatch resyncs before every mutation). Without it our
	// prevVersion is stale (0) and the server rejects the upload ("iq returned
	// error"). Best-effort: a resync hiccup shouldn't hard-block the mutation.
	_ = c.ResyncAppState(ctx, []string{action.collection}, false)

	rawKey, keyIDB64, err := c.ensureKey()
	if err != nil {
		return err
	}
	st := c.appState()
	st.mu.Lock()
	mk := appstate.DeriveMutationKeys(rawKey)
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
