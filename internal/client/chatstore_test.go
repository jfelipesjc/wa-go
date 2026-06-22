package client

import (
	"testing"

	"github.com/felipeleal/wa-go/internal/waproto"
)

func strp(s string) *string { return &s }
func u32p(v uint32) *uint32 { return &v }
func u64p(v uint64) *uint64 { return &v }
func boolp(b bool) *bool    { return &b }

func textWMI(id, participant, body string, ts uint64) *waproto.WebMessageInfo {
	return &waproto.WebMessageInfo{
		Key: &waproto.MessageKey{
			Id:          strp(id),
			Participant: strp(participant),
		},
		MessageTimestamp: u64p(ts),
		Message:          &waproto.Message{Conversation: strp(body)},
	}
}

func syntheticHistorySync() *waproto.HistorySync {
	return &waproto.HistorySync{
		Conversations: []*waproto.Conversation{
			{
				Id:                    "111@s.whatsapp.net",
				Name:                  strp("Alice"),
				UnreadCount:           u32p(2),
				ConversationTimestamp: u64p(1000),
				Messages: []*waproto.HistorySyncMsg{
					{Message: textWMI("m1", "", "hello", 900)},
					{Message: textWMI("m2", "", "world", 950)},
				},
			},
			{
				Id:                    "222@s.whatsapp.net",
				Name:                  strp("Bob"),
				UnreadCount:           u32p(0),
				ConversationTimestamp: u64p(2000),
				Messages: []*waproto.HistorySyncMsg{
					{Message: textWMI("m3", "", "yo", 1990)},
				},
			},
		},
		Pushnames: []*waproto.Pushname{
			{Id: strp("111@s.whatsapp.net"), Pushname: strp("Alice P")},
			{Id: strp("222@s.whatsapp.net"), Pushname: strp("Bob P")},
		},
	}
}

func TestApplyHistorySync(t *testing.T) {
	cs := NewChatStore()
	cs.ApplyHistorySync(syntheticHistorySync())

	chats := cs.Chats()
	if len(chats) != 2 {
		t.Fatalf("want 2 chats, got %d", len(chats))
	}
	// Ordered by ConversationTimestamp desc: Bob (2000) before Alice (1000).
	if chats[0].JID != "222@s.whatsapp.net" || chats[1].JID != "111@s.whatsapp.net" {
		t.Fatalf("chats not ordered desc: %s, %s", chats[0].JID, chats[1].JID)
	}
	if chats[1].Name != "Alice" || chats[1].UnreadCount != 2 || chats[1].ConversationTimestamp != 1000 {
		t.Fatalf("alice chat wrong: %+v", chats[1])
	}

	c, ok := cs.Contact("111@s.whatsapp.net")
	if !ok || c.PushName != "Alice P" {
		t.Fatalf("alice contact wrong: %+v ok=%v", c, ok)
	}

	msgs := cs.ChatMessages("111@s.whatsapp.net", 0)
	if len(msgs) != 2 {
		t.Fatalf("want 2 msgs, got %d", len(msgs))
	}
	if msgs[0].Key != "m1" || msgs[0].Text != "hello" || msgs[1].Key != "m2" {
		t.Fatalf("messages wrong/unordered: %+v", msgs)
	}
	if msgs[0].Type != string(MessageText) {
		t.Fatalf("want text type, got %q", msgs[0].Type)
	}

	// limit returns the most recent N (chronological order).
	last := cs.ChatMessages("111@s.whatsapp.net", 1)
	if len(last) != 1 || last[0].Key != "m2" {
		t.Fatalf("limit wrong: %+v", last)
	}
}

func TestConsumeMessageEvent(t *testing.T) {
	cs := NewChatStore()
	cs.ApplyHistorySync(syntheticHistorySync())

	cs.Consume(MessageEvent{
		From:      "111@s.whatsapp.net",
		ID:        "m99",
		Timestamp: 3000,
		Type:      MessageText,
		Text:      "new message",
		PushName:  "Alice Live",
	})

	ch, ok := cs.Chat("111@s.whatsapp.net")
	if !ok || ch.ConversationTimestamp != 3000 || ch.LastMessageTime != 3000 {
		t.Fatalf("chat not bumped: %+v ok=%v", ch, ok)
	}

	msgs := cs.ChatMessages("111@s.whatsapp.net", 0)
	if len(msgs) != 3 || msgs[2].Key != "m99" || msgs[2].Text != "new message" {
		t.Fatalf("message not appended: %+v", msgs)
	}

	// Chats() now orders alice (3000) first.
	if cs.Chats()[0].JID != "111@s.whatsapp.net" {
		t.Fatalf("reorder failed: %+v", cs.Chats())
	}
}

func TestConsumeAppStateArchive(t *testing.T) {
	cs := NewChatStore()
	cs.ApplyHistorySync(syntheticHistorySync())

	cs.Consume(AppStateMutationEvent{
		Collection: "regular_high",
		Index:      []string{"archive", "111@s.whatsapp.net"},
		Action: &waproto.SyncActionValue{
			ArchiveChatAction: &waproto.SyncActionValue_ArchiveChatAction{
				Archived: boolp(true),
			},
		},
		Operation: waproto.SyncdMutation_SET,
	})

	ch, _ := cs.Chat("111@s.whatsapp.net")
	if !ch.Archived {
		t.Fatalf("archive flag not set: %+v", ch)
	}

	// Pin via SET.
	cs.Consume(AppStateMutationEvent{
		Index: []string{"pin_v1", "222@s.whatsapp.net"},
		Action: &waproto.SyncActionValue{
			PinAction: &waproto.SyncActionValue_PinAction{Pinned: boolp(true)},
		},
		Operation: waproto.SyncdMutation_SET,
	})
	if ch2, _ := cs.Chat("222@s.whatsapp.net"); !ch2.Pinned {
		t.Fatalf("pin flag not set: %+v", ch2)
	}

	// Mute via SET, then REMOVE clears it.
	cs.Consume(AppStateMutationEvent{
		Index: []string{"mute", "222@s.whatsapp.net"},
		Action: &waproto.SyncActionValue{
			MuteAction: &waproto.SyncActionValue_MuteAction{Muted: boolp(true)},
		},
		Operation: waproto.SyncdMutation_SET,
	})
	if ch2, _ := cs.Chat("222@s.whatsapp.net"); !ch2.Muted {
		t.Fatalf("mute flag not set: %+v", ch2)
	}
	cs.Consume(AppStateMutationEvent{
		Index:     []string{"mute", "222@s.whatsapp.net"},
		Action:    &waproto.SyncActionValue{MuteAction: &waproto.SyncActionValue_MuteAction{}},
		Operation: waproto.SyncdMutation_REMOVE,
	})
	if ch2, _ := cs.Chat("222@s.whatsapp.net"); ch2.Muted {
		t.Fatalf("mute flag not cleared: %+v", ch2)
	}
}

func TestConsumeContactAndReceipt(t *testing.T) {
	cs := NewChatStore()
	cs.ApplyHistorySync(syntheticHistorySync())

	cs.Consume(ContactEvent{JID: "111@s.whatsapp.net", FullName: "Alice Full"})
	if c, _ := cs.Contact("111@s.whatsapp.net"); c.Name != "Alice Full" || c.PushName != "Alice P" {
		t.Fatalf("contact merge wrong: %+v", c)
	}

	// read receipt clears unread.
	cs.Consume(ReceiptEvent{From: "111@s.whatsapp.net", Type: "read"})
	if ch, _ := cs.Chat("111@s.whatsapp.net"); ch.UnreadCount != 0 {
		t.Fatalf("unread not cleared: %+v", ch)
	}
}
