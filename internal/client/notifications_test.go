package client

import (
	"context"
	"testing"
	"time"

	"github.com/felipeleal/wa-go/internal/wire"
)

// emitCollect runs fn (which calls c.emit indirectly) and returns the first event
// matching the predicate, draining the buffered events channel synchronously.
func emitCollect(t *testing.T, c *Client, fn func(), match func(Event) bool) Event {
	t.Helper()
	fn()
	for {
		select {
		case e := <-c.events:
			if match(e) {
				return e
			}
		default:
			return nil
		}
	}
}

func TestHandleNotificationGroupAdd(t *testing.T) {
	c := newTestClient(t)
	node := wire.Node{
		Tag: "notification",
		Attrs: map[string]string{
			"type":        "w:gp2",
			"from":        "123-456@g.us",
			"participant": "5511000000000@s.whatsapp.net",
		},
		Content: []wire.Node{{
			Tag:   "add",
			Attrs: map[string]string{},
			Content: []wire.Node{
				{Tag: "participant", Attrs: map[string]string{"jid": "5511111111111@s.whatsapp.net"}},
				{Tag: "participant", Attrs: map[string]string{"jid": "5512222222222@s.whatsapp.net"}},
			},
		}},
	}
	ev := emitCollect(t, c, func() { c.handleNotification(node) }, func(e Event) bool {
		_, ok := e.(GroupParticipantsUpdateEvent)
		return ok
	})
	g, ok := ev.(GroupParticipantsUpdateEvent)
	if !ok {
		t.Fatalf("no GroupParticipantsUpdateEvent, got %T", ev)
	}
	if g.GroupJID != "123-456@g.us" || g.Action != GroupParticipantAdd || g.By != "5511000000000@s.whatsapp.net" {
		t.Fatalf("group event attrs wrong: %+v", g)
	}
	if len(g.Participants) != 2 || g.Participants[0] != "5511111111111@s.whatsapp.net" {
		t.Fatalf("participants wrong: %+v", g.Participants)
	}
}

func TestHandleNotificationGroupRemove(t *testing.T) {
	c := newTestClient(t)
	node := wire.Node{
		Tag:   "notification",
		Attrs: map[string]string{"type": "w:gp2", "from": "g@g.us"},
		Content: []wire.Node{{
			Tag:     "remove",
			Content: []wire.Node{{Tag: "participant", Attrs: map[string]string{"jid": "x@s.whatsapp.net"}}},
		}},
	}
	ev := emitCollect(t, c, func() { c.handleNotification(node) }, func(e Event) bool {
		_, ok := e.(GroupParticipantsUpdateEvent)
		return ok
	})
	g := ev.(GroupParticipantsUpdateEvent)
	if g.Action != GroupParticipantRemove || len(g.Participants) != 1 {
		t.Fatalf("remove event wrong: %+v", g)
	}
}

func TestHandleNotificationGroupSubject(t *testing.T) {
	c := newTestClient(t)
	node := wire.Node{
		Tag:   "notification",
		Attrs: map[string]string{"type": "w:gp2", "from": "g@g.us", "participant": "a@s.whatsapp.net"},
		Content: []wire.Node{{
			Tag:   "subject",
			Attrs: map[string]string{"subject": "New Group Name"},
		}},
	}
	ev := emitCollect(t, c, func() { c.handleNotification(node) }, func(e Event) bool {
		_, ok := e.(GroupUpdateEvent)
		return ok
	})
	g, ok := ev.(GroupUpdateEvent)
	if !ok {
		t.Fatalf("no GroupUpdateEvent, got %T", ev)
	}
	if g.Subject == nil || *g.Subject != "New Group Name" {
		t.Fatalf("subject wrong: %+v", g)
	}
	if g.By != "a@s.whatsapp.net" {
		t.Fatalf("by wrong: %+v", g)
	}
}

func TestHandleNotificationGroupAnnounce(t *testing.T) {
	c := newTestClient(t)
	node := wire.Node{
		Tag:     "notification",
		Attrs:   map[string]string{"type": "w:gp2", "from": "g@g.us"},
		Content: []wire.Node{{Tag: "announcement"}},
	}
	ev := emitCollect(t, c, func() { c.handleNotification(node) }, func(e Event) bool {
		_, ok := e.(GroupUpdateEvent)
		return ok
	})
	g := ev.(GroupUpdateEvent)
	if g.Announce == nil || *g.Announce != true {
		t.Fatalf("announce wrong: %+v", g)
	}
}

func TestHandleNotificationGroupEphemeral(t *testing.T) {
	c := newTestClient(t)
	node := wire.Node{
		Tag:     "notification",
		Attrs:   map[string]string{"type": "w:gp2", "from": "g@g.us"},
		Content: []wire.Node{{Tag: "ephemeral", Attrs: map[string]string{"expiration": "604800"}}},
	}
	ev := emitCollect(t, c, func() { c.handleNotification(node) }, func(e Event) bool {
		_, ok := e.(GroupUpdateEvent)
		return ok
	})
	g := ev.(GroupUpdateEvent)
	if g.Ephemeral == nil || *g.Ephemeral != 604800 {
		t.Fatalf("ephemeral wrong: %+v", g)
	}
}

func TestHandleNotificationPicture(t *testing.T) {
	c := newTestClient(t)
	node := wire.Node{
		Tag:   "notification",
		Attrs: map[string]string{"type": "picture", "from": "5511999999999@s.whatsapp.net"},
		Content: []wire.Node{{
			Tag:   "set",
			Attrs: map[string]string{"author": "5511999999999@s.whatsapp.net", "id": "123"},
		}},
	}
	ev := emitCollect(t, c, func() { c.handleNotification(node) }, func(e Event) bool {
		_, ok := e.(PictureUpdateEvent)
		return ok
	})
	p, ok := ev.(PictureUpdateEvent)
	if !ok {
		t.Fatalf("no PictureUpdateEvent, got %T", ev)
	}
	if p.JID != "5511999999999@s.whatsapp.net" || p.Removed || p.Author == "" {
		t.Fatalf("picture event wrong: %+v", p)
	}
}

func TestHandleNotificationPictureDelete(t *testing.T) {
	c := newTestClient(t)
	node := wire.Node{
		Tag:     "notification",
		Attrs:   map[string]string{"type": "picture", "from": "g@g.us"},
		Content: []wire.Node{{Tag: "delete", Attrs: map[string]string{"author": "a@s.whatsapp.net"}}},
	}
	ev := emitCollect(t, c, func() { c.handleNotification(node) }, func(e Event) bool {
		_, ok := e.(PictureUpdateEvent)
		return ok
	})
	p := ev.(PictureUpdateEvent)
	if !p.Removed {
		t.Fatalf("expected Removed=true: %+v", p)
	}
}

func TestHandleNotificationAccountSync(t *testing.T) {
	c := newTestClient(t)
	node := wire.Node{
		Tag:   "notification",
		Attrs: map[string]string{"type": "server_sync", "from": "s.whatsapp.net"},
		Content: []wire.Node{
			{Tag: "collection", Attrs: map[string]string{"name": "critical_block"}},
			{Tag: "collection", Attrs: map[string]string{"name": "regular"}},
		},
	}
	ev := emitCollect(t, c, func() { c.handleNotification(node) }, func(e Event) bool {
		_, ok := e.(AppStateSyncDirtyEvent)
		return ok
	})
	a, ok := ev.(AppStateSyncDirtyEvent)
	if !ok {
		t.Fatalf("no AppStateSyncDirtyEvent, got %T", ev)
	}
	if len(a.Collections) != 2 || a.Collections[0] != "critical_block" {
		t.Fatalf("collections wrong: %+v", a.Collections)
	}
}

func TestHandleNotificationContacts(t *testing.T) {
	c := newTestClient(t)
	node := wire.Node{
		Tag:   "notification",
		Attrs: map[string]string{"type": "devices", "from": "5511999999999@s.whatsapp.net"},
	}
	ev := emitCollect(t, c, func() { c.handleNotification(node) }, func(e Event) bool {
		_, ok := e.(ContactUpdateEvent)
		return ok
	})
	cu := ev.(ContactUpdateEvent)
	if cu.Type != "devices" || cu.JID != "5511999999999@s.whatsapp.net" {
		t.Fatalf("contact update wrong: %+v", cu)
	}
}

func TestHandleNotificationUnknownCatchAll(t *testing.T) {
	c := newTestClient(t)
	node := wire.Node{
		Tag:   "notification",
		Attrs: map[string]string{"type": "status", "from": "x@s.whatsapp.net"},
	}
	ev := emitCollect(t, c, func() { c.handleNotification(node) }, func(e Event) bool {
		_, ok := e.(NotificationEvent)
		return ok
	})
	n, ok := ev.(NotificationEvent)
	if !ok {
		t.Fatalf("no NotificationEvent catch-all, got %T", ev)
	}
	if n.Type != "status" {
		t.Fatalf("catch-all type wrong: %+v", n)
	}
}

// TestHandleEncryptNotificationLowCountUploadsPreKeys feeds an <encrypt> with a
// low count and asserts a prekey upload iq (xmlns=encrypt, type=set) is written
// over the live session.
func TestHandleEncryptNotificationLowCount(t *testing.T) {
	c := newTestClient(t)
	creds := credsForTest(t)

	var sent []wire.Node
	sess := &session{
		send:  func(n wire.Node) error { sent = append(sent, n); return nil },
		creds: creds,
	}
	c.mu.Lock()
	c.active = sess
	c.mu.Unlock()

	node := wire.Node{
		Tag:     "notification",
		Attrs:   map[string]string{"type": "encrypt", "from": sWhatsAppNet},
		Content: []wire.Node{{Tag: "count", Attrs: map[string]string{"value": "2"}}},
	}
	c.handleNotification(node)

	var upload *wire.Node
	for i := range sent {
		if sent[i].Tag == "iq" && sent[i].Attrs["xmlns"] == "encrypt" && sent[i].Attrs["type"] == "set" {
			upload = &sent[i]
		}
	}
	if upload == nil {
		t.Fatalf("low pre-key count did not trigger a prekey upload iq; sent=%+v", sent)
	}
}

// TestHandleEncryptNotificationHighCountNoUpload asserts a healthy count does not
// trigger a re-upload.
func TestHandleEncryptNotificationHighCount(t *testing.T) {
	c := newTestClient(t)
	creds := credsForTest(t)
	var sent []wire.Node
	sess := &session{send: func(n wire.Node) error { sent = append(sent, n); return nil }, creds: creds}
	c.mu.Lock()
	c.active = sess
	c.mu.Unlock()

	node := wire.Node{
		Tag:     "notification",
		Attrs:   map[string]string{"type": "encrypt", "from": sWhatsAppNet},
		Content: []wire.Node{{Tag: "count", Attrs: map[string]string{"value": "30"}}},
	}
	c.handleNotification(node)
	if len(sent) != 0 {
		t.Fatalf("healthy pre-key count should not re-upload; sent=%+v", sent)
	}
}

func TestHandleReceiptUpdateRead(t *testing.T) {
	c := newTestClient(t)
	node := wire.Node{
		Tag: "receipt",
		Attrs: map[string]string{
			"from": "5511999999999@s.whatsapp.net",
			"id":   "MSGID1",
			"type": "read",
			"t":    "1700000000",
		},
	}
	ev := emitCollect(t, c, func() { c.handleReceiptUpdate(node) }, func(e Event) bool {
		_, ok := e.(ReceiptUpdateEvent)
		return ok
	})
	r, ok := ev.(ReceiptUpdateEvent)
	if !ok {
		t.Fatalf("no ReceiptUpdateEvent, got %T", ev)
	}
	if r.Type != ReceiptRead || r.From != "5511999999999@s.whatsapp.net" || r.Timestamp != 1700000000 {
		t.Fatalf("receipt update wrong: %+v", r)
	}
	if len(r.For) != 1 || r.For[0] != "MSGID1" {
		t.Fatalf("receipt ids wrong: %+v", r.For)
	}
}

func TestHandleReceiptUpdateDeliveryWithList(t *testing.T) {
	c := newTestClient(t)
	node := wire.Node{
		Tag:   "receipt",
		Attrs: map[string]string{"from": "x@s.whatsapp.net", "id": "A"},
		Content: []wire.Node{{
			Tag: "list",
			Content: []wire.Node{
				{Tag: "item", Attrs: map[string]string{"id": "B"}},
				{Tag: "item", Attrs: map[string]string{"id": "C"}},
			},
		}},
	}
	ev := emitCollect(t, c, func() { c.handleReceiptUpdate(node) }, func(e Event) bool {
		_, ok := e.(ReceiptUpdateEvent)
		return ok
	})
	r := ev.(ReceiptUpdateEvent)
	if r.Type != ReceiptDelivery {
		t.Fatalf("expected delivery, got %v", r.Type)
	}
	if len(r.For) != 3 || r.For[0] != "A" || r.For[1] != "B" || r.For[2] != "C" {
		t.Fatalf("batched ids wrong: %+v", r.For)
	}
}

func TestHandleReceiptUpdatePlayed(t *testing.T) {
	c := newTestClient(t)
	node := wire.Node{Tag: "receipt", Attrs: map[string]string{"from": "x@s.whatsapp.net", "id": "P", "type": "played"}}
	ev := emitCollect(t, c, func() { c.handleReceiptUpdate(node) }, func(e Event) bool {
		_, ok := e.(ReceiptUpdateEvent)
		return ok
	})
	r := ev.(ReceiptUpdateEvent)
	if r.Type != ReceiptPlayed {
		t.Fatalf("expected played, got %v", r.Type)
	}
}

// TestReceiptUpdateTypeRetryExcluded verifies retry/sender receipt types do not
// map to a ReceiptUpdateEvent (they are handled by the resend path).
func TestReceiptUpdateTypeRetryExcluded(t *testing.T) {
	if _, ok := receiptUpdateType("retry"); ok {
		t.Fatal("retry should not produce a receipt update")
	}
	if _, ok := receiptUpdateType("sender"); ok {
		t.Fatal("sender should not produce a receipt update")
	}
}

// TestLoginLoopNotificationEmitsAndAcks drives loginLoop end-to-end with a w:gp2
// notification and asserts both the granular event and the ack are produced.
func TestLoginLoopNotificationEmitsAndAcks(t *testing.T) {
	notif := wire.Node{
		Tag:   "notification",
		Attrs: map[string]string{"type": "w:gp2", "from": "g@g.us", "id": "N1", "participant": "a@s.whatsapp.net"},
		Content: []wire.Node{{
			Tag:     "promote",
			Content: []wire.Node{{Tag: "participant", Attrs: map[string]string{"jid": "b@s.whatsapp.net"}}},
		}},
	}
	conn := &scriptedConn{inbound: []wire.Node{notif}}
	c := NewWithDialer(nil, nil)

	done := make(chan struct{})
	go func() {
		_ = c.loginLoop(context.Background(), conn, loginCreds())
		close(done)
	}()

	ev := waitEvent(t, c, time.Second, func(e Event) bool {
		_, ok := e.(GroupParticipantsUpdateEvent)
		return ok
	})
	<-done
	g, ok := ev.(GroupParticipantsUpdateEvent)
	if !ok {
		t.Fatalf("no GroupParticipantsUpdateEvent, got %T", ev)
	}
	if g.Action != GroupParticipantPromote {
		t.Fatalf("expected promote, got %v", g.Action)
	}
	var ack *wire.Node
	for i := range conn.written {
		if conn.written[i].Tag == "ack" && conn.written[i].Attrs["class"] == "notification" {
			ack = &conn.written[i]
		}
	}
	if ack == nil {
		t.Fatalf("no class=notification ack written; sent=%+v", conn.written)
	}
	if ack.Attrs["id"] != "N1" {
		t.Fatalf("ack id wrong: %+v", ack.Attrs)
	}
}

// TestLoginLoopReadReceiptEmitsUpdate drives loginLoop with a read receipt and
// asserts a ReceiptUpdateEvent is emitted (in addition to the legacy ReceiptEvent).
func TestLoginLoopReadReceiptEmitsUpdate(t *testing.T) {
	rcpt := wire.Node{
		Tag:   "receipt",
		Attrs: map[string]string{"from": "5511888888888@s.whatsapp.net", "id": "RID", "type": "read", "t": "1699999999"},
	}
	conn := &scriptedConn{inbound: []wire.Node{rcpt}}
	c := NewWithDialer(nil, nil)

	done := make(chan struct{})
	go func() {
		_ = c.loginLoop(context.Background(), conn, loginCreds())
		close(done)
	}()

	ev := waitEvent(t, c, time.Second, func(e Event) bool {
		_, ok := e.(ReceiptUpdateEvent)
		return ok
	})
	<-done
	r, ok := ev.(ReceiptUpdateEvent)
	if !ok {
		t.Fatalf("no ReceiptUpdateEvent, got %T", ev)
	}
	if r.Type != ReceiptRead || len(r.For) != 1 || r.For[0] != "RID" {
		t.Fatalf("receipt update wrong: %+v", r)
	}
}
