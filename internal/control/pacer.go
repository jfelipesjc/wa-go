package control

import (
	"context"
	"math/rand"
	"time"
)

// Pacer is the cadence policy SendText consults before transmitting. Wait blocks
// for a human-like delay (proportional to the message length plus jitter) and
// reports the delay it slept; Allow enforces a rate limit. A nil Pacer means "no
// pacing" — the historical zero-delay behavior.
type Pacer interface {
	// Wait sleeps for a computed human-like delay, honoring ctx cancellation, and
	// returns the delay it actually waited.
	Wait(ctx context.Context, textLen int) (time.Duration, error)
	// Allow reports whether a send is permitted now under the rate limit, and
	// consumes a slot if so.
	Allow() bool
}

// PacerConfig tunes HumanPacer. Zero values fall back to DefaultPacerConfig so a
// partially-filled config is still usable.
type PacerConfig struct {
	// BaseDelay is the mean fixed "reaction" delay per message.
	BaseDelay time.Duration
	// BaseStdDev is the standard deviation of the gaussian applied to BaseDelay.
	BaseStdDev time.Duration
	// PerCharDelay is added per character of the message to simulate typing
	// speed (delay grows with text length).
	PerCharDelay time.Duration
	// MinDelay / MaxDelay clamp (truncate) the gaussian so the delay never goes
	// negative or absurdly long.
	MinDelay time.Duration
	MaxDelay time.Duration

	// RateLimit is the maximum number of sends Allow permits within RateWindow.
	RateLimit int
	// RateWindow is the sliding window over which RateLimit applies.
	RateWindow time.Duration

	// LongPauseEvery triggers an extra, larger pause after this many messages
	// (0 disables it). LongPause is the size of that extra pause.
	LongPauseEvery int
	LongPause      time.Duration
}

// DefaultPacerConfig returns conservative, human-plausible defaults: ~2s base
// reaction, ~50ms/char typing, clamped to [0.5s, 30s], 15 msgs/min, and a 12s
// breather every 20 messages.
func DefaultPacerConfig() PacerConfig {
	return PacerConfig{
		BaseDelay:      2 * time.Second,
		BaseStdDev:     600 * time.Millisecond,
		PerCharDelay:   50 * time.Millisecond,
		MinDelay:       500 * time.Millisecond,
		MaxDelay:       30 * time.Second,
		RateLimit:      15,
		RateWindow:     time.Minute,
		LongPauseEvery: 20,
		LongPause:      12 * time.Second,
	}
}

// withDefaults fills any zero field from DefaultPacerConfig.
func (c PacerConfig) withDefaults() PacerConfig {
	d := DefaultPacerConfig()
	if c.BaseDelay == 0 {
		c.BaseDelay = d.BaseDelay
	}
	if c.BaseStdDev == 0 {
		c.BaseStdDev = d.BaseStdDev
	}
	if c.PerCharDelay == 0 {
		c.PerCharDelay = d.PerCharDelay
	}
	if c.MinDelay == 0 {
		c.MinDelay = d.MinDelay
	}
	if c.MaxDelay == 0 {
		c.MaxDelay = d.MaxDelay
	}
	if c.RateLimit == 0 {
		c.RateLimit = d.RateLimit
	}
	if c.RateWindow == 0 {
		c.RateWindow = d.RateWindow
	}
	// LongPauseEvery / LongPause may legitimately be 0 (disabled); leave as-is.
	return c
}

// HumanPacer is a deterministic, testable Pacer. Its randomness comes from an
// injected *rand.Rand (never the global source) and its sleeping/timekeeping go
// through injectable clock funcs, so tests run instantly and reproducibly.
type HumanPacer struct {
	cfg PacerConfig
	rnd *rand.Rand

	// now / sleep are injectable for tests. now returns the current time; sleep
	// blocks for d honoring ctx. Production uses wall-clock time and a real timer.
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error

	// sent records send timestamps within the current rate window (sliding log).
	sent []time.Time
	// count is the running number of permitted/waited messages, used for the
	// periodic long pause.
	count int
}

// NewHumanPacer builds a HumanPacer with the given config and random source. A
// nil rnd is replaced with a fixed-seed source so behavior stays deterministic
// (callers wanting variety must supply their own seeded source).
func NewHumanPacer(cfg PacerConfig, rnd *rand.Rand) *HumanPacer {
	if rnd == nil {
		rnd = rand.New(rand.NewSource(1))
	}
	return &HumanPacer{
		cfg:   cfg.withDefaults(),
		rnd:   rnd,
		now:   time.Now,
		sleep: realSleep,
	}
}

// realSleep blocks for d, returning early with ctx.Err() if ctx is cancelled.
func realSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// computeDelay returns the human-like delay for a message of textLen chars: a
// gaussian-perturbed base reaction time plus a per-character typing component,
// truncated to [MinDelay, MaxDelay], with a periodic long pause folded in.
func (p *HumanPacer) computeDelay(textLen int) time.Duration {
	// Gaussian base reaction time.
	g := p.rnd.NormFloat64()*float64(p.cfg.BaseStdDev) + float64(p.cfg.BaseDelay)
	// Per-character typing time (linear in length).
	typing := float64(textLen) * float64(p.cfg.PerCharDelay)
	d := time.Duration(g + typing)

	// Truncate to the configured bounds.
	if d < p.cfg.MinDelay {
		d = p.cfg.MinDelay
	}
	if d > p.cfg.MaxDelay {
		d = p.cfg.MaxDelay
	}

	// Periodic longer breather (count is incremented by the caller, Wait).
	if p.cfg.LongPauseEvery > 0 && p.count > 0 && p.count%p.cfg.LongPauseEvery == 0 {
		d += p.cfg.LongPause
	}
	return d
}

// Wait computes a human-like delay for a message of textLen characters, sleeps
// for it (honoring ctx), and returns the slept duration. It is deterministic for
// a given rand source + clock.
func (p *HumanPacer) Wait(ctx context.Context, textLen int) (time.Duration, error) {
	if textLen < 0 {
		textLen = 0
	}
	p.count++
	d := p.computeDelay(textLen)
	if err := p.sleep(ctx, d); err != nil {
		return d, err
	}
	return d, nil
}

// Allow implements a sliding-window rate limiter: it permits a send only if
// fewer than RateLimit sends occurred within the trailing RateWindow. On success
// it records the send timestamp and returns true; otherwise it returns false
// without recording.
func (p *HumanPacer) Allow() bool {
	now := p.now()
	cutoff := now.Add(-p.cfg.RateWindow)
	// Drop timestamps older than the window.
	kept := p.sent[:0]
	for _, ts := range p.sent {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	p.sent = kept
	if len(p.sent) >= p.cfg.RateLimit {
		return false
	}
	p.sent = append(p.sent, now)
	return true
}

// --- typing presence modeling (logic/timing only; wiring is future work) ---

// TypingPlan describes the composing-indicator choreography for one message: how
// long to show "composing" before sending, and whether to send a "paused"
// presence afterward. The integration with the wire send is intentionally left
// to a later step; only the timing model lives here.
type TypingPlan struct {
	// Composing is how long to display the "composing" (typing) indicator before
	// the message goes out.
	Composing time.Duration
	// SendPausedAfter reports whether a "paused" presence should follow the send.
	SendPausedAfter bool
}

// PlanTyping derives a TypingPlan for a message of textLen characters. The
// composing duration tracks the per-character typing component (clamped to
// MaxDelay) so longer messages "type" for longer, matching what Wait will sleep.
func (p *HumanPacer) PlanTyping(textLen int) TypingPlan {
	if textLen < 0 {
		textLen = 0
	}
	d := time.Duration(float64(textLen) * float64(p.cfg.PerCharDelay))
	if d > p.cfg.MaxDelay {
		d = p.cfg.MaxDelay
	}
	return TypingPlan{Composing: d, SendPausedAfter: textLen > 0}
}
