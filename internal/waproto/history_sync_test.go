package waproto

import (
	"bytes"
	"testing"

	"google.golang.org/protobuf/proto"
)

// TestHistorySyncRoundTrip: a HistorySync carrying one Conversation with one
// HistorySyncMsg (a WebMessageInfo with a text Message) plus a pushname survives
// marshal/unmarshal with all fields intact.
func TestHistorySyncRoundTrip(t *testing.T) {
	orig := &HistorySync{
		SyncType:   HistorySyncType_HISTORY_SYNC_RECENT,
		ChunkOrder: proto.Uint32(1),
		Progress:   proto.Uint32(42),
		Conversations: []*Conversation{
			{
				Id:          "5511999999999@s.whatsapp.net",
				Name:        proto.String("Alice"),
				UnreadCount: proto.Uint32(3),
				Messages: []*HistorySyncMsg{
					{
						MsgOrderId: proto.Uint64(7),
						Message: &WebMessageInfo{
							Key: &MessageKey{
								RemoteJid: proto.String("5511999999999@s.whatsapp.net"),
								FromMe:    proto.Bool(false),
								Id:        proto.String("ABCD1234"),
							},
							MessageTimestamp: proto.Uint64(1700000000),
							Status:           WebMessageInfo_DELIVERY_ACK.Enum(),
							PushName:         proto.String("Alice"),
							Message: &Message{
								Conversation: proto.String("oi tudo bem?"),
							},
						},
					},
				},
			},
		},
		Pushnames: []*Pushname{
			{Id: proto.String("5511888888888@s.whatsapp.net"), Pushname: proto.String("Bob")},
		},
	}

	b, err := proto.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got HistorySync
	if err := proto.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.GetSyncType() != HistorySyncType_HISTORY_SYNC_RECENT {
		t.Errorf("syncType = %v", got.GetSyncType())
	}
	if got.GetChunkOrder() != 1 || got.GetProgress() != 42 {
		t.Errorf("chunkOrder/progress = %d/%d", got.GetChunkOrder(), got.GetProgress())
	}
	if len(got.GetConversations()) != 1 {
		t.Fatalf("conversations = %d", len(got.GetConversations()))
	}
	conv := got.GetConversations()[0]
	if conv.GetId() != "5511999999999@s.whatsapp.net" || conv.GetName() != "Alice" || conv.GetUnreadCount() != 3 {
		t.Errorf("conversation mismatch: %+v", conv)
	}
	if len(conv.GetMessages()) != 1 {
		t.Fatalf("messages = %d", len(conv.GetMessages()))
	}
	msg := conv.GetMessages()[0]
	if msg.GetMsgOrderId() != 7 {
		t.Errorf("msgOrderId = %d", msg.GetMsgOrderId())
	}
	wmi := msg.GetMessage()
	if wmi.GetKey().GetId() != "ABCD1234" {
		t.Errorf("key.id = %q", wmi.GetKey().GetId())
	}
	if wmi.GetMessage().GetConversation() != "oi tudo bem?" {
		t.Errorf("text = %q", wmi.GetMessage().GetConversation())
	}
	if wmi.GetStatus() != WebMessageInfo_DELIVERY_ACK {
		t.Errorf("status = %v", wmi.GetStatus())
	}
	if len(got.GetPushnames()) != 1 || got.GetPushnames()[0].GetPushname() != "Bob" {
		t.Errorf("pushnames = %+v", got.GetPushnames())
	}
}

// TestHistorySyncNotificationRoundTrip: the notification (carried in a
// ProtocolMessage) round-trips with its media reference and sync type.
func TestHistorySyncNotificationRoundTrip(t *testing.T) {
	orig := &ProtocolMessage{
		Type: ProtocolMessage_HISTORY_SYNC_NOTIFICATION.Enum(),
		HistorySyncNotification: &HistorySyncNotification{
			FileSha256:    []byte{1, 2, 3},
			FileLength:    proto.Uint64(99999),
			MediaKey:      bytes.Repeat([]byte{0xAB}, 32),
			FileEncSha256: []byte{4, 5, 6},
			DirectPath:    proto.String("/d/f/hist.enc"),
			SyncType:      HistorySyncType_HISTORY_SYNC_INITIAL_BOOTSTRAP.Enum(),
			ChunkOrder:    proto.Uint32(0),
			Progress:      proto.Uint32(10),
		},
	}

	b, err := proto.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got ProtocolMessage
	if err := proto.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	n := got.GetHistorySyncNotification()
	if n == nil {
		t.Fatal("historySyncNotification nil")
	}
	if n.GetDirectPath() != "/d/f/hist.enc" {
		t.Errorf("directPath = %q", n.GetDirectPath())
	}
	if !bytes.Equal(n.GetMediaKey(), bytes.Repeat([]byte{0xAB}, 32)) {
		t.Errorf("mediaKey mismatch")
	}
	if n.GetSyncType() != HistorySyncType_HISTORY_SYNC_INITIAL_BOOTSTRAP {
		t.Errorf("syncType = %v", n.GetSyncType())
	}
	if got.GetType() != ProtocolMessage_HISTORY_SYNC_NOTIFICATION {
		t.Errorf("type = %v", got.GetType())
	}
}

// TestSyncActionValueNewActionsRoundTrip: the newly typed chat actions
// (markChatAsRead/clear/delete) round-trip with their message ranges.
func TestSyncActionValueNewActionsRoundTrip(t *testing.T) {
	rng := &SyncActionValue_SyncActionMessageRange{
		LastMessageTimestamp: proto.Int64(1700000000),
		Messages: []*SyncActionValue_SyncActionMessage{
			{
				Key:       &MessageKey{Id: proto.String("M1"), FromMe: proto.Bool(true)},
				Timestamp: proto.Int64(1699999999),
			},
		},
	}
	orig := &SyncActionValue{
		Timestamp: proto.Int64(1700000001),
		MarkChatAsReadAction: &SyncActionValue_MarkChatAsReadAction{
			Read:         proto.Bool(true),
			MessageRange: rng,
		},
		ClearChatAction: &SyncActionValue_ClearChatAction{
			MessageRange: rng,
		},
		DeleteChatAction: &SyncActionValue_DeleteChatAction{},
		DeleteMessageForMeAction: &SyncActionValue_DeleteMessageForMeAction{
			DeleteMedia:      proto.Bool(true),
			MessageTimestamp: proto.Int64(1234),
		},
	}

	b, err := proto.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got SyncActionValue
	if err := proto.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !got.GetMarkChatAsReadAction().GetRead() {
		t.Errorf("markChatAsRead.read = false")
	}
	if got.GetMarkChatAsReadAction().GetMessageRange().GetLastMessageTimestamp() != 1700000000 {
		t.Errorf("messageRange.lastMessageTimestamp mismatch")
	}
	if len(got.GetMarkChatAsReadAction().GetMessageRange().GetMessages()) != 1 {
		t.Errorf("messageRange.messages len")
	}
	if got.GetClearChatAction() == nil {
		t.Errorf("clearChatAction nil")
	}
	if got.GetDeleteChatAction() == nil {
		t.Errorf("deleteChatAction nil")
	}
	if !got.GetDeleteMessageForMeAction().GetDeleteMedia() || got.GetDeleteMessageForMeAction().GetMessageTimestamp() != 1234 {
		t.Errorf("deleteMessageForMe mismatch")
	}
}
