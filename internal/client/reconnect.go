package client

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"time"
)

// errNoFactory is returned by Run when the Runner was built with a nil factory.
var errNoFactory = errors.New("client: Runner requires a non-nil client factory")

// Auto-reconnection for a standalone Client.
//
// Connect(ctx) returns when the connection ends (ctx cancel, disconnect, or a
// terminal error) and — crucially — closes the Client's Events() channel before
// returning. A single *Client therefore cannot be reconnected: its event channel
// is already closed. This mirrors the internal/manager design, whose supervisor
// builds a FRESH client.Client per attempt from a factory.
//
// Runner applies the same pattern to a lone Client without pulling in the whole
// Manager: it loops calling Connect on a fresh Client produced by a factory, with
// exponential backoff + full jitter between attempts (capped ~60s), forwarding
// every attempt's events to a single, Runner-owned aggregated channel. The
// aggregated channel stays open across reconnects and is closed exactly once when
// Run returns (ctx cancelled).
//
// Event channel handling: because each underlying Connect closes its own
// Events() channel on return, Runner pumps each attempt's events into its own
// long-lived channel. Consumers read Runner.Events() and never observe the
// per-attempt channel closing — they just see a continuous stream punctuated by
// DisconnectedEvent / QREvent / LoggedInEvent across reconnects.

// runnerSession is the slice of *Client that Runner drives. *Client satisfies it.
type runnerSession interface {
	Connect(ctx context.Context) error
	Events() <-chan Event
}

// Compile-time guarantee that *Client satisfies runnerSession.
var _ runnerSession = (*Client)(nil)

// ReconnectBackoff maps a 0-based attempt number to a wait duration. The default
// is DefaultReconnectBackoff (exponential with full jitter, capped at 60s).
type ReconnectBackoff func(attempt int) time.Duration

// reconnectCap bounds the backoff schedule.
const reconnectCap = 60 * time.Second

// DefaultReconnectBackoff is the production reconnect schedule: 1s, 2s, 4s, …
// capped at 60s, with full jitter (random in [0, base]). It matches
// manager.DefaultBackoff so a single Client and the Manager behave the same.
func DefaultReconnectBackoff(attempt int) time.Duration {
	const base = time.Second
	if attempt < 0 {
		attempt = 0
	}
	shift := attempt
	if shift > 6 {
		shift = 6
	}
	d := base << uint(shift) // 1s..64s
	if d > reconnectCap || d <= 0 {
		d = reconnectCap
	}
	// Full jitter: random in [0, d].
	return time.Duration(rand.Int63n(int64(d) + 1))
}

// Runner supervises a single logical Client across reconnections. Construct it
// with NewRunner; drive it with Run; consume events from Events.
type Runner struct {
	// factory produces a fresh session per attempt. The public NewRunner wraps a
	// func() *Client; tests can inject any runnerSession via newRunnerWithSession.
	factory func() runnerSession
	backoff ReconnectBackoff

	// sleep waits d or until ctx is done, returning false if ctx was cancelled.
	// Injectable so tests can drive backoff without real time.
	sleep func(ctx context.Context, d time.Duration) bool

	events    chan Event
	closeOnce sync.Once

	mu   sync.Mutex
	live *Client // current attempt's Client (nil if a non-*Client factory is used)
}

// RunnerOption configures a Runner.
type RunnerOption func(*Runner)

// WithReconnectBackoff overrides the reconnect backoff schedule.
func WithReconnectBackoff(b ReconnectBackoff) RunnerOption {
	return func(r *Runner) {
		if b != nil {
			r.backoff = b
		}
	}
}

// withSleep overrides the wait primitive (test-only injection point).
func withSleep(s func(ctx context.Context, d time.Duration) bool) RunnerOption {
	return func(r *Runner) {
		if s != nil {
			r.sleep = s
		}
	}
}

// NewRunner builds a Runner that produces a fresh *Client per connection attempt
// via factory. The factory MUST return a new Client each call (e.g. client.New(store))
// because Connect closes a Client's Events() channel on return. A nil factory
// makes Run a no-op error.
func NewRunner(factory func() *Client, opts ...RunnerOption) *Runner {
	var adapted func() runnerSession
	if factory != nil {
		adapted = func() runnerSession { return factory() }
	}
	return newRunnerWithSession(adapted, opts...)
}

// newRunnerWithSession is the internal constructor taking an interface factory so
// tests can drive Run with a fake runnerSession (no real Client / handshake).
func newRunnerWithSession(factory func() runnerSession, opts ...RunnerOption) *Runner {
	r := &Runner{
		factory: factory,
		backoff: DefaultReconnectBackoff,
		events:  make(chan Event, 16),
	}
	r.sleep = r.defaultSleep
	for _, o := range opts {
		o(r)
	}
	return r
}

// Run drives the connect/backoff loop until ctx is cancelled. It blocks. The
// aggregated Events channel is closed exactly once when Run returns. Run returns
// ctx.Err() (context.Canceled / DeadlineExceeded) on shutdown.
func (r *Runner) Run(ctx context.Context) error {
	defer r.closeOnce.Do(func() { close(r.events) })

	if r.factory == nil {
		return errNoFactory
	}

	attempt := 0
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		sess := r.factory()
		if cc, ok := sess.(*Client); ok {
			r.setLive(cc)
		}

		// Pump this attempt's events into the aggregated channel until Connect's
		// per-attempt Events() channel closes.
		pumpDone := make(chan struct{})
		go r.pump(ctx, sess.Events(), pumpDone)

		// Connect blocks until the connection ends; it closes Events() on return.
		_ = sess.Connect(ctx)
		<-pumpDone // drain the just-closed per-attempt channel fully
		r.setLive(nil)

		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Connection dropped while ctx is still live: back off, then retry.
		wait := r.backoff(attempt)
		attempt++
		if !r.sleep(ctx, wait) {
			return ctx.Err()
		}
	}
}

// pump forwards one attempt's events to the aggregated channel until the
// per-attempt channel closes or ctx is cancelled. It never blocks shutdown: a
// cancelled ctx makes forwarding give up.
func (r *Runner) pump(ctx context.Context, src <-chan Event, done chan struct{}) {
	defer close(done)
	for {
		select {
		case ev, ok := <-src:
			if !ok {
				return
			}
			select {
			case r.events <- ev:
			case <-ctx.Done():
				// keep draining src (it will close when Connect returns) but stop
				// forwarding; avoids wedging on a slow/absent consumer at shutdown.
			}
		case <-ctx.Done():
			// Drain the rest non-blockingly so the goroutine exits once src closes.
			for {
				select {
				case _, ok := <-src:
					if !ok {
						return
					}
				default:
					return
				}
			}
		}
	}
}

// Events returns the aggregated event stream spanning all reconnect attempts. It
// is closed once Run returns.
func (r *Runner) Events() <-chan Event { return r.events }

// Live returns the Client for the current connection attempt, or nil if no
// attempt is currently connected (between attempts / before Run). Use it to send
// on the live session; it changes across reconnects.
func (r *Runner) Live() *Client {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.live
}

func (r *Runner) setLive(c *Client) {
	r.mu.Lock()
	r.live = c
	r.mu.Unlock()
}

// defaultSleep waits d or until ctx is done; returns false if ctx was cancelled.
func (r *Runner) defaultSleep(ctx context.Context, d time.Duration) bool {
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
