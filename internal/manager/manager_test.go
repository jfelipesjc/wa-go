package manager

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/felipeleal/wa-go/internal/client"
)

// --- fake session ---
//
// We test the Manager at the orchestration level via the Session interface that
// *client.Client satisfies (Connect/Events/SendText). The fake lets each test
// drive the lifecycle (emit events, return from Connect to simulate a drop)
// deterministically and offline. See the design spec for the rationale.

type fakeSession struct {
	events chan client.Event

	// connectCount counts how many times Connect was invoked (reconnect proof).
	connectCount int32

	// behavior is called on each Connect with the attempt number (1-based). It
	// should emit events and return when the "connection" ends. A nil behavior
	// emits LoggedInEvent then blocks until ctx is cancelled.
	behavior func(ctx context.Context, attempt int32, emit func(client.Event)) error

	mu     sync.Mutex
	closed bool
}

func newFakeSession() *fakeSession {
	return &fakeSession{events: make(chan client.Event, 32)}
}

func (f *fakeSession) Connect(ctx context.Context) error {
	n := atomic.AddInt32(&f.connectCount, 1)
	defer func() {
		// Mirror *client.Client: the events channel is closed when Connect returns.
		f.mu.Lock()
		if !f.closed {
			close(f.events)
			f.closed = true
		}
		f.mu.Unlock()
	}()
	emit := func(e client.Event) {
		f.mu.Lock()
		c := f.closed
		f.mu.Unlock()
		if c {
			return
		}
		select {
		case f.events <- e:
		case <-ctx.Done():
		}
	}
	if f.behavior != nil {
		return f.behavior(ctx, n, emit)
	}
	emit(client.LoggedInEvent{})
	<-ctx.Done()
	return ctx.Err()
}

// ConnectWithPairingCode mirrors Connect for the fake (the pairing-code path is
// exercised at the client layer; the manager only needs to select between them).
func (f *fakeSession) ConnectWithPairingCode(ctx context.Context, phoneNumber string) error {
	return f.Connect(ctx)
}

func (f *fakeSession) Events() <-chan client.Event { return f.events }

func (f *fakeSession) SendText(ctx context.Context, to, text string) (string, error) {
	return "msgid-" + to, nil
}

// recreatingFake produces a fresh fakeSession per Connect attempt so that the
// "events closed on return" semantics hold across reconnects (a real Client is
// rebuilt per attempt by the manager's session factory).
func recreatingFake(behavior func(ctx context.Context, attempt int32, emit func(client.Event)) error) (factory func() Session, count *int32) {
	var c int32
	return func() Session {
		fs := newFakeSession()
		fs.behavior = func(ctx context.Context, _ int32, emit func(client.Event)) error {
			return behavior(ctx, atomic.AddInt32(&c, 1), emit)
		}
		return fs
	}, &c
}

// noWaitBackoff is an injectable backoff that never actually sleeps, so reconnect
// tests are instant and deterministic.
func noWaitBackoff(attempt int) time.Duration { return 0 }

// --- Test 1: N instances all reach LoggedIn ---

func TestManager_AllReachLoggedIn(t *testing.T) {
	const n = 5
	m := New(WithBackoff(noWaitBackoff))

	for i := 0; i < n; i++ {
		fs := newFakeSession() // default behavior: emit LoggedIn then block
		name := nameFor(i)
		if _, err := m.AddSession(name, fs); err != nil {
			t.Fatalf("AddSession(%s): %v", name, err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)

	waitFor(t, func() bool {
		st := m.Status()
		if len(st) != n {
			return false
		}
		for _, s := range st {
			if s != StateLoggedIn {
				return false
			}
		}
		return true
	}, "all instances LoggedIn")

	m.Stop()
}

// --- Test: Remove stops + unregisters a single instance ---

func TestManager_RemoveOneInstance(t *testing.T) {
	m := New(WithBackoff(noWaitBackoff))
	for i := 0; i < 3; i++ {
		if _, err := m.AddSession(nameFor(i), newFakeSession()); err != nil {
			t.Fatalf("AddSession(%s): %v", nameFor(i), err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)
	waitFor(t, func() bool { return len(m.Status()) == 3 }, "3 instances up")

	// Remove the middle one; the other two stay.
	if err := m.Remove(nameFor(1)); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	waitFor(t, func() bool {
		st := m.Status()
		_, gone := st[nameFor(1)]
		return len(st) == 2 && !gone
	}, "instance removed from Status")

	// Removing an unknown instance errors.
	if err := m.Remove("ghost"); err == nil {
		t.Fatalf("Remove(ghost) = nil, want error")
	}
	m.Stop()
}

// --- Test: terminal disconnect (401/device_removed) stops the retry loop ---

func TestManager_TerminalDisconnectStopsRetry(t *testing.T) {
	var attempts int32
	factory, count := recreatingFake(func(ctx context.Context, attempt int32, emit func(client.Event)) error {
		atomic.AddInt32(&attempts, 1)
		// Simulate WhatsApp unlinking the device: emit a terminal disconnect then end.
		emit(client.DisconnectedEvent{Reason: "login failure: 401"})
		return nil
	})
	_ = count
	m := New(WithBackoff(noWaitBackoff))
	if _, err := m.AddFactory("dead", factory); err != nil {
		t.Fatalf("AddFactory: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)

	// The supervisor must stop after the terminal disconnect, not loop on 401.
	waitFor(t, func() bool { return m.Status()["dead"] == StateDisconnected }, "instance reaches terminal Disconnected")
	time.Sleep(80 * time.Millisecond) // give a transient loop a chance to (wrongly) retry
	if n := atomic.LoadInt32(&attempts); n != 1 {
		t.Fatalf("connect attempts = %d, want 1 (no retry after terminal unlink)", n)
	}
	m.Stop()
}

// --- Test 2: aggregated events carry the right instance name ---

func TestManager_AggregatedEventsHaveName(t *testing.T) {
	m := New(WithBackoff(noWaitBackoff))

	names := []string{"alpha", "beta", "gamma"}
	for _, name := range names {
		fs := newFakeSession()
		if _, err := m.AddSession(name, fs); err != nil {
			t.Fatalf("AddSession: %v", err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)

	got := map[string]bool{}
	deadline := time.After(2 * time.Second)
	for len(got) < len(names) {
		select {
		case ev, ok := <-m.Events():
			if !ok {
				t.Fatal("events channel closed early")
			}
			if _, isLogin := ev.Event.(client.LoggedInEvent); isLogin {
				got[ev.Name] = true
			}
		case <-deadline:
			t.Fatalf("timed out; got %v", got)
		}
	}
	for _, name := range names {
		if !got[name] {
			t.Errorf("no LoggedIn event for %q", name)
		}
	}
	m.Stop()
}

// --- Test 3: reconnect with backoff after a drop ---

func TestManager_ReconnectsAfterDrop(t *testing.T) {
	// First attempt: emit Disconnected and return (drop). Subsequent attempts:
	// emit LoggedIn and stay up.
	factory, count := recreatingFake(func(ctx context.Context, attempt int32, emit func(client.Event)) error {
		if attempt == 1 {
			emit(client.DisconnectedEvent{Reason: "boom"})
			return nil // Connect returns -> manager should reconnect
		}
		emit(client.LoggedInEvent{})
		<-ctx.Done()
		return ctx.Err()
	})

	var backoffCalls int32
	m := New(WithBackoff(func(attempt int) time.Duration {
		atomic.AddInt32(&backoffCalls, 1)
		return 0
	}))
	if _, err := m.AddFactory("retry", factory); err != nil {
		t.Fatalf("AddFactory: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)

	waitFor(t, func() bool { return m.Status()["retry"] == StateLoggedIn }, "reconnect to LoggedIn")

	if atomic.LoadInt32(count) < 2 {
		t.Fatalf("connect count = %d, want >= 2 (reconnect)", atomic.LoadInt32(count))
	}
	if atomic.LoadInt32(&backoffCalls) < 1 {
		t.Fatalf("backoff was never consulted; want >= 1")
	}
	m.Stop()
}

// --- Test 4: Stop() terminates everything without leaking goroutines ---

func TestManager_StopNoLeak(t *testing.T) {
	before := runtime.NumGoroutine()

	m := New(WithBackoff(noWaitBackoff))
	for i := 0; i < 10; i++ {
		if _, err := m.AddSession(nameFor(i), newFakeSession()); err != nil {
			t.Fatalf("AddSession: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.Start(ctx)
	waitFor(t, func() bool { return countState(m.Status(), StateLoggedIn) == 10 }, "all up")

	m.Stop()
	cancel()

	// Aggregated channel must be closed after Stop.
	select {
	case _, ok := <-m.Events():
		if ok {
			// Drain any buffered events until closed.
			for {
				if _, ok := <-m.Events(); !ok {
					break
				}
			}
		}
	case <-time.After(time.Second):
		t.Fatal("Events() not closed after Stop")
	}

	// Allow scheduler to reclaim goroutines.
	waitFor(t, func() bool { return runtime.NumGoroutine() <= before+2 }, "goroutines reclaimed")
}

// --- Test 5: 50 instances under -race ---

func TestManager_FiftyInstancesRace(t *testing.T) {
	const n = 50
	m := New(WithBackoff(noWaitBackoff), WithConcurrency(8))
	for i := 0; i < n; i++ {
		if _, err := m.AddSession(nameFor(i), newFakeSession()); err != nil {
			t.Fatalf("AddSession: %v", err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)

	// Concurrent readers of Status/Events while instances come up.
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = m.Status()
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			case _, ok := <-m.Events():
				if !ok {
					return
				}
			}
		}
	}()

	waitFor(t, func() bool { return countState(m.Status(), StateLoggedIn) == n }, "50 up")
	close(stop)
	m.Stop()
	wg.Wait()
}

// --- Test 6: SendText delegates to the underlying session ---

func TestManager_SendTextDelegates(t *testing.T) {
	m := New(WithBackoff(noWaitBackoff))
	fs := newFakeSession()
	if _, err := m.AddSession("s1", fs); err != nil {
		t.Fatalf("AddSession: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)
	waitFor(t, func() bool { return m.Status()["s1"] == StateLoggedIn }, "up")

	mc, ok := m.Get("s1")
	if !ok {
		t.Fatal("Get(s1) not found")
	}
	id, err := mc.SendText(ctx, "5512@s.whatsapp.net", "hi")
	if err != nil {
		t.Fatalf("SendText: %v", err)
	}
	if id != "msgid-5512@s.whatsapp.net" {
		t.Fatalf("unexpected id %q", id)
	}
	m.Stop()
}

// --- Test 7: duplicate names are rejected ---

func TestManager_DuplicateNameRejected(t *testing.T) {
	m := New()
	if _, err := m.AddSession("dup", newFakeSession()); err != nil {
		t.Fatalf("first add: %v", err)
	}
	if _, err := m.AddSession("dup", newFakeSession()); err == nil {
		t.Fatal("expected error on duplicate name")
	}
}

// --- helpers ---

func nameFor(i int) string { return "inst-" + itoa(i) }

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func countState(m map[string]State, s State) int {
	n := 0
	for _, v := range m {
		if v == s {
			n++
		}
	}
	return n
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}
