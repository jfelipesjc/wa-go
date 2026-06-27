package client

import (
	"sort"
	"sync"

	"github.com/jfelipesjc/wa-go/internal/waproto"
)

// Chat is the materialized state of a single conversation, distilled from a
// history-sync Conversation and kept up to date by later message and app-state
// events. JID is the conversation id; Name is the best-known display name;
// UnreadCount/ConversationTimestamp mirror the history-sync fields. Archived,
// Pinned and Muted are toggled by app-state mutations. LastMessageTime tracks
// the timestamp of the most recent message materialized for the chat.
type Chat struct {
	JID                   string
	Name                  string
	UnreadCount           int
	ConversationTimestamp int64
	Archived              bool
	Pinned                bool
	Muted                 bool
	LastMessageTime       int64
}

// Contact is the materialized identity of a peer: JID plus the various
// name surfaces WhatsApp exposes (the contact-book Name, the Notify name the
// server attaches to stanzas, and the self-set PushName from history sync).
type Contact struct {
	JID      string
	Name     string
	Notify   string
	PushName string
}

// StoredMessage is a single message materialized into the store. Key is the
// message id, FromJID the chat/sender JID, Timestamp the unix seconds, Text the
// decoded body (when textual), Type a coarse classification, and Raw the
// originating WebMessageInfo (nil for messages materialized from a live
// MessageEvent that carried no WebMessageInfo).
type StoredMessage struct {
	Key       string
	FromJID   string
	Timestamp int64
	Text      string
	Type      string
	Raw       *waproto.WebMessageInfo
	// Media is the media descriptor for live messages (which carry no Raw
	// WebMessageInfo). It holds the keys/URL needed to download+decrypt, so
	// DownloadMedia can fetch the payload on demand even without Raw.
	Media *MediaInfo
}

// ChatStore is a thread-safe, in-memory materialization of the chat list,
// contacts and messages reconstructed from history-sync chunks and kept current
// by the live event stream. It is consultable via Chats/Chat/Contact/
// ChatMessages. The zero value is not usable; build one with NewChatStore.
type ChatStore struct {
	mu       sync.RWMutex
	chats    map[string]*Chat
	contacts map[string]*Contact
	messages map[string][]StoredMessage // keyed by chat JID, append order
}

// NewChatStore returns an empty, ready-to-use ChatStore.
func NewChatStore() *ChatStore {
	return &ChatStore{
		chats:    make(map[string]*Chat),
		contacts: make(map[string]*Contact),
		messages: make(map[string][]StoredMessage),
	}
}

// ApplyHistorySync materializes a decoded HistorySync: each Conversation
// creates/updates a Chat and indexes its messages, and each Pushname updates the
// corresponding Contact. It is safe to call repeatedly; later chunks merge into
// the existing state.
func (cs *ChatStore) ApplyHistorySync(hs *waproto.HistorySync) {
	if hs == nil {
		return
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()

	for _, conv := range hs.GetConversations() {
		if conv == nil {
			continue
		}
		jid := conv.GetId()
		if jid == "" {
			continue
		}
		ch := cs.chats[jid]
		if ch == nil {
			ch = &Chat{JID: jid}
			cs.chats[jid] = ch
		}
		if n := conv.GetName(); n != "" {
			ch.Name = n
		}
		ch.UnreadCount = int(conv.GetUnreadCount())
		if ts := conv.GetConversationTimestamp(); ts != 0 {
			ch.ConversationTimestamp = int64(ts)
		}
		if lt := conv.GetLastMsgTimestamp(); lt != 0 {
			if int64(lt) > ch.LastMessageTime {
				ch.LastMessageTime = int64(lt)
			}
		}
		ch.Archived = conv.GetArchived()
		ch.Pinned = conv.GetPinned() != 0
		ch.MuteFrom(conv.GetMuteEndTime())

		for _, hm := range conv.GetMessages() {
			if hm == nil {
				continue
			}
			wmi := hm.GetMessage()
			if wmi == nil {
				continue
			}
			cs.indexMessageLocked(jid, storedFromWebMessageInfo(jid, wmi))
		}
	}

	for _, pn := range hs.GetPushnames() {
		if pn == nil {
			continue
		}
		jid := pn.GetId()
		if jid == "" {
			continue
		}
		cs.contactLocked(jid).PushName = pn.GetPushname()
	}
}

// MuteFrom sets the Muted flag from a mute-end-timestamp (non-zero means muted).
func (ch *Chat) MuteFrom(muteEndTime uint64) {
	ch.Muted = muteEndTime != 0
}

// ApplyContact creates/updates the contact-book Name for a JID.
func (cs *ChatStore) ApplyContact(jid, name string) {
	if jid == "" {
		return
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	c := cs.contactLocked(jid)
	if name != "" {
		c.Name = name
	}
}

// ApplyAppStateMutation folds an app-state mutation into the matching Chat's
// flags. The WhatsApp index convention is [actionType, jid, ...]; the typed
// action carried on the SyncActionValue selects which flag to update. SET turns
// the relevant flag on (per the action's own value); a REMOVE archive/pin clears
// it. Unknown collections/actions are ignored.
func (cs *ChatStore) ApplyAppStateMutation(index []string, action *waproto.SyncActionValue, op waproto.SyncdMutation_SyncdOperation) {
	if action == nil || len(index) < 2 {
		return
	}
	jid := index[1]
	if jid == "" {
		return
	}
	removed := op == waproto.SyncdMutation_REMOVE

	cs.mu.Lock()
	defer cs.mu.Unlock()
	ch := cs.chatLocked(jid)

	switch {
	case action.GetArchiveChatAction() != nil:
		if removed {
			ch.Archived = false
		} else {
			ch.Archived = action.GetArchiveChatAction().GetArchived()
		}
	case action.GetPinAction() != nil:
		if removed {
			ch.Pinned = false
		} else {
			ch.Pinned = action.GetPinAction().GetPinned()
		}
	case action.GetMuteAction() != nil:
		if removed {
			ch.Muted = false
		} else {
			ch.Muted = action.GetMuteAction().GetMuted()
		}
	}
}

// Consume folds a single Client event into the store. It is meant to be driven
// straight off the event channel:
//
//	cs := NewChatStore()
//	for ev := range c.Events() {
//		cs.Consume(ev)
//	}
//
// HistorySyncEvent materializes chats/contacts/messages; MessageEvent appends a
// live message and bumps the chat; ContactEvent updates the contact name;
// AppStateMutationEvent toggles archive/pin/mute; ReceiptEvent clears unread on
// a read receipt. Other events are ignored.
func (cs *ChatStore) Consume(ev Event) {
	switch e := ev.(type) {
	case HistorySyncEvent:
		cs.ApplyHistorySync(&waproto.HistorySync{
			Conversations: e.Conversations,
			Pushnames:     e.Pushnames,
		})
	case MessageEvent:
		cs.applyMessageEvent(e)
	case ContactEvent:
		name := e.FullName
		if name == "" {
			name = e.FirstName
		}
		cs.ApplyContact(e.JID, name)
	case AppStateMutationEvent:
		cs.ApplyAppStateMutation(e.Index, e.Action, e.Operation)
	case ReceiptEvent:
		if e.Type == "read" {
			cs.markRead(e.From)
		}
	}
}

func (cs *ChatStore) applyMessageEvent(e MessageEvent) {
	if e.From == "" {
		return
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()

	ch := cs.chatLocked(e.From)
	if e.Timestamp > ch.ConversationTimestamp {
		ch.ConversationTimestamp = e.Timestamp
	}
	if e.Timestamp > ch.LastMessageTime {
		ch.LastMessageTime = e.Timestamp
	}

	from := e.Sender
	if from == "" {
		from = e.From
	}
	cs.indexMessageLocked(e.From, StoredMessage{
		Key:       e.ID,
		FromJID:   from,
		Timestamp: e.Timestamp,
		Text:      e.Text,
		Type:      string(e.Type),
		Media:     e.Media,
	})

	if e.PushName != "" {
		cs.contactLocked(from).PushName = e.PushName
	}
}

func (cs *ChatStore) markRead(jid string) {
	if jid == "" {
		return
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if ch := cs.chats[jid]; ch != nil {
		ch.UnreadCount = 0
	}
}

// --- query surface ---

// Chats returns a snapshot of all chats, ordered by ConversationTimestamp
// descending (most recent first).
func (cs *ChatStore) Chats() []Chat {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	out := make([]Chat, 0, len(cs.chats))
	for _, ch := range cs.chats {
		out = append(out, *ch)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].ConversationTimestamp > out[j].ConversationTimestamp
	})
	return out
}

// Chat returns the chat for jid, if present.
func (cs *ChatStore) Chat(jid string) (Chat, bool) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	if ch := cs.chats[jid]; ch != nil {
		return *ch, true
	}
	return Chat{}, false
}

// Contact returns the contact for jid, if present.
func (cs *ChatStore) Contact(jid string) (Contact, bool) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	if c := cs.contacts[jid]; c != nil {
		return *c, true
	}
	return Contact{}, false
}

// ChatMessages returns up to limit of the most recent messages for jid, in
// chronological (oldest-first) order. A non-positive limit returns all.
func (cs *ChatStore) ChatMessages(jid string, limit int) []StoredMessage {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	msgs := cs.messages[jid]
	if len(msgs) == 0 {
		return nil
	}
	start := 0
	if limit > 0 && limit < len(msgs) {
		start = len(msgs) - limit
	}
	out := make([]StoredMessage, len(msgs)-start)
	copy(out, msgs[start:])
	return out
}

// --- internal helpers (callers hold cs.mu) ---

func (cs *ChatStore) chatLocked(jid string) *Chat {
	ch := cs.chats[jid]
	if ch == nil {
		ch = &Chat{JID: jid}
		cs.chats[jid] = ch
	}
	return ch
}

func (cs *ChatStore) contactLocked(jid string) *Contact {
	c := cs.contacts[jid]
	if c == nil {
		c = &Contact{JID: jid}
		cs.contacts[jid] = c
	}
	return c
}

// indexMessageLocked appends sm to the chat's message slice, de-duplicating on
// Key (a later copy overwrites an earlier one), and keeps the slice ordered by
// Timestamp ascending.
func (cs *ChatStore) indexMessageLocked(jid string, sm StoredMessage) {
	msgs := cs.messages[jid]
	for i := range msgs {
		if msgs[i].Key != "" && msgs[i].Key == sm.Key {
			msgs[i] = sm
			cs.messages[jid] = msgs
			return
		}
	}
	msgs = append(msgs, sm)
	sort.SliceStable(msgs, func(i, j int) bool {
		return msgs[i].Timestamp < msgs[j].Timestamp
	})
	cs.messages[jid] = msgs
}

// storedFromWebMessageInfo distills a history-sync WebMessageInfo into a
// StoredMessage, extracting the text body when present.
func storedFromWebMessageInfo(chatJID string, wmi *waproto.WebMessageInfo) StoredMessage {
	sm := StoredMessage{FromJID: chatJID, Type: "unknown", Raw: wmi}
	if key := wmi.GetKey(); key != nil {
		sm.Key = key.GetId()
		if p := key.GetParticipant(); p != "" {
			sm.FromJID = p
		}
	}
	sm.Timestamp = int64(wmi.GetMessageTimestamp())
	if msg := wmi.GetMessage(); msg != nil {
		if txt := msg.GetConversation(); txt != "" {
			sm.Text = txt
			sm.Type = string(MessageText)
		} else if ext := msg.GetExtendedTextMessage(); ext != nil {
			sm.Text = ext.GetText()
			sm.Type = string(MessageText)
		}
	}
	return sm
}
