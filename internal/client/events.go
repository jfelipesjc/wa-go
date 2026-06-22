package client

import "github.com/felipeleal/wa-go/internal/waproto"

// This file defines the rich incoming-message event surface produced by the
// receive path (receive.go + receive_parse.go). The pairing/connection events
// (QREvent, PairSuccessEvent, ...) live in client.go; the message-shaped events
// live here.

// MessageType classifies the decoded content of a MessageEvent so consumers can
// switch on the kind without re-inspecting the raw protobuf. It mirrors the
// content oneof of WAProto.Message.
type MessageType string

const (
	// MessageText is a plain text body (conversation or extendedTextMessage).
	MessageText MessageType = "text"
	// MessageImage / MessageVideo / MessageAudio / MessageDocument /
	// MessageSticker are media messages; their metadata is in MessageEvent.Media.
	MessageImage    MessageType = "image"
	MessageVideo    MessageType = "video"
	MessageAudio    MessageType = "audio"
	MessageDocument MessageType = "document"
	MessageSticker  MessageType = "sticker"
	// MessageLocation is a (live or static) location share; see Location.
	MessageLocation MessageType = "location"
	// MessageContact is a shared contact (vCard); see Contact.
	MessageContact MessageType = "contact"
	// MessageReaction is an emoji reaction to another message; see Reaction.
	MessageReaction MessageType = "reaction"
	// MessageRevoke is a "delete for everyone" of an earlier message; the target
	// is in Revoked.
	MessageRevoke MessageType = "revoke"
	// MessageEdit is an edit of an earlier message; the new content is parsed into
	// the same MessageEvent (Text/Media/...) and Edited points at the original key.
	MessageEdit MessageType = "edit"
	// MessagePoll is a poll-creation message; see Poll.
	MessagePoll MessageType = "poll"
	// MessageUnknown is any content the parser did not recognize. Raw still holds
	// the decoded protobuf for inspection.
	MessageUnknown MessageType = "unknown"
)

// MediaKind names the concrete media category carried in MediaInfo.
type MediaKind string

const (
	MediaImage    MediaKind = "image"
	MediaVideo    MediaKind = "video"
	MediaAudio    MediaKind = "audio"
	MediaDocument MediaKind = "document"
	MediaSticker  MediaKind = "sticker"
)

// MediaInfo carries the metadata needed to download and decrypt a media payload
// later (the HTTP fetch is a separate concern). Field availability depends on
// Kind; zero values mean "absent".
type MediaInfo struct {
	Kind          MediaKind
	Mimetype      string
	Caption       string
	FileName      string // documents
	FileLength    uint64
	MediaKey      []byte
	DirectPath    string
	URL           string
	FileSha256    []byte
	FileEncSha256 []byte

	// Dimensions / duration, populated per kind.
	Width   uint32
	Height  uint32
	Seconds uint32 // audio/video duration

	// Audio-specific.
	IsPTT bool // voice note (push-to-talk)

	// Document-specific.
	PageCount uint32

	// Sticker-specific.
	IsAnimated bool

	// JpegThumbnail (or PngThumbnail for stickers) inline preview, if present.
	Thumbnail []byte
}

// ReactionInfo describes an emoji reaction: the target message key and the emoji
// (empty Text means the reaction was removed).
type ReactionInfo struct {
	Key  MessageRef
	Text string
}

// LocationInfo describes a shared location.
type LocationInfo struct {
	Latitude  float64
	Longitude float64
	Name      string
	Address   string
	IsLive    bool
}

// ContactInfo describes a shared contact (vCard).
type ContactInfo struct {
	DisplayName string
	Vcard       string
}

// PollInfo describes a poll-creation message.
type PollInfo struct {
	Name                   string
	Options                []string
	SelectableOptionsCount uint32
}

// MessageRef identifies a specific message (the WAProto.MessageKey subset that
// matters for replies, reactions, edits and revokes).
type MessageRef struct {
	ID          string
	FromMe      bool
	RemoteJID   string
	Participant string
}

// QuotedInfo carries the reply context of a message: the quoted message's key
// (StanzaID/Participant from ContextInfo) and, when present, its decoded text.
type QuotedInfo struct {
	StanzaID    string
	Participant string
	Text        string
	// Message is the raw quoted protobuf, if the sender embedded it.
	Message *waproto.Message
}

// MessageEvent carries a decrypted incoming message of any type. Text is always
// populated for text/caption-bearing messages (preserving the historical
// behavior cmd/wa-pair relies on). The typed sub-structs (Media, Reaction, ...)
// are non-nil only for their respective Type. Raw holds the fully decoded
// (and unwrapped) protobuf for callers that need fields the event omits.
type MessageEvent struct {
	From      string // sender JID (the group JID for group messages)
	Sender    string // for group messages, the participant JID; "" for 1:1
	ID        string
	Timestamp int64
	PushName  string
	IsGroup   bool

	Type     MessageType
	Text     string
	Media    *MediaInfo
	Reaction *ReactionInfo
	Location *LocationInfo
	Contact  *ContactInfo
	Poll     *PollInfo

	// Quoted is the reply context (extendedTextMessage / media contextInfo).
	Quoted *QuotedInfo
	// Mentions are the JIDs @-mentioned in the message (contextInfo.mentionedJid).
	Mentions []string

	// Revoked is set for Type == MessageRevoke: the key of the deleted message.
	Revoked *MessageRef
	// Edited is set for Type == MessageEdit: the key of the original message.
	Edited *MessageRef

	Raw *waproto.Message
}

func (MessageEvent) isEvent() {}

// ReceiptEvent reports a delivery/read receipt the server forwarded for one of
// our outgoing messages. Type is the receipt type ("", "read", "played", ...).
type ReceiptEvent struct {
	From        string
	Participant string
	ID          string
	Type        string
}

func (ReceiptEvent) isEvent() {}

// HistorySyncEvent carries a decoded chunk of history the server pushed after
// login (a HISTORY_SYNC_NOTIFICATION downloaded + inflated into a HistorySync).
// SyncType classifies the chunk (initial bootstrap, recent, full, push-name...).
// Conversations holds the chats and their messages; Pushnames maps JIDs to
// display names. Raw is the full decoded protobuf for callers that need more.
type HistorySyncEvent struct {
	SyncType      waproto.HistorySyncType
	Progress      uint32
	ChunkOrder    uint32
	Conversations []*waproto.Conversation
	Pushnames     []*waproto.Pushname
	Raw           *waproto.HistorySync
}

func (HistorySyncEvent) isEvent() {}

// --- app-state (resync) events ---

// ContactEvent reports a contactAction mutation surfaced by an app-state resync:
// the contact's JID plus its full name / first name as set on the account.
type ContactEvent struct {
	JID       string
	FullName  string
	FirstName string
}

func (ContactEvent) isEvent() {}

// AppStateMutationEvent is the generic catch-all for an app-state mutation that
// does not have a dedicated typed event (mute/pin/archive/star/...). Collection
// names the source collection; Index is the decoded JSON index array; Action is
// the decoded SyncActionValue; Operation is SET or REMOVE.
type AppStateMutationEvent struct {
	Collection string
	Index      []string
	Action     *waproto.SyncActionValue
	Operation  waproto.SyncdMutation_SyncdOperation
}

func (AppStateMutationEvent) isEvent() {}

// PresenceEvent reports a presence/chatstate update for a contact: From is the
// peer JID, State is one of available/unavailable/composing/paused, and LastSeen
// is the optional "last" attribute (unix seconds) the server attaches to an
// unavailable presence (0 when absent).
type PresenceEvent struct {
	From     string
	State    string
	LastSeen int64
}

func (PresenceEvent) isEvent() {}
