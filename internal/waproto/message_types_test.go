package waproto

import (
	"bytes"
	"testing"

	"google.golang.org/protobuf/proto"
)

// TestImageMessageRoundTrip: a Message carrying an ImageMessage marshals and
// unmarshals back with all fields preserved.
func TestImageMessageRoundTrip(t *testing.T) {
	orig := &Message{
		ImageMessage: &ImageMessage{
			Url:               proto.String("https://mmg.whatsapp.net/d/f/abc.enc"),
			Mimetype:          proto.String("image/jpeg"),
			Caption:           proto.String("hello"),
			FileSha256:        []byte{1, 2, 3, 4},
			FileLength:        proto.Uint64(123456),
			Height:            proto.Uint32(1080),
			Width:             proto.Uint32(1920),
			MediaKey:          []byte{5, 6, 7, 8},
			FileEncSha256:     []byte{9, 10, 11, 12},
			DirectPath:        proto.String("/d/f/abc.enc"),
			MediaKeyTimestamp: proto.Int64(1700000000),
			JpegThumbnail:     []byte{0xFF, 0xD8, 0xFF},
		},
	}

	b, err := proto.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Message
	if err := proto.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	im := got.GetImageMessage()
	if im == nil {
		t.Fatal("imageMessage missing after round-trip")
	}
	if im.GetUrl() != "https://mmg.whatsapp.net/d/f/abc.enc" {
		t.Errorf("url = %q", im.GetUrl())
	}
	if im.GetMimetype() != "image/jpeg" {
		t.Errorf("mimetype = %q", im.GetMimetype())
	}
	if im.GetCaption() != "hello" {
		t.Errorf("caption = %q", im.GetCaption())
	}
	if im.GetFileLength() != 123456 {
		t.Errorf("fileLength = %d", im.GetFileLength())
	}
	if im.GetHeight() != 1080 || im.GetWidth() != 1920 {
		t.Errorf("height/width = %d/%d", im.GetHeight(), im.GetWidth())
	}
	if !bytes.Equal(im.GetFileSha256(), []byte{1, 2, 3, 4}) {
		t.Errorf("fileSha256 = %v", im.GetFileSha256())
	}
	if !bytes.Equal(im.GetMediaKey(), []byte{5, 6, 7, 8}) {
		t.Errorf("mediaKey = %v", im.GetMediaKey())
	}
	if im.GetMediaKeyTimestamp() != 1700000000 {
		t.Errorf("mediaKeyTimestamp = %d", im.GetMediaKeyTimestamp())
	}
}

// TestReactionMessageRoundTrip: a Message carrying a ReactionMessage with a
// MessageKey survives a round-trip.
func TestReactionMessageRoundTrip(t *testing.T) {
	orig := &Message{
		ReactionMessage: &ReactionMessage{
			Key: &MessageKey{
				RemoteJid:   proto.String("5511999999999@s.whatsapp.net"),
				FromMe:      proto.Bool(false),
				Id:          proto.String("3EB0ABC123"),
				Participant: proto.String("5511888888888@s.whatsapp.net"),
			},
			Text:              proto.String("\U0001F44D"),
			SenderTimestampMs: proto.Int64(1700000123456),
		},
	}

	b, err := proto.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Message
	if err := proto.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	rm := got.GetReactionMessage()
	if rm == nil {
		t.Fatal("reactionMessage missing after round-trip")
	}
	if rm.GetText() != "\U0001F44D" {
		t.Errorf("text = %q", rm.GetText())
	}
	if rm.GetSenderTimestampMs() != 1700000123456 {
		t.Errorf("senderTimestampMs = %d", rm.GetSenderTimestampMs())
	}
	k := rm.GetKey()
	if k == nil {
		t.Fatal("reaction key missing")
	}
	if k.GetRemoteJid() != "5511999999999@s.whatsapp.net" {
		t.Errorf("remoteJid = %q", k.GetRemoteJid())
	}
	if k.GetId() != "3EB0ABC123" {
		t.Errorf("id = %q", k.GetId())
	}
	if k.GetFromMe() != false {
		t.Errorf("fromMe = %v", k.GetFromMe())
	}
	if k.GetParticipant() != "5511888888888@s.whatsapp.net" {
		t.Errorf("participant = %q", k.GetParticipant())
	}
}

// TestExtendedTextReplyRoundTrip: an extendedTextMessage that quotes another
// message (a reply) via ContextInfo, plus a mention, round-trips correctly.
func TestExtendedTextReplyRoundTrip(t *testing.T) {
	orig := &Message{
		ExtendedTextMessage: &ExtendedTextMessage{
			Text: proto.String("replying to you @5511888888888"),
			ContextInfo: &ContextInfo{
				StanzaId:    proto.String("ORIGINAL_MSG_ID"),
				Participant: proto.String("5511888888888@s.whatsapp.net"),
				QuotedMessage: &Message{
					Conversation: proto.String("the original text"),
				},
				MentionedJid: []string{"5511888888888@s.whatsapp.net"},
			},
		},
	}

	b, err := proto.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Message
	if err := proto.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	et := got.GetExtendedTextMessage()
	if et == nil {
		t.Fatal("extendedTextMessage missing after round-trip")
	}
	if et.GetText() != "replying to you @5511888888888" {
		t.Errorf("text = %q", et.GetText())
	}
	ci := et.GetContextInfo()
	if ci == nil {
		t.Fatal("contextInfo missing")
	}
	if ci.GetStanzaId() != "ORIGINAL_MSG_ID" {
		t.Errorf("stanzaId = %q", ci.GetStanzaId())
	}
	if ci.GetParticipant() != "5511888888888@s.whatsapp.net" {
		t.Errorf("participant = %q", ci.GetParticipant())
	}
	if qm := ci.GetQuotedMessage(); qm == nil || qm.GetConversation() != "the original text" {
		t.Errorf("quotedMessage = %v", qm)
	}
	if len(ci.GetMentionedJid()) != 1 || ci.GetMentionedJid()[0] != "5511888888888@s.whatsapp.net" {
		t.Errorf("mentionedJid = %v", ci.GetMentionedJid())
	}
}
