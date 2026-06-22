package client

import (
	"testing"

	"github.com/felipeleal/wa-go/internal/keys"
	"github.com/felipeleal/wa-go/internal/signal"
	"github.com/felipeleal/wa-go/internal/waproto"
	"github.com/felipeleal/wa-go/internal/wire"
	"google.golang.org/protobuf/proto"
)

// TestHandleGroupMessage exercises the full group receive path:
//  1. ALICE mints a sender key for the group and sends the SenderKeyDistribution
//     embedded in a Message over the 1:1 signal session (a pkmsg/msg <enc>). The
//     handler processes it and persists the sender key record.
//  2. ALICE encrypts a real WAProto.Message via the group cipher (skmsg). The
//     handler loads the installed sender key, decrypts via DecryptGroup, parses,
//     and emits a MessageEvent with IsGroup/Sender set and the right text.
func TestHandleGroupMessage(t *testing.T) {
	// Reuse the 1:1 session setup: bob (us) has a session with alice's address,
	// and aliceCipher can emit a "msg" <enc> to bob.
	c, bobCreds, aliceJID, aliceCipher := setupBobAndAlice(t)

	const groupJID = "120363000000000000@g.us"

	// --- ALICE builds her group sender key + SKDM ---
	signing, err := keys.GenKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	var chainSeed [32]byte
	for i := range chainSeed {
		chainSeed[i] = byte(i*3 + 1)
	}
	aliceGroup := signal.NewGroupCipher(&signal.SenderKeyRecord{})
	skdm := aliceGroup.CreateSenderKeyDistribution(987654, chainSeed, signing)
	axolotl := signal.SerializeSenderKeyDistributionMessage(skdm.KeyID, skdm.Iteration, skdm.ChainKey, skdm.SigningPub)

	// Wrap the SKDM in a Message and deliver it over the 1:1 channel as a "msg".
	skdmMsg := &waproto.Message{
		SenderKeyDistributionMessage: &waproto.SenderKeyDistributionMessage{
			GroupId:                             proto.String(groupJID),
			AxolotlSenderKeyDistributionMessage: axolotl,
		},
	}
	skdmRaw, _ := proto.Marshal(skdmMsg)
	ctSKDM, err := aliceCipher.Encrypt(padWAMessage(skdmRaw, 5))
	if err != nil {
		t.Fatalf("encrypt SKDM enc: %v", err)
	}

	skdmStanza := wire.Node{
		Tag: "message",
		Attrs: map[string]string{
			"from": groupJID, "participant": aliceJID, "id": "SKDM1", "type": "text", "t": "1700000001",
		},
		Content: []wire.Node{
			{Tag: "enc", Attrs: map[string]string{"type": ctSKDM.Type}, Content: ctSKDM.Serialized},
		},
	}
	if err := c.handleMessage((&recordConn{}).SendNode, skdmStanza, bobCreds); err != nil {
		t.Fatalf("handleMessage(SKDM): %v", err)
	}

	// The sender key must now be persisted for (group, sender).
	if _, ok, _ := c.store.LoadSenderKey(groupJID, aliceJID); !ok {
		t.Fatal("sender key not persisted after SKDM")
	}
	// The SKDM-only message must NOT have emitted a content event.
	drainNonContent(t, c)

	// --- ALICE sends an actual group message (skmsg) ---
	const want = "ola grupo, mensagem real"
	groupMsg := &waproto.Message{Conversation: proto.String(want)}
	groupRaw, _ := proto.Marshal(groupMsg)
	skmsg, err := aliceGroup.EncryptGroup(padWAMessage(groupRaw, 9))
	if err != nil {
		t.Fatalf("EncryptGroup: %v", err)
	}

	groupStanza := wire.Node{
		Tag: "message",
		Attrs: map[string]string{
			"from": groupJID, "participant": aliceJID, "id": "GMSG1", "type": "text", "t": "1700000002",
		},
		Content: []wire.Node{
			{Tag: "enc", Attrs: map[string]string{"type": "skmsg"}, Content: skmsg},
		},
	}
	rc := &recordConn{}
	if err := c.handleMessage(rc.SendNode, groupStanza, bobCreds); err != nil {
		t.Fatalf("handleMessage(skmsg): %v", err)
	}

	got := nextMessageEvent(t, c)
	if got == nil {
		t.Fatal("no MessageEvent for group skmsg")
	}
	if got.Text != want {
		t.Fatalf("group text = %q, want %q", got.Text, want)
	}
	if !got.IsGroup || got.Sender != aliceJID || got.From != groupJID {
		t.Fatalf("group envelope wrong: IsGroup=%v Sender=%q From=%q", got.IsGroup, got.Sender, got.From)
	}
	if got.ID != "GMSG1" {
		t.Fatalf("id = %q", got.ID)
	}

	// receipt + ack were sent for the group message (with participant echoed).
	var sawReceipt bool
	for _, n := range rc.sent {
		if n.Tag == "receipt" {
			sawReceipt = true
			if n.Attrs["participant"] != aliceJID {
				t.Errorf("receipt participant = %q", n.Attrs["participant"])
			}
		}
	}
	if !sawReceipt {
		t.Error("no receipt for group message")
	}
}

// drainNonContent fails if any MessageEvent is currently queued (used after the
// SKDM-only message which must not emit content).
func drainNonContent(t *testing.T, c *Client) {
	t.Helper()
	for {
		select {
		case ev := <-c.Events():
			if me, ok := ev.(MessageEvent); ok {
				t.Fatalf("unexpected MessageEvent after SKDM-only message: %+v", me)
			}
		default:
			return
		}
	}
}

// nextMessageEvent drains the event channel and returns the first MessageEvent.
func nextMessageEvent(t *testing.T, c *Client) *MessageEvent {
	t.Helper()
	for {
		select {
		case ev := <-c.Events():
			if me, ok := ev.(MessageEvent); ok {
				return &me
			}
		default:
			return nil
		}
	}
}
