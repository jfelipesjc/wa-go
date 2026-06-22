package client

import (
	"context"
	"testing"
	"time"

	"github.com/felipeleal/wa-go/internal/store"
	"github.com/felipeleal/wa-go/internal/wire"
)

// drainFor collects events for up to d, returning the first event of the matching
// kind (matched by the predicate), or nil on timeout.
func waitEvent(t *testing.T, c *Client, d time.Duration, match func(Event) bool) Event {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case e, ok := <-c.events:
			if !ok {
				return nil
			}
			if match(e) {
				return e
			}
		case <-deadline:
			return nil
		}
	}
}

// TestSubscribePresenceNode checks the outgoing subscribe stanza shape.
func TestSubscribePresenceNode(t *testing.T) {
	c := NewWithDialer(nil, nil)
	var sent wire.Node
	sess := &session{send: func(n wire.Node) error { sent = n; return nil }}
	c.mu.Lock()
	c.active = sess
	c.mu.Unlock()

	if err := c.SubscribePresence(context.Background(), "5511999999999@s.whatsapp.net"); err != nil {
		t.Fatalf("SubscribePresence: %v", err)
	}
	if sent.Tag != "presence" || sent.Attrs["type"] != "subscribe" ||
		sent.Attrs["to"] != "5511999999999@s.whatsapp.net" {
		t.Fatalf("subscribe node wrong: %+v", sent)
	}
}

func TestParsePresenceNode(t *testing.T) {
	// available
	ev, ok := parsePresenceNode(wire.Node{Tag: "presence", Attrs: map[string]string{"from": "a@s.whatsapp.net"}})
	if !ok || ev.State != "available" {
		t.Fatalf("available: %+v ok=%v", ev, ok)
	}
	// unavailable with last
	ev, ok = parsePresenceNode(wire.Node{Tag: "presence", Attrs: map[string]string{"from": "a@s.whatsapp.net", "type": "unavailable", "last": "1700000000"}})
	if !ok || ev.State != "unavailable" || ev.LastSeen != 1700000000 {
		t.Fatalf("unavailable: %+v ok=%v", ev, ok)
	}
	// chatstate composing
	ev, ok = parsePresenceNode(wire.Node{Tag: "chatstate", Attrs: map[string]string{"from": "a@s.whatsapp.net"},
		Content: []wire.Node{{Tag: "composing"}}})
	if !ok || ev.State != "composing" {
		t.Fatalf("composing: %+v ok=%v", ev, ok)
	}
	// non presence
	if _, ok := parsePresenceNode(wire.Node{Tag: "message"}); ok {
		t.Fatal("message should not parse as presence")
	}
}

// loginCreds returns minimal registered creds so loginLoop can run.
func loginCreds() *store.Creds {
	return &store.Creds{Me: "10000000000@s.whatsapp.net", Registered: true}
}

// TestLoginLoopCallEmitsEventAndAck drives loginLoop with an inbound <call><offer>
// and asserts a CallEvent is emitted and a class="call" ack is written. The loop
// is NOT expected to auto-reject.
func TestLoginLoopCallEmitsEventAndAck(t *testing.T) {
	call := wire.Node{
		Tag:   "call",
		Attrs: map[string]string{"from": "5511999999999@s.whatsapp.net", "id": "CALLSTANZA1", "t": "1700000000"},
		Content: []wire.Node{{
			Tag:   "offer",
			Attrs: map[string]string{"call-id": "CALLID123", "call-creator": "5511999999999@s.whatsapp.net"},
		}},
	}
	conn := &scriptedConn{inbound: []wire.Node{call}}
	c := NewWithDialer(nil, nil)

	gotCall := make(chan CallEvent, 1)
	go func() {
		for e := range c.events {
			if ce, ok := e.(CallEvent); ok {
				gotCall <- ce
			}
		}
	}()

	done := make(chan struct{})
	go func() {
		_ = c.loginLoop(context.Background(), conn, loginCreds())
		close(done)
	}()
	<-done

	select {
	case ce := <-gotCall:
		if ce.Info.ID != "CALLID123" || !ce.Info.Offer {
			t.Fatalf("CallEvent wrong: %+v", ce.Info)
		}
	case <-time.After(time.Second):
		t.Fatal("no CallEvent emitted")
	}

	// An ack with class=call must have been written; no <reject> auto-sent.
	var ack *wire.Node
	for i := range conn.written {
		n := conn.written[i]
		if n.Tag == "ack" && n.Attrs["class"] == "call" {
			ack = &conn.written[i]
		}
		if n.Tag == "call" {
			if _, ok := childByTag(n, "reject"); ok {
				t.Fatal("loop auto-rejected the call; it must not")
			}
		}
	}
	if ack == nil {
		t.Fatalf("no class=call ack written; sent=%+v", conn.written)
	}
	if ack.Attrs["id"] != "CALLSTANZA1" || ack.Attrs["to"] != "5511999999999@s.whatsapp.net" {
		t.Fatalf("call ack attrs wrong: %+v", ack.Attrs)
	}
}

// TestLoginLoopPresenceEmitsEvent drives loginLoop with an inbound <presence> and
// asserts a PresenceEvent is emitted.
func TestLoginLoopPresenceEmitsEvent(t *testing.T) {
	pres := wire.Node{Tag: "presence", Attrs: map[string]string{"from": "5511888888888@s.whatsapp.net", "type": "unavailable", "last": "1699999999"}}
	conn := &scriptedConn{inbound: []wire.Node{pres}}
	c := NewWithDialer(nil, nil)

	done := make(chan struct{})
	go func() {
		_ = c.loginLoop(context.Background(), conn, loginCreds())
		close(done)
	}()

	ev := waitEvent(t, c, time.Second, func(e Event) bool { _, ok := e.(PresenceEvent); return ok })
	<-done
	pe, ok := ev.(PresenceEvent)
	if !ok {
		t.Fatalf("no PresenceEvent emitted, got %T", ev)
	}
	if pe.From != "5511888888888@s.whatsapp.net" || pe.State != "unavailable" || pe.LastSeen != 1699999999 {
		t.Fatalf("PresenceEvent wrong: %+v", pe)
	}
}
