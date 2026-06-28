// Package wa is the public API facade for the wa-go WhatsApp library.
//
// The heavy implementation lives in internal/ packages (so it can't be imported
// directly by other modules); this package re-exports the consumer-facing surface
// via type aliases so a separate project (e.g. an Evolution-style service) can
// depend on github.com/jfelipesjc/wa-go/wa.
//
// This is the equivalent of Baileys' index.ts entry point.
package wa

import (
	"github.com/jfelipesjc/wa-go/internal/client"
	"github.com/jfelipesjc/wa-go/internal/manager"
	"github.com/jfelipesjc/wa-go/internal/store"
)

// --- Store ---

// Store persists device credentials, Signal sessions, prekeys and app-state.
type Store = store.Store

// OpenStore opens (creating if needed) a SQLite-backed Store at path.
func OpenStore(path string) (Store, error) { return store.OpenSQLite(path) }

// --- Client ---

// Client is a single WhatsApp connection (one device/number).
type Client = client.Client

// MsgKey identifies a single message for star/delete-for-me style actions.
type MsgKey = client.MsgKey

// NewClient builds a Client backed by the given Store.
func NewClient(s Store) *Client { return client.New(s) }

// ChatStore materializes history sync + contacts + app-state into queryable
// chats/contacts/messages. Plug it into a Client's event stream via Consume.
type ChatStore = client.ChatStore

// NewChatStore builds an empty ChatStore.
func NewChatStore() *ChatStore { return client.NewChatStore() }

// Chat is a materialized chat row (JID, name, archived/pinned/muted, timestamps).
type Chat = client.Chat

// Contact is a materialized peer identity (JID + name surfaces).
type Contact = client.Contact

// MediaOpts carries optional metadata for media sends (mimetype, dimensions,
// duration, etc.). Used by SendSticker and the lower-level media senders.
type MediaOpts = client.MediaOpts

// PrivacySetting names a privacy toggle (lastSeen, online, profile, status,
// readReceipts, groupsAdd) for UpdatePrivacy.
type PrivacySetting = client.PrivacySetting

// Privacy setting names, re-exported so external callers can pass them to
// (*Client).UpdatePrivacy without importing internal/client.
const (
	PrivacyLastSeen       = client.PrivacyLastSeen
	PrivacyOnline         = client.PrivacyOnline
	PrivacyProfilePicture = client.PrivacyProfilePicture
	PrivacyStatus         = client.PrivacyStatus
	PrivacyReadReceipts   = client.PrivacyReadReceipts
	PrivacyGroupsAdd      = client.PrivacyGroupsAdd
)

// --- Groups, Communities & Newsletters ---
//
// The Community* and Newsletter* methods live on (*Client) (= client.Client via
// the alias above) and are therefore already callable; these aliases re-export
// their input/output types so an external module can name and construct them.

// GroupInfo is the parsed metadata of a group or community: subject, owner,
// description, creation time and participants.
type GroupInfo = client.GroupInfo

// GroupParticipant is one member of a group/community (jid + admin flags).
type GroupParticipant = client.GroupParticipant

// GroupParticipantResult reports the per-participant outcome (jid + status code)
// of a participants update on a group or community.
type GroupParticipantResult = client.GroupParticipantResult

// GroupLinkInfo is a sub-group linked into a community (JID + subject).
type GroupLinkInfo = client.GroupLinkInfo

// CommunityMembershipRequest is one pending join request for a community.
type CommunityMembershipRequest = client.CommunityMembershipRequest

// NewsletterInfo is the parsed metadata of a channel (newsletter).
type NewsletterInfo = client.NewsletterInfo

// NewsletterMessage is a single message from a NewsletterFetchMessages reply.
type NewsletterMessage = client.NewsletterMessage

// LiveUpdatesSubscription is the result of SubscribeLiveUpdates (a duration).
type LiveUpdatesSubscription = client.LiveUpdatesSubscription

// NewsletterUpdateInput carries the optional metadata fields an admin may edit
// on a channel (Name/Description/Picture; a nil pointer means leave unchanged).
type NewsletterUpdateInput = client.NewsletterUpdateInput

// NewsletterKeyType selects how a newsletter is addressed in a metadata fetch
// (by JID or by invite key).
type NewsletterKeyType = client.NewsletterKeyType

// Newsletter key types, re-exported for (*Client).NewsletterMetadata.
const (
	NewsletterKeyJID    = client.NewsletterKeyJID
	NewsletterKeyInvite = client.NewsletterKeyInvite
)

// NewsletterReactionMode is the channel-wide reaction policy.
type NewsletterReactionMode = client.NewsletterReactionMode

// Newsletter reaction modes, re-exported for (*Client).NewsletterReactionMode.
const (
	ReactionModeAll       = client.ReactionModeAll
	ReactionModeBasic     = client.ReactionModeBasic
	ReactionModeNone      = client.ReactionModeNone
	ReactionModeBlocklist = client.ReactionModeBlocklist
)

// EnableDebug turns on verbose pairing/connection diagnostics to w.
var EnableDebug = client.EnableDebug

// --- Events ---

// Event is the sum type delivered on Client.Events().
type Event = client.Event

type (
	QREvent                      = client.QREvent
	PairingCodeEvent             = client.PairingCodeEvent
	PairSuccessEvent             = client.PairSuccessEvent
	LoggedInEvent                = client.LoggedInEvent
	DisconnectedEvent            = client.DisconnectedEvent
	MessageEvent                 = client.MessageEvent
	ReceiptEvent                 = client.ReceiptEvent
	ReceiptUpdateEvent           = client.ReceiptUpdateEvent
	PresenceEvent                = client.PresenceEvent
	CallEvent                    = client.CallEvent
	ContactEvent                 = client.ContactEvent
	ContactUpdateEvent           = client.ContactUpdateEvent
	GroupParticipantsUpdateEvent = client.GroupParticipantsUpdateEvent
	GroupUpdateEvent             = client.GroupUpdateEvent
	PictureUpdateEvent           = client.PictureUpdateEvent
	AppStateSyncDirtyEvent       = client.AppStateSyncDirtyEvent
	HistorySyncEvent             = client.HistorySyncEvent
	NotificationEvent            = client.NotificationEvent
)

// MessageType classifies a received MessageEvent.
type MessageType = client.MessageType

// MessageType values (see MessageEvent.Type).
const (
	MessageText             = client.MessageText
	MessageImage            = client.MessageImage
	MessageVideo            = client.MessageVideo
	MessageAudio            = client.MessageAudio
	MessageDocument         = client.MessageDocument
	MessageSticker          = client.MessageSticker
	MessageLocation         = client.MessageLocation
	MessageContact          = client.MessageContact
	MessageReaction         = client.MessageReaction
	MessageRevoke           = client.MessageRevoke
	MessageEdit             = client.MessageEdit
	MessagePoll             = client.MessagePoll
	MessagePollVote         = client.MessagePollVote
	MessageButtons          = client.MessageButtons
	MessageList             = client.MessageList
	MessageTemplate         = client.MessageTemplate
	MessageInteractive      = client.MessageInteractive
	MessageButtonReply      = client.MessageButtonReply
	MessageListReply        = client.MessageListReply
	MessageTemplateReply    = client.MessageTemplateReply
	MessageInteractiveReply = client.MessageInteractiveReply
	MessageUnknown          = client.MessageUnknown
)

// --- Manager (multi-session) ---

// Manager runs many Clients concurrently with supervision and reconnection.
type Manager = manager.Manager

// ManagedClient is a Manager-supervised instance handle (SendText, etc.).
type ManagedClient = manager.ManagedClient

// Option configures a Manager.
type Option = manager.Option

// State is an instance's lifecycle state.
type State = manager.State

const (
	StateIdle         = manager.StateIdle
	StateConnecting   = manager.StateConnecting
	StateConnected    = manager.StateConnected
	StateLoggedIn     = manager.StateLoggedIn
	StateDisconnected = manager.StateDisconnected
	StateBackoff      = manager.StateBackoff
)

// InstanceEvent is an Event tagged with the originating instance name.
type InstanceEvent = manager.InstanceEvent

// NewManager builds a Manager.
func NewManager(opts ...Option) *Manager { return manager.New(opts...) }

// WithConcurrency bounds how many instances connect at once (default 16).
func WithConcurrency(n int) Option { return manager.WithConcurrency(n) }
