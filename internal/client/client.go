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

	"github.com/felipeleal/wa-go/internal/keys"
	"github.com/felipeleal/wa-go/internal/store"
	"github.com/felipeleal/wa-go/internal/waproto"
	"github.com/felipeleal/wa-go/internal/wire"
	"github.com/felipeleal/wa-go/internal/ws"
)

// waVersion is the WhatsApp Web client version the registration payload
// advertises. Kept in one place so it is easy to bump when WA updates.
var waVersion = waproto.WAVersion{2, 3000, 1035194821}

// browser mirrors Baileys' Browsers.ubuntu('Chrome'): {os, browser, osVersion}.
var browser = waproto.Browser{"Ubuntu", "Chrome", "22.04.4"}

// countryCode is the locale country reported in the UserAgent.
const countryCode = "US"

// Event is the sum type emitted on the Client's event channel. Exactly one of
// the concrete event types is sent per channel value.
type Event interface{ isEvent() }

// QREvent carries a freshly built pairing QR string to render/scan.
type QREvent struct{ Code string }

// PairSuccessEvent signals a successful first-time pairing. JID is the device's
// assigned WhatsApp JID ("me").
type PairSuccessEvent struct{ JID string }

// LoggedInEvent signals the server accepted a resume/login (<success>).
type LoggedInEvent struct{}

// DisconnectedEvent signals the connection ended. Reason carries a short cause.
type DisconnectedEvent struct{ Reason string }

// MessageEvent carries a decrypted incoming 1:1 text message. From is the
// sender's JID, Text the decoded body (conversation or extendedTextMessage.text)
// and ID the message id (used for the receipt).
type MessageEvent struct {
	From string
	Text string
	ID   string
}

func (QREvent) isEvent()           {}
func (PairSuccessEvent) isEvent()  {}
func (LoggedInEvent) isEvent()     {}
func (DisconnectedEvent) isEvent() {}
func (MessageEvent) isEvent()      {}

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
}

// session is the live login state SendText needs: the serialized send function
// (already guarded by loginLoop's sendMu) and the device creds.
type session struct {
	send  func(wire.Node) error
	creds *store.Creds
}

// New constructs a Client backed by the given store.
func New(s store.Store) *Client {
	return &Client{
		store:        s,
		events:       make(chan Event, 16),
		dial:         dialWebSocket,
		newEphemeral: keys.GenKeyPair,
		pending:      make(map[string]chan wire.Node),
	}
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
