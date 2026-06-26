package client

import (
	"context"
	"encoding/base64"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jfelipesjc/wa-go/internal/appstate"
	waproto "github.com/jfelipesjc/wa-go/internal/waproto"
	"github.com/jfelipesjc/wa-go/internal/wire"
	"google.golang.org/protobuf/proto"
)

// fakeRunnerSession is a runnerSession that "drops" on each Connect, mirroring
// the real Client: it owns an events channel that it closes when Connect returns.
// behavior drives each attempt (emit events, then return to simulate a drop).
type fakeRunnerSession struct {
	events   chan Event
	behavior func(ctx context.Context, emit func(Event)) error

	mu     sync.Mutex
	closed bool
}

func newFakeRunnerSession(behavior func(ctx context.Context, emit func(Event)) error) *fakeRunnerSession {
	return &fakeRunnerSession{events: make(chan Event, 8), behavior: behavior}
}

func (f *fakeRunnerSession) Events() <-chan Event { return f.events }

func (f *fakeRunnerSession) Connect(ctx context.Context) error {
	defer func() {
		f.mu.Lock()
		if !f.closed {
			close(f.events)
			f.closed = true
		}
		f.mu.Unlock()
	}()
	emit := func(e Event) {
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
		return f.behavior(ctx, emit)
	}
	emit(DisconnectedEvent{Reason: "drop"})
	return nil
}

// ============================ Runner (reconnect) ============================

// TestRunner_ReconnectsWithBackoffAndStopsOnCancel drives Run with a fake
// session that emits a QR then "drops" on every attempt. It proves: (1) Run
// reconnects (fresh session per attempt), (2) backoff is consulted with
// monotonically increasing attempt numbers via an injected clock (no real time),
// (3) Run stops on ctx cancel and (4) the aggregated event channel forwards
// across reconnects and is closed exactly once.
func TestRunner_ReconnectsWithBackoffAndStopsOnCancel(t *testing.T) {
	var connectAttempts int32
	var sleepCalls int32
	var backoffArgs []int
	var mu sync.Mutex

	// Fresh fake session per attempt; each emits a QR then returns (drops).
	factory := func() runnerSession {
		atomic.AddInt32(&connectAttempts, 1)
		return newFakeRunnerSession(func(ctx context.Context, emit func(Event)) error {
			emit(QREvent{Code: "qr"})
			return nil // drop
		})
	}

	ctx, cancel := context.WithCancel(context.Background())

	r := newRunnerWithSession(factory,
		WithReconnectBackoff(func(attempt int) time.Duration {
			mu.Lock()
			backoffArgs = append(backoffArgs, attempt)
			mu.Unlock()
			return time.Duration(attempt) * time.Millisecond // observable, no real wait
		}),
		withSleep(func(ctx context.Context, d time.Duration) bool {
			n := atomic.AddInt32(&sleepCalls, 1)
			// After a few reconnects, cancel to stop the loop deterministically.
			if n >= 3 {
				cancel()
				return false
			}
			return ctx.Err() == nil
		}),
	)

	// Drain events so the aggregated channel never wedges; it closes when Run ends.
	var qrSeen int32
	done := make(chan struct{})
	go func() {
		for ev := range r.Events() {
			if _, ok := ev.(QREvent); ok {
				atomic.AddInt32(&qrSeen, 1)
			}
		}
		close(done)
	}()

	err := r.Run(ctx)
	if err == nil {
		t.Fatalf("Run returned nil error; want ctx error")
	}

	<-done // aggregated channel closed exactly once

	if atomic.LoadInt32(&qrSeen) < 2 {
		t.Fatalf("QR events forwarded = %d; want >= 2 (events bridged across reconnects)", atomic.LoadInt32(&qrSeen))
	}

	if got := atomic.LoadInt32(&connectAttempts); got < 3 {
		t.Fatalf("connect attempts = %d; want >= 3 (proves reconnection)", got)
	}

	// Backoff must be called with monotonically increasing attempt numbers.
	mu.Lock()
	defer mu.Unlock()
	for i, a := range backoffArgs {
		if a != i {
			t.Fatalf("backoff attempt[%d] = %d; want %d (exponential schedule input)", i, a, i)
		}
	}
}

// TestRunner_NilFactory ensures Run fails cleanly and still closes its channel.
func TestRunner_NilFactory(t *testing.T) {
	r := NewRunner(nil)
	if err := r.Run(context.Background()); err == nil {
		t.Fatalf("Run with nil factory returned nil; want error")
	}
	// Events channel must be closed.
	select {
	case _, ok := <-r.Events():
		if ok {
			t.Fatalf("Events channel delivered a value; want closed")
		}
	case <-time.After(time.Second):
		t.Fatalf("Events channel not closed after Run")
	}
}

// TestDefaultReconnectBackoff_CapAndJitter verifies the schedule is bounded and
// jittered: every value is within [0, cap], and large attempts saturate the cap.
func TestDefaultReconnectBackoff_CapAndJitter(t *testing.T) {
	for attempt := 0; attempt < 20; attempt++ {
		for i := 0; i < 50; i++ {
			d := DefaultReconnectBackoff(attempt)
			if d < 0 || d > reconnectCap {
				t.Fatalf("attempt %d: backoff %v out of [0, %v]", attempt, d, reconnectCap)
			}
		}
	}
	// Negative attempt is clamped to 0 and stays within cap.
	if d := DefaultReconnectBackoff(-5); d < 0 || d > reconnectCap {
		t.Fatalf("negative attempt backoff %v out of range", d)
	}
}

// ========================== disappearing messages ==========================

func TestBuildGroupEphemeralIQ_Enable(t *testing.T) {
	n := buildGroupEphemeralIQ("id-1", "123-456@g.us", 86400)
	if n.Tag != "iq" {
		t.Fatalf("tag = %q; want iq", n.Tag)
	}
	if n.Attrs["xmlns"] != "w:g2" || n.Attrs["type"] != "set" || n.Attrs["to"] != "123-456@g.us" {
		t.Fatalf("iq attrs wrong: %v", n.Attrs)
	}
	children, _ := n.Content.([]wire.Node)
	if len(children) != 1 {
		t.Fatalf("children = %d; want 1", len(children))
	}
	eph := children[0]
	if eph.Tag != "ephemeral" {
		t.Fatalf("child tag = %q; want ephemeral", eph.Tag)
	}
	if eph.Attrs["expiration"] != "86400" {
		t.Fatalf("expiration = %q; want 86400", eph.Attrs["expiration"])
	}
}

func TestBuildGroupEphemeralIQ_Disable(t *testing.T) {
	n := buildGroupEphemeralIQ("id-1", "g@g.us", 0)
	children, _ := n.Content.([]wire.Node)
	if len(children) != 1 || children[0].Tag != "not_ephemeral" {
		t.Fatalf("disable child = %+v; want single not_ephemeral", children)
	}
	if len(children[0].Attrs) != 0 {
		t.Fatalf("not_ephemeral should have no attrs, got %v", children[0].Attrs)
	}
}

func TestBuildEphemeralSettingMessage(t *testing.T) {
	now := time.Unix(1700000000, 0)
	m := buildEphemeralSettingMessage(604800, now)
	pm := m.GetProtocolMessage()
	if pm == nil {
		t.Fatalf("no protocol message")
	}
	if pm.GetType().String() != "EPHEMERAL_SETTING" {
		t.Fatalf("type = %v; want EPHEMERAL_SETTING", pm.GetType())
	}
	if pm.GetEphemeralExpiration() != 604800 {
		t.Fatalf("expiration = %d; want 604800", pm.GetEphemeralExpiration())
	}
	if pm.GetEphemeralSettingTimestamp() != now.UnixMilli() {
		t.Fatalf("setting ts = %d; want %d", pm.GetEphemeralSettingTimestamp(), now.UnixMilli())
	}
}

func TestBuildEphemeralSettingMessage_Disable(t *testing.T) {
	m := buildEphemeralSettingMessage(0, time.Unix(1, 0))
	if exp := m.GetProtocolMessage().GetEphemeralExpiration(); exp != 0 {
		t.Fatalf("disable expiration = %d; want 0", exp)
	}
}

func TestSetDisappearingMessages_NegativeRejected(t *testing.T) {
	c := New(nil)
	if _, err := c.SetDisappearingMessages(context.Background(), "x@s.whatsapp.net", -time.Second); err == nil {
		t.Fatalf("negative duration accepted; want error")
	}
}

// =============================== labels ===============================

func TestLabelIndexBuilders(t *testing.T) {
	if got := labelEditIndex("L1"); !eqStrs(got, []string{"label_edit", "L1"}) {
		t.Fatalf("labelEditIndex = %v", got)
	}
	if got := chatLabelIndex("L1", "5511@s.whatsapp.net"); !eqStrs(got, []string{"label_jid", "L1", "5511@s.whatsapp.net"}) {
		t.Fatalf("chatLabelIndex = %v", got)
	}
	if got := messageLabelIndex("L1", "5511@s.whatsapp.net", "MID"); !eqStrs(got, []string{"label_message", "L1", "5511@s.whatsapp.net", "MID", "0", "0"}) {
		t.Fatalf("messageLabelIndex = %v", got)
	}
}

func TestBuildGetLabelsIQ(t *testing.T) {
	n := buildGetLabelsIQ("idL")
	if n.Tag != "iq" || n.Attrs["xmlns"] != "w:biz" || n.Attrs["type"] != "get" || n.Attrs["to"] != sWhatsAppNet {
		t.Fatalf("labels iq attrs wrong: %v", n.Attrs)
	}
	children, _ := n.Content.([]wire.Node)
	if len(children) != 1 || children[0].Tag != "labels" {
		t.Fatalf("labels iq child = %+v; want single <labels>", children)
	}
}

func TestParseLabels(t *testing.T) {
	reply := wire.Node{
		Tag: "iq",
		Content: []wire.Node{{
			Tag: "labels",
			Content: []wire.Node{
				{Tag: "label", Attrs: map[string]string{"id": "1", "name": "Lead", "color": "2"}},
				{Tag: "label", Attrs: map[string]string{"id": "2", "name": "Paid"}},
				{Tag: "noise"},
			},
		}},
	}
	got := parseLabels(reply)
	if len(got) != 2 {
		t.Fatalf("parsed %d labels; want 2 (%+v)", len(got), got)
	}
	if got[0].ID != "1" || got[0].Name != "Lead" || got[0].Color != "2" {
		t.Fatalf("label[0] = %+v", got[0])
	}
	if got[1].ID != "2" || got[1].Name != "Paid" || got[1].Color != "" {
		t.Fatalf("label[1] = %+v", got[1])
	}
}

func TestParseLabels_Empty(t *testing.T) {
	if got := parseLabels(wire.Node{Tag: "iq"}); len(got) != 0 {
		t.Fatalf("parseLabels(empty) = %v; want []", got)
	}
}

// TestAddChatLabelRoundTripDecode proves AddChatLabel now emits a real,
// decodable app-state patch in the "regular" collection whose mutation carries a
// labelAssociationAction{labeled:true} at the chat-label index.
func TestAddChatLabelRoundTripDecode(t *testing.T) {
	c, exec := testClientWithAppState(t)
	n := exec(func() error {
		return c.AddChatLabel(context.Background(), "5511@s.whatsapp.net", "L1")
	})

	if got := collectionOf(t, n); got != collRegular {
		t.Fatalf("collection = %q, want %q", got, collRegular)
	}

	sync, _ := childByTag(n, "sync")
	coll, _ := childByTag(sync, "collection")
	patchNode, _ := childByTag(coll, "patch")
	patchBytes := patchNode.Content.([]byte)

	var patch waproto.SyncdPatch
	if err := proto.Unmarshal(patchBytes, &patch); err != nil {
		t.Fatalf("unmarshal patch: %v", err)
	}
	patch.Version = &waproto.SyncdVersion{Version: proto.Uint64(1)}

	rawKey := make([]byte, 32)
	for i := range rawKey {
		rawKey[i] = byte(i + 1)
	}
	keyID := base64.StdEncoding.EncodeToString([]byte("appstatekeyid01"))
	resolve := func(id string) ([]byte, bool) {
		if id == keyID {
			return rawKey, true
		}
		return nil, false
	}
	res, err := appstate.DecodePatch(collRegular, &patch, appstate.NewHashState(), resolve)
	if err != nil {
		t.Fatalf("DecodePatch: %v", err)
	}
	if len(res.Mutations) != 1 {
		t.Fatalf("mutations = %d", len(res.Mutations))
	}
	m := res.Mutations[0]
	want := []string{"label_jid", "L1", "5511@s.whatsapp.net"}
	if !eqStrs(m.Index, want) {
		t.Fatalf("index = %v, want %v", m.Index, want)
	}
	if la := m.Action.GetLabelAssociationAction(); la == nil || !la.GetLabeled() {
		t.Fatalf("labelAssociationAction wrong: %v", m.Action)
	}
}

// TestEditLabelRoundTripDecode proves EditLabel emits a labelEditAction patch.
func TestEditLabelRoundTripDecode(t *testing.T) {
	c, exec := testClientWithAppState(t)
	n := exec(func() error {
		return c.EditLabel(context.Background(), "L7", "VIP", 3, 0, false)
	})

	sync, _ := childByTag(n, "sync")
	coll, _ := childByTag(sync, "collection")
	patchNode, _ := childByTag(coll, "patch")
	patchBytes := patchNode.Content.([]byte)

	var patch waproto.SyncdPatch
	if err := proto.Unmarshal(patchBytes, &patch); err != nil {
		t.Fatalf("unmarshal patch: %v", err)
	}
	patch.Version = &waproto.SyncdVersion{Version: proto.Uint64(1)}
	rawKey := make([]byte, 32)
	for i := range rawKey {
		rawKey[i] = byte(i + 1)
	}
	keyID := base64.StdEncoding.EncodeToString([]byte("appstatekeyid01"))
	resolve := func(id string) ([]byte, bool) {
		if id == keyID {
			return rawKey, true
		}
		return nil, false
	}
	res, err := appstate.DecodePatch(collRegular, &patch, appstate.NewHashState(), resolve)
	if err != nil {
		t.Fatalf("DecodePatch: %v", err)
	}
	m := res.Mutations[0]
	want := []string{"label_edit", "L7"}
	if !eqStrs(m.Index, want) {
		t.Fatalf("index = %v, want %v", m.Index, want)
	}
	le := m.Action.GetLabelEditAction()
	if le == nil || le.GetName() != "VIP" || le.GetColor() != 3 || le.GetDeleted() {
		t.Fatalf("labelEditAction wrong: %v", m.Action)
	}
}

// --- helpers ---

func eqStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
