package control

import (
	"context"
	"math/rand"
	"testing"
	"time"
)

// newTestPacer builds a HumanPacer with a deterministic rand source and a fake
// clock/sleep so tests run instantly. It returns the pacer plus a pointer to the
// fake clock's current time and the recorded sleep durations.
func newTestPacer(cfg PacerConfig, seed int64) (*HumanPacer, *time.Time, *[]time.Duration) {
	p := NewHumanPacer(cfg, rand.New(rand.NewSource(seed)))
	now := time.Unix(0, 0)
	var slept []time.Duration
	p.now = func() time.Time { return now }
	p.sleep = func(ctx context.Context, d time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		slept = append(slept, d)
		now = now.Add(d) // advance the fake clock by the slept amount
		return nil
	}
	return p, &now, &slept
}

// TestWaitWithinBounds: delays are always clamped to [MinDelay, MaxDelay].
func TestWaitWithinBounds(t *testing.T) {
	cfg := DefaultPacerConfig()
	cfg.LongPauseEvery = 0 // isolate the gaussian+typing component
	p, _, _ := newTestPacer(cfg, 1)

	for i := 0; i < 1000; i++ {
		d, err := p.Wait(context.Background(), 20)
		if err != nil {
			t.Fatalf("Wait: %v", err)
		}
		if d < cfg.MinDelay || d > cfg.MaxDelay {
			t.Fatalf("delay %v out of bounds [%v,%v]", d, cfg.MinDelay, cfg.MaxDelay)
		}
	}
}

// TestWaitProportionalToLength: a longer message should, on average, take longer
// (the per-char component dominates the gaussian noise at large lengths).
func TestWaitProportionalToLength(t *testing.T) {
	cfg := DefaultPacerConfig()
	cfg.LongPauseEvery = 0
	cfg.MaxDelay = time.Hour // don't clamp; we want to see the length effect

	avg := func(textLen int) time.Duration {
		p, _, _ := newTestPacer(cfg, 99)
		var total time.Duration
		const n = 200
		for i := 0; i < n; i++ {
			d, _ := p.Wait(context.Background(), textLen)
			total += d
		}
		return total / n
	}

	short := avg(5)
	long := avg(500)
	if long <= short {
		t.Fatalf("expected longer text to take longer: short=%v long=%v", short, long)
	}
}

// TestWaitDeterministic: identical config + seed + inputs yield identical delays.
func TestWaitDeterministic(t *testing.T) {
	cfg := DefaultPacerConfig()
	p1, _, s1 := newTestPacer(cfg, 12345)
	p2, _, s2 := newTestPacer(cfg, 12345)

	for i := 0; i < 50; i++ {
		d1, _ := p1.Wait(context.Background(), i)
		d2, _ := p2.Wait(context.Background(), i)
		if d1 != d2 {
			t.Fatalf("iter %d: nondeterministic delay %v != %v", i, d1, d2)
		}
	}
	if len(*s1) != len(*s2) {
		t.Fatalf("slept slices differ in length")
	}
}

// TestWaitRespectsContextCancel: a cancelled context makes Wait return its error
// without "sleeping".
func TestWaitRespectsContextCancel(t *testing.T) {
	p, _, slept := newTestPacer(DefaultPacerConfig(), 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := p.Wait(ctx, 10)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if len(*slept) != 0 {
		t.Fatalf("expected no sleep recorded on cancel, got %v", *slept)
	}
}

// TestWaitContextCancelRealSleep verifies the production realSleep path also
// honors cancellation (returns promptly with ctx.Err()).
func TestWaitContextCancelRealSleep(t *testing.T) {
	p := NewHumanPacer(PacerConfig{
		BaseDelay: time.Hour, MinDelay: time.Hour, MaxDelay: time.Hour,
	}, rand.New(rand.NewSource(1)))
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err := p.Wait(ctx, 0)
	if err == nil {
		t.Fatal("expected ctx error")
	}
	if time.Since(start) > time.Second {
		t.Fatalf("realSleep did not return promptly on cancel")
	}
}

// TestLongPause: with LongPauseEvery=K, every K-th message gets the extra pause.
func TestLongPause(t *testing.T) {
	cfg := PacerConfig{
		BaseDelay:      time.Second,
		BaseStdDev:     1, // negligible noise
		PerCharDelay:   0,
		MinDelay:       0,
		MaxDelay:       time.Hour,
		LongPauseEvery: 5,
		LongPause:      10 * time.Second,
	}
	p, _, _ := newTestPacer(cfg, 3)

	var delays []time.Duration
	for i := 0; i < 10; i++ {
		d, _ := p.Wait(context.Background(), 0)
		delays = append(delays, d)
	}
	// count starts at 1 after the first Wait; the long pause triggers when
	// count%5==0, i.e. on the 5th and 10th messages (indices 4 and 9).
	if delays[4] < 10*time.Second {
		t.Fatalf("expected long pause on 5th message, got %v", delays[4])
	}
	if delays[9] < 10*time.Second {
		t.Fatalf("expected long pause on 10th message, got %v", delays[9])
	}
	if delays[3] >= 10*time.Second {
		t.Fatalf("unexpected long pause on 4th message: %v", delays[3])
	}
}

// TestAllowRateLimit: Allow permits exactly RateLimit sends in a window, blocks
// further ones, then frees up once the window slides past.
func TestAllowRateLimit(t *testing.T) {
	cfg := DefaultPacerConfig()
	cfg.RateLimit = 3
	cfg.RateWindow = time.Minute
	p, now, _ := newTestPacer(cfg, 1)

	for i := 0; i < 3; i++ {
		if !p.Allow() {
			t.Fatalf("send %d should be allowed", i)
		}
	}
	if p.Allow() {
		t.Fatal("4th send should be blocked by rate limit")
	}

	// Advance the fake clock past the window; the old timestamps expire.
	*now = now.Add(2 * time.Minute)
	if !p.Allow() {
		t.Fatal("send should be allowed again after window slides")
	}
}

// TestPlanTyping: composing time scales with length and SendPausedAfter is set
// for non-empty messages.
func TestPlanTyping(t *testing.T) {
	cfg := DefaultPacerConfig()
	cfg.MaxDelay = time.Hour
	p := NewHumanPacer(cfg, rand.New(rand.NewSource(1)))

	zero := p.PlanTyping(0)
	if zero.Composing != 0 || zero.SendPausedAfter {
		t.Fatalf("empty message plan unexpected: %+v", zero)
	}
	short := p.PlanTyping(10)
	long := p.PlanTyping(100)
	if long.Composing <= short.Composing {
		t.Fatalf("composing should grow with length: short=%v long=%v", short.Composing, long.Composing)
	}
	if !long.SendPausedAfter {
		t.Fatal("non-empty message should send paused after")
	}
}

// TestPacerInterface confirms HumanPacer satisfies the Pacer interface.
func TestPacerInterface(t *testing.T) {
	var _ Pacer = NewHumanPacer(DefaultPacerConfig(), nil)
}
