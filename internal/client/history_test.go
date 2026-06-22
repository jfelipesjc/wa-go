package client

import (
	"bytes"
	"compress/zlib"
	"path/filepath"
	"testing"

	"github.com/felipeleal/wa-go/internal/store"
	"github.com/felipeleal/wa-go/internal/waproto"
	"google.golang.org/protobuf/proto"
)

// zlibCompress mirrors the server's compression of a serialized HistorySync.
func zlibCompress(t *testing.T, raw []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		t.Fatalf("zlib write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zlib close: %v", err)
	}
	return buf.Bytes()
}

// TestDecodeHistorySync: a zlib-compressed serialized HistorySync decodes back to
// the right conversations/messages.
func TestDecodeHistorySync(t *testing.T) {
	hs := &waproto.HistorySync{
		SyncType: waproto.HistorySyncType_HISTORY_SYNC_RECENT,
		Conversations: []*waproto.Conversation{
			{
				Id:   "5511999999999@s.whatsapp.net",
				Name: proto.String("Alice"),
				Messages: []*waproto.HistorySyncMsg{
					{
						Message: &waproto.WebMessageInfo{
							Key: &waproto.MessageKey{Id: proto.String("MSG1")},
							Message: &waproto.Message{
								Conversation: proto.String("bom dia"),
							},
						},
					},
				},
			},
		},
		Pushnames: []*waproto.Pushname{
			{Id: proto.String("x@s.whatsapp.net"), Pushname: proto.String("X")},
		},
	}
	raw, err := proto.Marshal(hs)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	compressed := zlibCompress(t, raw)

	got, err := DecodeHistorySync(compressed)
	if err != nil {
		t.Fatalf("DecodeHistorySync: %v", err)
	}
	if got.GetSyncType() != waproto.HistorySyncType_HISTORY_SYNC_RECENT {
		t.Errorf("syncType = %v", got.GetSyncType())
	}
	if len(got.GetConversations()) != 1 {
		t.Fatalf("conversations = %d", len(got.GetConversations()))
	}
	conv := got.GetConversations()[0]
	if conv.GetId() != "5511999999999@s.whatsapp.net" || conv.GetName() != "Alice" {
		t.Errorf("conversation mismatch: %+v", conv)
	}
	if len(conv.GetMessages()) != 1 {
		t.Fatalf("messages = %d", len(conv.GetMessages()))
	}
	if txt := conv.GetMessages()[0].GetMessage().GetMessage().GetConversation(); txt != "bom dia" {
		t.Errorf("text = %q", txt)
	}
	if len(got.GetPushnames()) != 1 {
		t.Errorf("pushnames = %d", len(got.GetPushnames()))
	}
}

func TestDecodeHistorySyncErrors(t *testing.T) {
	if _, err := DecodeHistorySync(nil); err == nil {
		t.Error("expected error for empty blob")
	}
	if _, err := DecodeHistorySync([]byte("not zlib")); err == nil {
		t.Error("expected error for non-zlib blob")
	}
}

// TestEmitHistorySyncEvent: emitHistorySync surfaces a HistorySyncEvent with the
// decoded conversations.
func TestEmitHistorySyncEvent(t *testing.T) {
	c := New(nil)
	events := c.Events()

	hs := &waproto.HistorySync{
		SyncType:   waproto.HistorySyncType_HISTORY_SYNC_FULL,
		Progress:   proto.Uint32(100),
		ChunkOrder: proto.Uint32(2),
		Conversations: []*waproto.Conversation{
			{Id: "a@s.whatsapp.net"},
		},
	}
	go c.emitHistorySync(hs)

	ev := <-events
	hse, ok := ev.(HistorySyncEvent)
	if !ok {
		t.Fatalf("event type = %T, want HistorySyncEvent", ev)
	}
	if hse.SyncType != waproto.HistorySyncType_HISTORY_SYNC_FULL {
		t.Errorf("SyncType = %v", hse.SyncType)
	}
	if hse.Progress != 100 || hse.ChunkOrder != 2 {
		t.Errorf("progress/chunkOrder = %d/%d", hse.Progress, hse.ChunkOrder)
	}
	if len(hse.Conversations) != 1 || hse.Conversations[0].GetId() != "a@s.whatsapp.net" {
		t.Errorf("conversations mismatch: %+v", hse.Conversations)
	}
}

// TestAppStateSyncKeyShareHandler: an APP_STATE_SYNC_KEY_SHARE protocolMessage
// run through handleProtocolSideEffects persists each key into the store and is
// treated as a pure side effect.
func TestAppStateSyncKeyShareHandler(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer st.Close()
	c := New(st)

	keyID := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	keyData := bytes.Repeat([]byte{0x42}, 32)

	pm := &waproto.ProtocolMessage{
		Type: waproto.ProtocolMessage_APP_STATE_SYNC_KEY_SHARE.Enum(),
		AppStateSyncKeyShare: &waproto.AppStateSyncKeyShare{
			Keys: []*waproto.AppStateSyncKey{
				{
					KeyId:   &waproto.AppStateSyncKeyId{KeyId: keyID},
					KeyData: &waproto.AppStateSyncKeyData{KeyData: keyData},
				},
			},
		},
	}

	if handled := c.handleProtocolSideEffects(pm); !handled {
		t.Fatal("expected handleProtocolSideEffects to report handled")
	}

	got, ok, err := st.LoadAppStateSyncKey(keyID)
	if err != nil || !ok {
		t.Fatalf("LoadAppStateSyncKey: ok=%v err=%v", ok, err)
	}
	if !bytes.Equal(got, keyData) {
		t.Fatalf("stored keyData = %x, want %x", got, keyData)
	}
}
