// Package client orchestrates the WhatsApp multi-device pairing/auth flow on top
// of the lower layers: ws (transport), wire (Noise + binary nodes), keys
// (identity), store (persistence) and waproto (ClientPayload + ADV protobufs).
//
// The flow mirrors Baileys' Socket/socket.js:
//
//  1. Connect: ws.Dial -> wire.NewConn -> Conn.Handshake (RegistrationPayload for
//     a fresh identity, or LoginPayload when creds are already registered) ->
//     read loop.
//  2. pair-device: on <iq type=set><pair-device><ref>, build the QR string, emit
//     QREvent, and reply with an empty <iq type=result>. Multiple <ref> children
//     re-emit the QR (refresh).
//  3. pair-success: on <iq type=set><pair-success>, run handlePairSuccess
//     (HMAC + signature verification, re-sign, build the pair-device-sign reply),
//     persist creds, emit PairSuccessEvent. The server then ends the stream.
//  4. relogin: reconnect with LoginPayload using the saved creds; handle
//     <success> (LoggedInEvent) and <failure> (DisconnectedEvent).
package client

import (
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/jfelipesjc/wa-go/internal/control"
	"github.com/jfelipesjc/wa-go/internal/keys"
	"github.com/jfelipesjc/wa-go/internal/store"
	"github.com/jfelipesjc/wa-go/internal/waproto"
	"github.com/jfelipesjc/wa-go/internal/wire"
	"github.com/jfelipesjc/wa-go/internal/ws"
)

// The device fingerprint (client version, browser tuple, locale) the
// registration/login ClientPayload advertises now lives in the per-Client
// control.DeviceProfile (default control.DefaultProfile()), overridable via
// WithDeviceProfile. See internal/control/device_profile.go.

// Event is the sum type emitted on the Client's event channel. Exactly one of
// the concrete event types is sent per channel value.
type Event interface{ isEvent() }

// QREvent carries a freshly built pairing QR string to render/scan.
type QREvent struct{ Code string }

// PairingCodeEvent carries the 8-character pairing code the user types on their
// phone (WhatsApp > Linked devices > Link with phone number). It is emitted
// instead of QREvent when pairing via ConnectWithPairingCode.
type PairingCodeEvent struct{ Code string }

// PairSuccessEvent signals a successful first-time pairing. JID is the device's
// assigned WhatsApp JID ("me").
type PairSuccessEvent struct{ JID string }

// LoggedInEvent signals the server accepted a resume/login (<success>).
type LoggedInEvent struct{}

// DisconnectedEvent signals the connection ended. Reason carries a short cause.
type DisconnectedEvent struct{ Reason string }

// MessageEvent (the rich incoming-message event) and ReceiptEvent are defined in
// events.go.

func (QREvent) isEvent()           {}
func (PairingCodeEvent) isEvent()  {}
func (PairSuccessEvent) isEvent()  {}
func (LoggedInEvent) isEvent()     {}
func (DisconnectedEvent) isEvent() {}

// Client orchestrates the pairing/auth flow against one device's store.
type Client struct {
	store store.Store

	events chan Event

	// dial is the transport dialer; defaults to the real WebSocket transport
	// but can be overridden in tests with a replay/loopback transport.
	dial func(ctx context.Context) (io.ReadWriteCloser, error)

	// newEphemeral generates the Noise ephemeral key pair per connection.
	// Overridable for deterministic tests.
	newEphemeral func() (keys.KeyPair, error)

	// profile is the device fingerprint advertised in the registration/login
	// ClientPayload. Defaults to control.DefaultProfile() (the historical
	// hardcoded values), overridable via WithDeviceProfile.
	profile control.DeviceProfile

	// pacer, when non-nil, throttles SendText with a human-like delay before each
	// message goes out. nil = no pacing (the original zero-delay behavior).
	pacer control.Pacer

	// uploader, when non-nil, performs the live HTTP upload of an encrypted media
	// blob to WhatsApp's media servers and returns its directPath/url. It is nil
	// by default (this build is offline), so the Send*media helpers require the
	// caller to pass a pre-uploaded MediaRef. See send_media.go.
	uploader mediaUploader

	// onOutgoing / onIncoming are raw frame hooks invoked with each node just
	// before encryption (outgoing) and just after decode (incoming). They are
	// optional and run under recover so a panicking hook cannot derail the
	// send/read loops. Guarded by hooksMu.
	hooksMu    sync.RWMutex
	onOutgoing []func(*wire.Node)
	onIncoming []func(wire.Node)

	// active holds the live login session's send function and creds, set while
	// loginLoop is running so SendText can use the established connection. It is
	// nil before login / after disconnect. Guarded by mu.
	mu     sync.Mutex
	active *session

	// pending maps an outstanding iq stanza id to the channel that delivers the
	// matching <iq type=result|error> back to the requester. The single read loop
	// (loginLoop) routes results here so request/response works without a second
	// reader. Guarded by pendingMu.
	pendingMu sync.Mutex
	pending   map[string]chan wire.Node

	// retry holds the reliability state for retry receipts:
	//   - msgRetry counts how many retry receipts we have asked for per inbound
	//     msgId (capped at maxMsgRetryCount), mirroring Baileys' msgRetryCache.
	//   - sentCache remembers the messages WE sent (msgId -> payload + recipient)
	//     so we can re-encrypt and resend when a device asks us for a retry.
	// Both are in-memory (Baileys keeps them in caches too) and guarded by retryMu.
	retryMu     sync.Mutex
	msgRetry    map[string]int
	sentCache   map[string]*sentMessage
	sentOrder   []string // FIFO insertion order for eviction
	sentCap     int      // max entries in sentCache (0 => default)
	retryPreKey atomic.Uint32
}

// sentMessage is one entry of the outbound message cache: the original plaintext
// WAProto.Message and the recipient JID it was sent to, kept so a retry receipt
// can be answered by re-encrypting the same content for the requesting device.
type sentMessage struct {
	msg   *waproto.Message
	toJID string
}

// session is the live login state SendText needs: the serialized send function
// (already guarded by loginLoop's sendMu) and the device creds.
type session struct {
	send  func(wire.Node) error
	creds *store.Creds
}

// New constructs a Client backed by the given store, using the real WebSocket
// transport.
func New(s store.Store) *Client {
	return NewWithDialer(s, dialWebSocket)
}

// NewWithDialer constructs a Client backed by the given store but with an
// injectable transport dialer. The production path (New) passes dialWebSocket;
// tests can pass an in-memory loopback transport to drive the client offline.
// A nil dial falls back to dialWebSocket.
func NewWithDialer(s store.Store, dial func(ctx context.Context) (io.ReadWriteCloser, error)) *Client {
	if dial == nil {
		dial = dialWebSocket
	}
	c := &Client{
		store:        s,
		events:       make(chan Event, 16),
		dial:         dial,
		newEphemeral: keys.GenKeyPair,
		profile:      control.DefaultProfile(),
		pending:      make(map[string]chan wire.Node),
		msgRetry:     make(map[string]int),
		sentCache:    make(map[string]*sentMessage),
		sentCap:      defaultSentCacheCap,
	}
	// Retry pre-keys are minted from a high id range so they never collide with
	// the initial 1..initialPreKeyCount batch uploaded at login.
	c.retryPreKey.Store(retryPreKeyBase)
	return c
}

// Option configures a Client at construction time.
type Option func(*Client)

// NewWithOptions constructs a Client backed by the given store and transport
// dialer, applying the supplied options. A nil dial falls back to dialWebSocket.
// This is the extension point for the Control Layer (device profile, pacer,
// frame hooks); New / NewWithDialer remain the zero-config constructors.
func NewWithOptions(s store.Store, dial func(ctx context.Context) (io.ReadWriteCloser, error), opts ...Option) *Client {
	c := NewWithDialer(s, dial)
	for _, o := range opts {
		o(c)
	}
	return c
}

// WithDeviceProfile sets the device fingerprint advertised in the
// registration/login ClientPayload. The default is control.DefaultProfile().
func WithDeviceProfile(p control.DeviceProfile) Option {
	return func(c *Client) { c.profile = p }
}

// WithPacer installs a send-cadence pacer. SendText calls Wait before each
// message. nil leaves the default (no pacing).
func WithPacer(p control.Pacer) Option {
	return func(c *Client) { c.pacer = p }
}

// WithMediaUploader installs the live media-upload transport used by the Send*
// media helpers when the caller does not supply a pre-uploaded MediaRef. nil
// (the default) leaves media upload unconfigured. See send_media.go.
func WithMediaUploader(u mediaUploader) Option {
	return func(c *Client) { c.uploader = u }
}

// OnOutgoingNode registers a callback invoked with each outgoing node just
// before it is encoded/encrypted. The pointer lets a hook inspect or mutate the
// node in place. Hooks run under recover; a panic is swallowed (and dropped) so
// it cannot break the send path. Safe to call concurrently.
func (c *Client) OnOutgoingNode(fn func(*wire.Node)) {
	if fn == nil {
		return
	}
	c.hooksMu.Lock()
	c.onOutgoing = append(c.onOutgoing, fn)
	c.hooksMu.Unlock()
}

// OnIncomingNode registers a callback invoked with each decoded incoming node
// (post-decrypt, post-decode). Hooks run under recover. Safe to call
// concurrently.
func (c *Client) OnIncomingNode(fn func(wire.Node)) {
	if fn == nil {
		return
	}
	c.hooksMu.Lock()
	c.onIncoming = append(c.onIncoming, fn)
	c.hooksMu.Unlock()
}

// runOutgoingHooks invokes each outgoing hook with n, recovering from panics so
// a misbehaving hook never derails the send loop.
func (c *Client) runOutgoingHooks(n *wire.Node) {
	c.hooksMu.RLock()
	hooks := c.onOutgoing
	c.hooksMu.RUnlock()
	for _, fn := range hooks {
		safeNodePtrHook(fn, n)
	}
}

// runIncomingHooks invokes each incoming hook with n, recovering from panics.
func (c *Client) runIncomingHooks(n wire.Node) {
	c.hooksMu.RLock()
	hooks := c.onIncoming
	c.hooksMu.RUnlock()
	for _, fn := range hooks {
		safeNodeHook(fn, n)
	}
}

func safeNodePtrHook(fn func(*wire.Node), n *wire.Node) {
	defer func() { _ = recover() }()
	fn(n)
}

func safeNodeHook(fn func(wire.Node), n wire.Node) {
	defer func() { _ = recover() }()
	fn(n)
}

// Events returns the channel on which pairing/login events are delivered. It is
// closed when Connect returns.
func (c *Client) Events() <-chan Event { return c.events }

// emit delivers an event, dropping it if the consumer is not keeping up rather
// than blocking the read loop. Tests drain the channel, so the buffer suffices.
func (c *Client) emit(e Event) {
	select {
	case c.events <- e:
	default:
	}
}

// --- live session handle ---

// setActive publishes the live login session so SendText can reach the
// connection. clearActive removes it (and rejects any in-flight iq waiters).
func (c *Client) setActive(s *session) {
	c.mu.Lock()
	c.active = s
	c.mu.Unlock()
}

func (c *Client) clearActive() {
	c.mu.Lock()
	c.active = nil
	c.mu.Unlock()
	// Fail any outstanding iq requests so SendText doesn't block past disconnect.
	c.pendingMu.Lock()
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.pendingMu.Unlock()
}

func (c *Client) activeSession() (*session, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active, c.active != nil
}

// --- iq request/response registry ---

// registerIQ allocates a delivery channel for an iq id and returns it plus an
// unregister func. The read loop matches incoming <iq type=result|error> ids
// against this map via deliverIQ.
func (c *Client) registerIQ(id string) (<-chan wire.Node, func()) {
	ch := make(chan wire.Node, 1)
	c.pendingMu.Lock()
	c.pending[id] = ch
	c.pendingMu.Unlock()
	return ch, func() {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
	}
}

// deliverIQ routes an <iq> result/error to a waiting requester. It returns true
// if the id was awaited (and thus consumed by a requester), false otherwise.
func (c *Client) deliverIQ(node wire.Node) bool {
	id := node.Attrs["id"]
	if id == "" {
		return false
	}
	c.pendingMu.Lock()
	ch, ok := c.pending[id]
	if ok {
		delete(c.pending, id)
	}
	c.pendingMu.Unlock()
	if !ok {
		return false
	}
	ch <- node
	return true
}

// dialWebSocket is the production transport: ws.Dial.
func dialWebSocket(ctx context.Context) (io.ReadWriteCloser, error) {
	return ws.Dial(ctx)
}

// handshake dials the transport, wraps it in a wire.Conn, and runs the Noise XX
// handshake using the ClientPayload produced by payloadFor(creds). It returns a
// ready-to-use nodeConn (SendNode/ReadNode/Close).
func (c *Client) handshake(ctx context.Context, creds *store.Creds, payloadFor func(*store.Creds) ([]byte, error)) (nodeConn, error) {
	rwc, err := c.dial(ctx)
	if err != nil {
		return nil, fmt.Errorf("client: dial: %w", err)
	}

	payload, err := payloadFor(creds)
	if err != nil {
		rwc.Close()
		return nil, fmt.Errorf("client: build client payload: %w", err)
	}

	eph, err := c.newEphemeral()
	if err != nil {
		rwc.Close()
		return nil, fmt.Errorf("client: ephemeral key: %w", err)
	}

	conn := wire.NewConn(rwc)
	var noisePriv, noisePub [32]byte
	copy(noisePriv[:], creds.NoiseKey.Priv)
	copy(noisePub[:], creds.NoiseKey.Pub)

	if _, err := conn.Handshake(
		eph.Priv[:], eph.Pub[:],
		noisePriv[:], noisePub[:],
		payload,
	); err != nil {
		conn.Close()
		return nil, fmt.Errorf("client: handshake: %w", err)
	}
	return conn, nil
}

// Connect runs the pairing/auth flow. It loads creds from the store; if the
// device is already registered it logs in, otherwise it runs the QR pairing
// flow and, on success, transparently reconnects to log in. Connect returns
// when the context is cancelled or a terminal error/disconnect occurs. The
// event channel is closed before Connect returns.
func (c *Client) Connect(ctx context.Context) error {
	defer close(c.events)

	creds, ok, err := c.store.LoadCreds()
	if err != nil {
		return fmt.Errorf("client: load creds: %w", err)
	}
	if !ok || creds == nil {
		// Fresh identity: generate, persist, and pair.
		id, err := keys.NewIdentity()
		if err != nil {
			return fmt.Errorf("client: new identity: %w", err)
		}
		creds = credsFromIdentity(id)
		if err := c.store.SaveCreds(creds); err != nil {
			return fmt.Errorf("client: save initial creds: %w", err)
		}
	}

	// If already registered, go straight to login; otherwise pair first.
	if creds.Registered {
		return c.runLogin(ctx, creds)
	}

	paired, err := c.runPairing(ctx, creds)
	if err != nil {
		return err
	}
	if !paired {
		return nil // context cancelled / stream ended without success
	}

	// Reload the now-registered creds and log in.
	creds, _, err = c.store.LoadCreds()
	if err != nil {
		return fmt.Errorf("client: reload creds after pairing: %w", err)
	}
	return c.runLogin(ctx, creds)
}

// ConnectWithPairingCode runs the auth flow using pairing-by-code (a.k.a. "link
// with phone number") instead of a QR scan: the user types an 8-character code,
// emitted as a PairingCodeEvent, into WhatsApp > Linked devices > Link with
// phone number. phoneNumber is the E.164 number WITHOUT a leading '+', e.g.
// "5511999998888".
//
// It mirrors Connect: if the device is already registered it logs in directly;
// otherwise it runs the pairing-code flow (companion_hello -> companion_finish
// -> pair-success) and, on success, transparently reconnects to log in. The QR
// flow (Connect/runPairing) is untouched — this is a parallel path. The event
// channel is closed before this returns.
func (c *Client) ConnectWithPairingCode(ctx context.Context, phoneNumber string) error {
	defer close(c.events)

	creds, ok, err := c.store.LoadCreds()
	if err != nil {
		return fmt.Errorf("client: load creds: %w", err)
	}
	if !ok || creds == nil {
		id, err := keys.NewIdentity()
		if err != nil {
			return fmt.Errorf("client: new identity: %w", err)
		}
		creds = credsFromIdentity(id)
		if err := c.store.SaveCreds(creds); err != nil {
			return fmt.Errorf("client: save initial creds: %w", err)
		}
	}

	if creds.Registered {
		return c.runLogin(ctx, creds)
	}

	paired, err := c.runPairingCode(ctx, creds, phoneNumber)
	if err != nil {
		return err
	}
	if !paired {
		return nil // context cancelled / stream ended without success
	}

	creds, _, err = c.store.LoadCreds()
	if err != nil {
		return fmt.Errorf("client: reload creds after pairing: %w", err)
	}
	return c.runLogin(ctx, creds)
}
