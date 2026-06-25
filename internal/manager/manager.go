// Package manager runs N WhatsApp sessions (each a client.Client with its own
// store) concurrently inside one process, with per-instance supervision,
// exponential-backoff reconnection with jitter, and an aggregated event stream
// tagged by instance name.
//
// Design (see docs/superpowers/specs/2026-06-22-instance-manager-design.md):
//
// The Manager depends on a minimal Session interface (Connect/Events/SendText)
// that *client.Client satisfies. Each managed instance owns a session factory
// that produces a FRESH Session per connection attempt — this mirrors the real
// client, whose Events() channel is closed when Connect returns, so a reconnect
// needs a new client. Tests inject a trivial fake Session through the same
// factory, exercising the supervision/backoff/aggregation logic fully offline
// without the Noise handshake.
//
// Concurrency model:
//   - Start launches one supervisor goroutine per instance, gated by a semaphore
//     (WithConcurrency) bounding how many connect at once.
//   - Each supervisor loops: build session -> Connect -> on return, if ctx is
//     live, sleep backoff(attempt) and retry. Per-attempt events are forwarded
//     to the aggregated channel, tagged with the instance name, and used to
//     derive the instance State.
//   - Stop cancels the root context and waits for every supervisor (and the
//     aggregation pump) to exit, then closes the aggregated channel exactly once.
package manager

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/felipeleal/wa-go/internal/client"
	"github.com/felipeleal/wa-go/internal/store"
)

// Session is the minimal slice of *client.Client the Manager drives. *client.Client
// satisfies it directly. Connect blocks until the connection ends (ctx cancel or
// disconnect) and closes Events() before returning.
type Session interface {
	Connect(ctx context.Context) error
	ConnectWithPairingCode(ctx context.Context, phoneNumber string) error
	Events() <-chan client.Event
	SendText(ctx context.Context, to, text string) (string, error)
}

// Compile-time guarantee that the real client satisfies Session.
var _ Session = (*client.Client)(nil)

// State is the supervised lifecycle state of one instance, derived from events.
type State int

const (
	// StateIdle is the state before Start (registered, not yet running).
	StateIdle State = iota
	// StateConnecting: a connection attempt is in progress (no events yet).
	StateConnecting
	// StateConnected: handshake done, pairing/login in progress (saw a QR, etc).
	StateConnected
	// StateLoggedIn: server accepted login (LoggedInEvent).
	StateLoggedIn
	// StateDisconnected: the connection ended.
	StateDisconnected
	// StateBackoff: waiting before the next reconnect attempt.
	StateBackoff
)

func (s State) String() string {
	switch s {
	case StateIdle:
		return "Idle"
	case StateConnecting:
		return "Connecting"
	case StateConnected:
		return "Connected"
	case StateLoggedIn:
		return "LoggedIn"
	case StateDisconnected:
		return "Disconnected"
	case StateBackoff:
		return "Backoff"
	default:
		return "Unknown"
	}
}

// InstanceEvent is a client.Event tagged with the instance it came from.
type InstanceEvent struct {
	Name  string
	Event client.Event
}

// Instance is the registration config for one supervised session.
type Instance struct {
	Name  string
	Store store.Store // optional; the source for the default factory

	factory func() Session
}

// ManagedClient is the live handle for one instance, exposing delegating methods.
type ManagedClient struct {
	name string
	m    *Manager
}

// Name returns the instance name.
func (mc *ManagedClient) Name() string { return mc.name }

// SendText delegates to the instance's current live session. It errors if the
// instance has no active session (not yet logged in / disconnected).
func (mc *ManagedClient) SendText(ctx context.Context, to, text string) (string, error) {
	sess := mc.m.liveSession(mc.name)
	if sess == nil {
		return "", fmt.Errorf("manager: instance %q has no active session", mc.name)
	}
	return sess.SendText(ctx, to, text)
}

// Client returns the instance's current live *client.Client, exposing the full
// client API (SendImage/SendReaction/OnWhatsApp/group methods/…) beyond the
// SendText shortcut. It returns (nil,false) when the instance is offline or when
// the live session is not a real client (e.g. a test fake). Callers must not
// retain the pointer across reconnects — fetch it fresh per operation, since the
// manager rebuilds the client on every reconnection.
func (mc *ManagedClient) Client() (*client.Client, bool) {
	sess := mc.m.liveSession(mc.name)
	if sess == nil {
		return nil, false
	}
	c, ok := sess.(*client.Client)
	return c, ok
}

// BackoffFunc maps a 0-based attempt number to a wait duration.
type BackoffFunc func(attempt int) time.Duration

// Manager supervises a set of named instances.
type Manager struct {
	backoff     BackoffFunc
	concurrency int

	mu        sync.Mutex
	instances map[string]*managed
	started   bool

	events    chan InstanceEvent
	cancel    context.CancelFunc
	ctxRef    context.Context // root context once Start ran (for late Add)
	sem       chan struct{}   // concurrency cap (nil = unbounded)
	wg        sync.WaitGroup  // supervisors
	closeOnce sync.Once
}

// managed is the Manager's internal per-instance runtime state.
type managed struct {
	name       string
	factory    func() Session
	cancel     context.CancelFunc // stops this instance's supervisor (set at launch)
	pairNumber string             // if set, connect via pairing code for this number (not QR)

	mu       sync.Mutex
	state    State
	live     Session // current connected session (for SendText), nil otherwise
	terminal bool    // set when the account was unlinked (401/device_removed): stop retrying
}

func (mg *managed) setCancel(c context.CancelFunc) {
	mg.mu.Lock()
	mg.cancel = c
	mg.mu.Unlock()
}

func (mg *managed) getCancel() context.CancelFunc {
	mg.mu.Lock()
	defer mg.mu.Unlock()
	return mg.cancel
}

func (mg *managed) setTerminal(v bool) {
	mg.mu.Lock()
	mg.terminal = v
	mg.mu.Unlock()
}

func (mg *managed) isTerminal() bool {
	mg.mu.Lock()
	defer mg.mu.Unlock()
	return mg.terminal
}

// isTerminalDisconnect reports whether a DisconnectedEvent reason means the
// account was unlinked by WhatsApp (device removed / login failure / replaced) —
// a permanent state where reconnecting just loops on 401, so the supervisor must
// stop and the instance needs a fresh pair.
func isTerminalDisconnect(reason string) bool {
	// Only genuinely terminal signals: device unlinked / logged out / auth-rejected
	// (401/403). Generic "login failure: <code>" with transient codes (e.g. 503
	// service-unavailable) must fall through to backoff/retry, not stop forever.
	for _, s := range []string{"device_removed", "loggedOut", "logged out", "failure: 401", "failure: 403"} {
		if strings.Contains(reason, s) {
			return true
		}
	}
	return false
}

func (mg *managed) setState(s State) {
	mg.mu.Lock()
	mg.state = s
	mg.mu.Unlock()
}

func (mg *managed) getState() State {
	mg.mu.Lock()
	defer mg.mu.Unlock()
	return mg.state
}

func (mg *managed) setLive(s Session) {
	mg.mu.Lock()
	mg.live = s
	mg.mu.Unlock()
}

func (mg *managed) getLive() Session {
	mg.mu.Lock()
	defer mg.mu.Unlock()
	return mg.live
}

// Option configures a Manager.
type Option func(*Manager)

// WithBackoff overrides the reconnect backoff schedule (default: exponential
// 1s,2s,4s… capped at 60s with full jitter).
func WithBackoff(f BackoffFunc) Option {
	return func(m *Manager) {
		if f != nil {
			m.backoff = f
		}
	}
}

// WithConcurrency bounds how many instances connect at the same time (default 16).
func WithConcurrency(n int) Option {
	return func(m *Manager) {
		if n > 0 {
			m.concurrency = n
		}
	}
}

// DefaultBackoff is the production reconnect schedule: 1s, 2s, 4s, … capped at
// 60s, with full jitter (random in [0, base]).
func DefaultBackoff(attempt int) time.Duration {
	const base = time.Second
	const cap = 60 * time.Second
	d := base << uint(min(attempt, 6)) // 1s..64s
	if d > cap || d <= 0 {
		d = cap
	}
	// Full jitter: random in [0, d].
	return time.Duration(rand.Int63n(int64(d) + 1))
}

// New constructs a Manager with the given options.
func New(opts ...Option) *Manager {
	m := &Manager{
		backoff:     DefaultBackoff,
		concurrency: 16,
		instances:   make(map[string]*managed),
		events:      make(chan InstanceEvent, 64),
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

// Events returns the aggregated, name-tagged event stream for all instances. It
// is closed once Stop completes.
func (m *Manager) Events() <-chan InstanceEvent { return m.events }

// Add registers an instance backed by a ready store.Store. A *client.Client is
// built per connection attempt from the store (so reconnects get a fresh client).
func (m *Manager) Add(name string, st store.Store) (*ManagedClient, error) {
	if st == nil {
		return nil, errors.New("manager: nil store")
	}
	return m.AddFactory(name, func() Session { return client.New(st) })
}

// AddPaired is like Add but pairs via PAIRING CODE for the given phone number
// (instead of QR): the supervisor connects with ConnectWithPairingCode, which
// emits a PairingCodeEvent carrying the 8-char code to type on the phone. Once
// the instance is registered, subsequent reconnects just log in (the number is
// then a no-op). number should be the full international number, digits only.
func (m *Manager) AddPaired(name string, st store.Store, number string) (*ManagedClient, error) {
	if st == nil {
		return nil, errors.New("manager: nil store")
	}
	return m.addFactory(name, func() Session { return client.New(st) }, number)
}

// AddSession registers an instance backed by a fixed Session (used in tests and
// for one-shot sessions; a single dropped Connect cannot be re-created from it,
// so prefer Add/AddFactory for reconnection).
func (m *Manager) AddSession(name string, s Session) (*ManagedClient, error) {
	if s == nil {
		return nil, errors.New("manager: nil session")
	}
	return m.AddFactory(name, func() Session { return s })
}

// AddFactory registers an instance whose Session is produced fresh per attempt.
func (m *Manager) AddFactory(name string, factory func() Session) (*ManagedClient, error) {
	return m.addFactory(name, factory, "")
}

// addFactory is the shared registration core. pairNumber != "" makes the
// supervisor connect via pairing code for that number instead of QR.
func (m *Manager) addFactory(name string, factory func() Session, pairNumber string) (*ManagedClient, error) {
	if name == "" {
		return nil, errors.New("manager: empty instance name")
	}
	if factory == nil {
		return nil, errors.New("manager: nil factory")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.instances[name]; exists {
		return nil, fmt.Errorf("manager: instance %q already registered", name)
	}
	mg := &managed{name: name, factory: factory, state: StateIdle, pairNumber: pairNumber}
	m.instances[name] = mg
	mc := &ManagedClient{name: name, m: m}
	// If Start already ran, launch the new instance immediately.
	if m.started && m.cancel != nil {
		m.launch(mg)
	}
	return mc, nil
}

// Remove stops and unregisters a single instance: it cancels that instance's
// supervisor (which ends its connection and event pump) and drops it from the
// registry, so it no longer appears in Status and frees its resources. Returns
// an error if the instance is unknown. The instance's Store is owned by the
// caller (the API backend closes it after Remove).
func (m *Manager) Remove(name string) error {
	m.mu.Lock()
	mg, ok := m.instances[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("manager: instance %q not found", name)
	}
	delete(m.instances, name)
	m.mu.Unlock()
	if cancel := mg.getCancel(); cancel != nil {
		cancel() // supervise sees ctx.Done(), closes the session, returns
	}
	return nil
}

// Start launches every registered instance concurrently under ctx. It returns
// immediately; supervisors run in the background until ctx is cancelled or Stop.
func (m *Manager) Start(ctx context.Context) {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return
	}
	root, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	m.ctxRef = root
	m.started = true
	if m.sem == nil && m.concurrency > 0 {
		m.sem = make(chan struct{}, m.concurrency)
	}
	insts := make([]*managed, 0, len(m.instances))
	for _, mg := range m.instances {
		insts = append(insts, mg)
	}
	m.mu.Unlock()

	for _, mg := range insts {
		m.launch(mg)
	}
}

// launch starts an instance's supervisor under a per-instance context derived
// from the manager root, recording the cancel func so Remove can stop just this
// instance. Caller must hold no lock that conflicts with mg/m mutation; it is
// invoked from Start (after unlock) and AddFactory (under m.mu). m.ctxRef must be
// set (m.started true).
func (m *Manager) launch(mg *managed) {
	ctx, cancel := context.WithCancel(m.ctxRef)
	mg.setCancel(cancel) // guarded by mg.mu so Remove's read is race-free
	m.wg.Add(1)
	go m.supervise(ctx, mg)
}

// supervise runs the connect/backoff loop for one instance until ctx is done.
func (m *Manager) supervise(ctx context.Context, mg *managed) {
	defer m.wg.Done()

	// Acquire a concurrency slot for the initial connect; released once the
	// instance reaches a steady state (logged in or after first attempt) so
	// other instances can connect. We hold it only for the connecting window.
	attempt := 0
	for {
		if ctx.Err() != nil {
			mg.setState(StateDisconnected)
			return
		}

		mg.setState(StateConnecting)
		m.acquire(ctx)
		sess := mg.factory()
		mg.setLive(sess)

		// Pump this attempt's events into the aggregated channel and update state.
		// settled fires once the instance reaches a steady state (logged in /
		// connected) so the concurrency slot is released for the next instance;
		// the slot bounds the connecting window, not the whole connection.
		pumpDone := make(chan struct{})
		settled := make(chan struct{})
		go m.pump(ctx, mg, sess, pumpDone, settled)

		// Release the concurrency slot once settled (or the attempt ends).
		var releaseOnce sync.Once
		release := func() { releaseOnce.Do(m.release) }
		go func() {
			select {
			case <-settled:
			case <-pumpDone:
			case <-ctx.Done():
			}
			release()
		}()

		// Connect blocks until the connection ends. When a pairing number is set we
		// connect via pairing code (emits a PairingCodeEvent with the 8-char code);
		// ConnectWithPairingCode logs in normally once the instance is registered,
		// so reconnects after pairing are unaffected.
		if mg.pairNumber != "" {
			_ = sess.ConnectWithPairingCode(ctx, mg.pairNumber)
		} else {
			_ = sess.Connect(ctx)
		}
		<-pumpDone // session's Events() is closed by Connect; pump has drained it
		release()  // ensure released even if never settled (e.g. immediate drop)
		mg.setLive(nil)

		if ctx.Err() != nil {
			mg.setState(StateDisconnected)
			return
		}

		// Account was unlinked (device removed / 401): stop — retrying just loops
		// on 401. The instance stays registered as disconnected; a fresh pair
		// (delete + recreate, or a re-pair) is required.
		if mg.isTerminal() {
			mg.setState(StateDisconnected)
			return
		}

		// Connection dropped: back off, then retry.
		mg.setState(StateBackoff)
		wait := m.backoff(attempt)
		attempt++
		if !m.sleep(ctx, wait) {
			mg.setState(StateDisconnected)
			return
		}
	}
}

// pump forwards a single attempt's events to the aggregated channel, tagged with
// the instance name, and updates the derived State. It returns when the session's
// Events() channel closes (Connect returned) or ctx is cancelled.
func (m *Manager) pump(ctx context.Context, mg *managed, sess Session, done, settled chan struct{}) {
	defer close(done)
	var settledOnce sync.Once
	markSettled := func() { settledOnce.Do(func() { close(settled) }) }
	ch := sess.Events()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return
			}
			next := stateFromEvent(ev, mg.getState())
			mg.setState(next)
			if next == StateConnected || next == StateLoggedIn {
				markSettled()
			}
			// A login-failure / device-removed disconnect is terminal: flag it so
			// supervise stops retrying instead of looping on 401.
			if d, ok := ev.(client.DisconnectedEvent); ok && isTerminalDisconnect(d.Reason) {
				mg.setTerminal(true)
			}
			m.forward(ctx, InstanceEvent{Name: mg.name, Event: ev})
		case <-ctx.Done():
			// Drain remaining buffered events without blocking, then exit.
			for {
				select {
				case ev, ok := <-ch:
					if !ok {
						return
					}
					mg.setState(stateFromEvent(ev, mg.getState()))
				default:
					return
				}
			}
		}
	}
}

// forward sends an aggregated event without blocking shutdown: it gives up if
// ctx is cancelled (Stop) so a slow/absent consumer cannot wedge supervisors.
func (m *Manager) forward(ctx context.Context, ie InstanceEvent) {
	select {
	case m.events <- ie:
	case <-ctx.Done():
	}
}

// stateFromEvent derives the next State from an event and the current state.
func stateFromEvent(ev client.Event, cur State) State {
	switch ev.(type) {
	case client.QREvent, client.PairSuccessEvent:
		return StateConnected
	case client.LoggedInEvent:
		return StateLoggedIn
	case client.DisconnectedEvent:
		return StateDisconnected
	default:
		// MessageEvent and others don't change connectivity state.
		if cur == StateConnecting {
			return StateConnected
		}
		return cur
	}
}

// acquire/release implement the concurrency cap (no-op if unbounded).
func (m *Manager) acquire(ctx context.Context) {
	if m.sem == nil {
		return
	}
	select {
	case m.sem <- struct{}{}:
	case <-ctx.Done():
	}
}

func (m *Manager) release() {
	if m.sem == nil {
		return
	}
	select {
	case <-m.sem:
	default:
	}
}

// sleep waits d or until ctx is done; returns false if ctx was cancelled.
func (m *Manager) sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// Stop cancels all supervisors, waits for them to exit, and closes the
// aggregated event channel exactly once.
func (m *Manager) Stop() {
	m.mu.Lock()
	cancel := m.cancel
	started := m.started
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if started {
		m.wg.Wait()
	}
	m.closeOnce.Do(func() { close(m.events) })
}

// Get returns the ManagedClient for a name.
func (m *Manager) Get(name string) (*ManagedClient, bool) {
	m.mu.Lock()
	_, ok := m.instances[name]
	m.mu.Unlock()
	if !ok {
		return nil, false
	}
	return &ManagedClient{name: name, m: m}, true
}

// Status returns a snapshot of every instance's derived State.
func (m *Manager) Status() map[string]State {
	m.mu.Lock()
	out := make(map[string]State, len(m.instances))
	insts := make([]*managed, 0, len(m.instances))
	for _, mg := range m.instances {
		insts = append(insts, mg)
	}
	m.mu.Unlock()
	for _, mg := range insts {
		out[mg.name] = mg.getState()
	}
	return out
}

// liveSession returns the current live Session for an instance (for SendText).
func (m *Manager) liveSession(name string) Session {
	m.mu.Lock()
	mg, ok := m.instances[name]
	m.mu.Unlock()
	if !ok {
		return nil
	}
	return mg.getLive()
}
