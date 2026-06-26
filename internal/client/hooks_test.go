package client

import (
	"context"
	"sync"
	"testing"

	"github.com/jfelipesjc/wa-go/internal/control"
	"github.com/jfelipesjc/wa-go/internal/keys"
	"github.com/jfelipesjc/wa-go/internal/wire"
)

// TestOnIncomingHookCalled drives pairingLoop over a scripted conn carrying one
// pair-device iq and asserts the incoming hook saw that node.
func TestOnIncomingHookCalled(t *testing.T) {
	id, err := keys.NewIdentity()
	if err != nil {
		t.Fatalf("new identity: %v", err)
	}
	creds := credsFromIdentity(id)

	pairDevice := wire.Node{
		Tag:   "iq",
		Attrs: map[string]string{"type": "set", "id": "iq-1", "from": sWhatsAppNet},
		Content: []wire.Node{
			{
				Tag:   "pair-device",
				Attrs: map[string]string{},
				Content: []wire.Node{
					{Tag: "ref", Attrs: map[string]string{}, Content: []byte("REF_X")},
				},
			},
		},
	}

	conn := &scriptedConn{inbound: []wire.Node{pairDevice}}
	c := New(nil)

	var mu sync.Mutex
	var incoming []string
	c.OnIncomingNode(func(n wire.Node) {
		mu.Lock()
		incoming = append(incoming, n.Tag+"/"+n.Attrs["id"])
		mu.Unlock()
	})

	done := make(chan struct{})
	go func() {
		for range c.events {
		}
		close(done)
	}()
	_, _ = c.pairingLoop(context.Background(), conn, creds)
	close(c.events)
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(incoming) != 1 || incoming[0] != "iq/iq-1" {
		t.Fatalf("incoming hook got %v, want [iq/iq-1]", incoming)
	}
}

// TestOnOutgoingHookCalled asserts the outgoing hook fires for the iq-result
// reply that pairingLoop writes in response to pair-device.
func TestOnOutgoingHookCalled(t *testing.T) {
	id, err := keys.NewIdentity()
	if err != nil {
		t.Fatalf("new identity: %v", err)
	}
	creds := credsFromIdentity(id)

	pairDevice := wire.Node{
		Tag:   "iq",
		Attrs: map[string]string{"type": "set", "id": "iq-77", "from": sWhatsAppNet},
		Content: []wire.Node{
			{
				Tag:   "pair-device",
				Attrs: map[string]string{},
				Content: []wire.Node{
					{Tag: "ref", Attrs: map[string]string{}, Content: []byte("REF_Y")},
				},
			},
		},
	}

	conn := &scriptedConn{inbound: []wire.Node{pairDevice}}
	c := New(nil)

	var mu sync.Mutex
	var outgoing []wire.Node
	c.OnOutgoingNode(func(n *wire.Node) {
		mu.Lock()
		outgoing = append(outgoing, *n)
		mu.Unlock()
	})

	done := make(chan struct{})
	go func() {
		for range c.events {
		}
		close(done)
	}()
	_, _ = c.pairingLoop(context.Background(), conn, creds)
	close(c.events)
	<-done

	mu.Lock()
	defer mu.Unlock()
	var sawResult bool
	for _, n := range outgoing {
		if n.Tag == "iq" && n.Attrs["type"] == "result" && n.Attrs["id"] == "iq-77" {
			sawResult = true
		}
	}
	if !sawResult {
		t.Fatalf("outgoing hook never saw the iq-result reply; got %d nodes", len(outgoing))
	}
}

// TestOutgoingHookCanMutateNode proves the hook receives a pointer it can mutate
// before the node is written to the wire.
func TestOutgoingHookCanMutateNode(t *testing.T) {
	c := New(nil)
	c.OnOutgoingNode(func(n *wire.Node) {
		if n.Attrs == nil {
			n.Attrs = map[string]string{}
		}
		n.Attrs["x-hooked"] = "1"
	})

	conn := &scriptedConn{}
	// Build the wrapped send closure the way loginLoop does, indirectly: call the
	// hook runner then write. We emulate by invoking runOutgoingHooks + SendNode.
	n := wire.Node{Tag: "ping", Attrs: map[string]string{}}
	c.runOutgoingHooks(&n)
	_ = conn.SendNode(n)

	if len(conn.written) != 1 || conn.written[0].Attrs["x-hooked"] != "1" {
		t.Fatalf("mutation by outgoing hook not observed: %+v", conn.written)
	}
}

// TestHookPanicRecovered ensures a panicking hook does not crash the loop and the
// node is still processed/written.
func TestHookPanicRecovered(t *testing.T) {
	c := New(nil)
	c.OnIncomingNode(func(n wire.Node) { panic("boom in") })
	c.OnOutgoingNode(func(n *wire.Node) { panic("boom out") })

	// Direct unit test of the recover wrappers: these must not panic.
	c.runIncomingHooks(wire.Node{Tag: "iq"})
	out := wire.Node{Tag: "iq", Attrs: map[string]string{}}
	c.runOutgoingHooks(&out)

	// And a second, well-behaved hook after the panicking one still runs.
	var ran bool
	c.OnIncomingNode(func(n wire.Node) { ran = true })
	c.runIncomingHooks(wire.Node{Tag: "message"})
	if !ran {
		t.Fatal("subsequent hook did not run after a panicking one")
	}
}

// TestNewWithOptions wires the Control Layer options onto the Client.
func TestNewWithOptions(t *testing.T) {
	prof := control.RandomDesktopProfile(5)
	pacer := control.NewHumanPacer(control.DefaultPacerConfig(), nil)
	c := NewWithOptions(nil, nil, WithDeviceProfile(prof), WithPacer(pacer))
	_ = c // construction + option application is the assertion
	if (c.profile == control.DeviceProfile{}) {
		t.Fatal("device profile not applied")
	}
	if c.pacer == nil {
		t.Fatal("pacer not applied")
	}
}
