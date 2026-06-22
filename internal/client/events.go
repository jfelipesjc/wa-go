package client

import (
	"github.com/felipeleal/wa-go/internal/waproto"
	"github.com/felipeleal/wa-go/internal/wire"
)

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
	// MessagePollVote is a poll-update message (a vote); see PollVote. The vote
	// itself is encrypted and is exposed raw (the crypto is handled elsewhere).
	MessagePollVote MessageType = "poll_vote"
	// MessageButtons is a buttonsMessage (template-style quick-reply buttons);
	// Text holds the content text and Buttons the choices.
	MessageButtons MessageType = "buttons"
	// MessageList is a listMessage (a menu of selectable rows); Text holds the
	// description and List the sections/rows.
	MessageList MessageType = "list"
	// MessageTemplate is a templateMessage (hydrated four-row template); Text
	// holds the hydrated content text and Buttons the hydrated buttons.
	MessageTemplate MessageType = "template"
	// MessageInteractive is an interactiveMessage (native-flow / product list);
	// Text holds the body text.
	MessageInteractive MessageType = "interactive"
	// MessageButtonReply is the reply to a buttonsMessage; see ButtonReply.
	MessageButtonReply MessageType = "button_reply"
	// MessageListReply is the reply to a listMessage; see ListReply.
	MessageListReply MessageType = "list_reply"
	// MessageTemplateReply is the reply to a templateMessage; see ButtonReply.
	MessageTemplateReply MessageType = "template_reply"
	// MessageInteractiveReply is the reply to an interactiveMessage; see
	// InteractiveReply.
	MessageInteractiveReply MessageType = "interactive_reply"
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

// ButtonInfo is one quick-reply button of a buttonsMessage / templateMessage.
type ButtonInfo struct {
	ID   string
	Text string
}

// ListItemRow is one selectable row of a received listMessage section. It is a
// receive-side type (distinct from the send path's ListRow) so the two surfaces
// can evolve independently.
type ListItemRow struct {
	RowID       string
	Title       string
	Description string
}

// ListItemSection is one section (group of rows) of a received listMessage.
type ListItemSection struct {
	Title string
	Rows  []ListItemRow
}

// ListInfo describes a received listMessage (interactive menu).
type ListInfo struct {
	Title       string
	Description string
	ButtonText  string
	FooterText  string
	Sections    []ListItemSection
}

// ButtonReplyInfo describes the reply to a buttonsMessage or templateMessage:
// the id of the selected button and its display text.
type ButtonReplyInfo struct {
	SelectedID string
	Text       string
}

// ListReplyInfo describes the reply to a listMessage: the id of the selected
// row plus, when present, its title/description.
type ListReplyInfo struct {
	RowID       string
	Title       string
	Description string
}

// InteractiveReplyInfo describes the reply to an interactiveMessage: the body
// text plus, for native-flow replies, the flow name and raw params JSON.
type InteractiveReplyInfo struct {
	Text       string
	Name       string
	ParamsJSON string
}

// PollVoteInfo describes a poll-update message (a vote). The vote is encrypted;
// EncPayload/EncIV expose the ciphertext (decryption is handled elsewhere) and
// PollKey references the poll-creation message the vote belongs to.
type PollVoteInfo struct {
	PollKey    MessageRef
	EncPayload []byte
	EncIV      []byte
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

	// Interactive content (buttons / list / template / interactive) and replies.
	Buttons          []ButtonInfo
	List             *ListInfo
	ButtonReply      *ButtonReplyInfo
	ListReply        *ListReplyInfo
	InteractiveReply *InteractiveReplyInfo
	// PollVote is set for Type == MessagePollVote (a pollUpdateMessage).
	PollVote *PollVoteInfo

	// ViewOnce / Ephemeral report which transport wrappers were peeled off the
	// real content while parsing. They are independent (a view-once message can
	// also be ephemeral).
	ViewOnce  bool
	Ephemeral bool

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

// --- granular notification / receipt events ---
//
// These mirror Baileys' ev.emit surface for <notification> and <receipt>
// stanzas (Socket/messages-recv.js: processNotification / handleGroupNotification
// / handleReceipt). They let the API layer (#8) turn raw stanzas into rich
// typed webhooks without re-parsing the wire format.

// GroupParticipantAction names the kind of participant change carried by a
// w:gp2 notification (the child tag of the <notification type=w:gp2>).
type GroupParticipantAction string

const (
	// GroupParticipantAdd / Remove / Promote / Demote / Leave map directly to the
	// w:gp2 child tags <add>/<remove>/<promote>/<demote>/<leave>. Modify is the
	// participant-number-change (<modify>).
	GroupParticipantAdd     GroupParticipantAction = "add"
	GroupParticipantRemove  GroupParticipantAction = "remove"
	GroupParticipantPromote GroupParticipantAction = "promote"
	GroupParticipantDemote  GroupParticipantAction = "demote"
	GroupParticipantLeave   GroupParticipantAction = "leave"
	GroupParticipantModify  GroupParticipantAction = "modify"
)

// GroupParticipantsUpdateEvent reports a membership/role change in a group,
// parsed from a <notification type=w:gp2> with an add/remove/promote/demote/
// leave/modify child. GroupJID is the group; By is the acting participant (the
// notification's `participant` attr, may be empty); Participants are the affected
// JIDs (the <participant jid=.../> children).
type GroupParticipantsUpdateEvent struct {
	GroupJID     string
	Action       GroupParticipantAction
	Participants []string
	By           string
}

func (GroupParticipantsUpdateEvent) isEvent() {}

// GroupUpdateEvent reports a non-membership change to a group's settings, parsed
// from a <notification type=w:gp2> with a subject/announce/ephemeral/... child.
// Only the pointer field(s) relevant to the change are non-nil.
type GroupUpdateEvent struct {
	GroupJID string
	By       string
	// Subject is set for a <subject> child (the new group name).
	Subject *string
	// Announce is set for an <announcement>/<not_announcement> child (true =
	// announce-only / admins-only messaging).
	Announce *bool
	// Locked is set for a <locked>/<unlocked> child (true = only admins can edit
	// group info).
	Locked *bool
	// Ephemeral is set for an <ephemeral>/<not_ephemeral> child: the disappearing-
	// message expiration in seconds (0 = turned off).
	Ephemeral *uint32
	// Description is set for a <description> child (the new group description text).
	Description *string
}

func (GroupUpdateEvent) isEvent() {}

// PictureUpdateEvent reports a profile/group picture change, parsed from a
// <notification type=picture> with a <set>/<delete> child. JID is the subject
// (contact or group); Author is the acting JID; Removed is true for a delete.
type PictureUpdateEvent struct {
	JID     string
	Author  string
	Removed bool
}

func (PictureUpdateEvent) isEvent() {}

// AppStateSyncDirtyEvent signals that one or more app-state collections are dirty
// and should be resynced, parsed from a <notification type=account_sync|server_sync>.
// Collections names the dirty collections (the <collection name=.../> children, or
// the child tag for an account_sync). It is advisory: the loop does NOT force a
// resync, leaving that policy to the consumer.
type AppStateSyncDirtyEvent struct {
	Collections []string
}

func (AppStateSyncDirtyEvent) isEvent() {}

// ContactUpdateEvent reports a contact/devices notification (a
// <notification type=contacts|devices>): a lightweight signal that the contact's
// devices or details changed. JID is the contact; Type is the notification type.
type ContactUpdateEvent struct {
	JID  string
	Type string
}

func (ContactUpdateEvent) isEvent() {}

// ReceiptType classifies a delivery/read receipt update.
type ReceiptType string

const (
	// ReceiptDelivery is a plain delivery receipt (no `type` attr on the <receipt>).
	ReceiptDelivery ReceiptType = "delivery"
	// ReceiptRead is a read receipt (type=read or read-self).
	ReceiptRead ReceiptType = "read"
	// ReceiptPlayed is a played receipt for a voice note (type=played).
	ReceiptPlayed ReceiptType = "played"
)

// ReceiptUpdateEvent reports a delivery/read/played receipt the server forwarded
// for one or more of our outgoing messages. For is the set of message IDs the
// receipt covers (the stanza id plus any batched <list><item id=.../> children).
// From is the peer JID; Participant is the group member (empty for 1:1); Type
// classifies the receipt; Timestamp is the stanza `t` (unix seconds, 0 if absent).
type ReceiptUpdateEvent struct {
	For         []string
	From        string
	Participant string
	Type        ReceiptType
	Timestamp   int64
}

func (ReceiptUpdateEvent) isEvent() {}

// NotificationEvent is the generic catch-all for a <notification> the granular
// handlers did not classify, so no stanza is silently dropped. Type/From mirror
// the stanza attrs; Raw is the full decoded node for inspection.
type NotificationEvent struct {
	Type string
	From string
	Raw  wire.Node
}

func (NotificationEvent) isEvent() {}
