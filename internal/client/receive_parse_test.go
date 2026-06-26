package client

import (
	"testing"

	"github.com/jfelipesjc/wa-go/internal/waproto"
	"google.golang.org/protobuf/proto"
)

// TestParseConversation: a bare text body.
func TestParseConversation(t *testing.T) {
	ev := parseMessage(&waproto.Message{Conversation: proto.String("hello")})
	if ev.Type != MessageText {
		t.Fatalf("type = %q, want text", ev.Type)
	}
	if ev.Text != "hello" {
		t.Fatalf("text = %q", ev.Text)
	}
}

// TestParseExtendedTextReply: text + reply context (quoted) + mentions.
func TestParseExtendedTextReply(t *testing.T) {
	m := &waproto.Message{
		ExtendedTextMessage: &waproto.ExtendedTextMessage{
			Text: proto.String("replying"),
			ContextInfo: &waproto.ContextInfo{
				StanzaId:      proto.String("QUOTED1"),
				Participant:   proto.String("5551111111@s.whatsapp.net"),
				MentionedJid:  []string{"5552222222@s.whatsapp.net", "5553333333@s.whatsapp.net"},
				QuotedMessage: &waproto.Message{Conversation: proto.String("original text")},
			},
		},
	}
	ev := parseMessage(m)
	if ev.Type != MessageText || ev.Text != "replying" {
		t.Fatalf("type/text = %q/%q", ev.Type, ev.Text)
	}
	if ev.Quoted == nil {
		t.Fatal("no Quoted")
	}
	if ev.Quoted.StanzaID != "QUOTED1" || ev.Quoted.Participant != "5551111111@s.whatsapp.net" {
		t.Fatalf("quoted key wrong: %+v", ev.Quoted)
	}
	if ev.Quoted.Text != "original text" {
		t.Fatalf("quoted text = %q", ev.Quoted.Text)
	}
	if len(ev.Mentions) != 2 || ev.Mentions[0] != "5552222222@s.whatsapp.net" {
		t.Fatalf("mentions = %v", ev.Mentions)
	}
}

// TestParseImage: media metadata + caption -> Text.
func TestParseImage(t *testing.T) {
	mediaKey := []byte("0123456789abcdef0123456789abcdef")
	m := &waproto.Message{
		ImageMessage: &waproto.ImageMessage{
			Mimetype:      proto.String("image/jpeg"),
			Caption:       proto.String("nice pic"),
			FileLength:    proto.Uint64(12345),
			MediaKey:      mediaKey,
			DirectPath:    proto.String("/v/t62/path"),
			Url:           proto.String("https://mmg.whatsapp.net/x"),
			FileSha256:    []byte("sha256sha256sha256sha256sha256!!"),
			FileEncSha256: []byte("enc256enc256enc256enc256enc256!!"),
			Width:         proto.Uint32(640),
			Height:        proto.Uint32(480),
		},
	}
	ev := parseMessage(m)
	if ev.Type != MessageImage {
		t.Fatalf("type = %q", ev.Type)
	}
	if ev.Text != "nice pic" {
		t.Fatalf("caption->text = %q", ev.Text)
	}
	if ev.Media == nil {
		t.Fatal("no Media")
	}
	md := ev.Media
	if md.Kind != MediaImage || md.Mimetype != "image/jpeg" || md.FileLength != 12345 {
		t.Fatalf("media metadata wrong: %+v", md)
	}
	if md.Width != 640 || md.Height != 480 {
		t.Fatalf("dims = %dx%d", md.Width, md.Height)
	}
	if string(md.MediaKey) != string(mediaKey) || md.DirectPath != "/v/t62/path" {
		t.Fatalf("download metadata wrong: %+v", md)
	}
	if len(md.FileSha256) != 32 || len(md.FileEncSha256) != 32 {
		t.Fatalf("sha lengths %d/%d", len(md.FileSha256), len(md.FileEncSha256))
	}
}

// TestParseAudioPTT: voice note metadata.
func TestParseAudioPTT(t *testing.T) {
	m := &waproto.Message{
		AudioMessage: &waproto.AudioMessage{
			Mimetype: proto.String("audio/ogg; codecs=opus"),
			Seconds:  proto.Uint32(7),
			Ptt:      proto.Bool(true),
			MediaKey: []byte("k"),
		},
	}
	ev := parseMessage(m)
	if ev.Type != MessageAudio || ev.Media == nil {
		t.Fatalf("type/media = %q/%v", ev.Type, ev.Media)
	}
	if !ev.Media.IsPTT || ev.Media.Seconds != 7 {
		t.Fatalf("ptt/seconds = %v/%d", ev.Media.IsPTT, ev.Media.Seconds)
	}
}

// TestParseDocument: file name + page count.
func TestParseDocument(t *testing.T) {
	m := &waproto.Message{
		DocumentMessage: &waproto.DocumentMessage{
			Mimetype:  proto.String("application/pdf"),
			FileName:  proto.String("report.pdf"),
			PageCount: proto.Uint32(10),
			Caption:   proto.String("see attached"),
		},
	}
	ev := parseMessage(m)
	if ev.Type != MessageDocument || ev.Media == nil {
		t.Fatal("not a document")
	}
	if ev.Media.FileName != "report.pdf" || ev.Media.PageCount != 10 {
		t.Fatalf("doc metadata: %+v", ev.Media)
	}
	if ev.Text != "see attached" {
		t.Fatalf("caption = %q", ev.Text)
	}
}

// TestParseSticker.
func TestParseSticker(t *testing.T) {
	m := &waproto.Message{
		StickerMessage: &waproto.StickerMessage{
			Mimetype:   proto.String("image/webp"),
			IsAnimated: proto.Bool(true),
			Width:      proto.Uint32(512),
			Height:     proto.Uint32(512),
		},
	}
	ev := parseMessage(m)
	if ev.Type != MessageSticker || ev.Media == nil {
		t.Fatal("not sticker")
	}
	if !ev.Media.IsAnimated || ev.Media.Kind != MediaSticker {
		t.Fatalf("sticker metadata: %+v", ev.Media)
	}
}

// TestParseLocation.
func TestParseLocation(t *testing.T) {
	m := &waproto.Message{
		LocationMessage: &waproto.LocationMessage{
			DegreesLatitude:  proto.Float64(-23.5),
			DegreesLongitude: proto.Float64(-46.6),
			Name:             proto.String("Sao Jose dos Campos"),
			Address:          proto.String("SP, Brazil"),
		},
	}
	ev := parseMessage(m)
	if ev.Type != MessageLocation || ev.Location == nil {
		t.Fatal("not location")
	}
	if ev.Location.Latitude != -23.5 || ev.Location.Longitude != -46.6 {
		t.Fatalf("coords = %v,%v", ev.Location.Latitude, ev.Location.Longitude)
	}
	if ev.Location.Name != "Sao Jose dos Campos" {
		t.Fatalf("name = %q", ev.Location.Name)
	}
}

// TestParseContact.
func TestParseContact(t *testing.T) {
	m := &waproto.Message{
		ContactMessage: &waproto.ContactMessage{
			DisplayName: proto.String("Bruno"),
			Vcard:       proto.String("BEGIN:VCARD\nFN:Bruno\nEND:VCARD"),
		},
	}
	ev := parseMessage(m)
	if ev.Type != MessageContact || ev.Contact == nil {
		t.Fatal("not contact")
	}
	if ev.Contact.DisplayName != "Bruno" {
		t.Fatalf("name = %q", ev.Contact.DisplayName)
	}
}

// TestParseReaction.
func TestParseReaction(t *testing.T) {
	m := &waproto.Message{
		ReactionMessage: &waproto.ReactionMessage{
			Key: &waproto.MessageKey{
				Id:        proto.String("TARGET1"),
				FromMe:    proto.Bool(true),
				RemoteJid: proto.String("5551111111@s.whatsapp.net"),
			},
			Text: proto.String("👍"),
		},
	}
	ev := parseMessage(m)
	if ev.Type != MessageReaction || ev.Reaction == nil {
		t.Fatal("not reaction")
	}
	if ev.Reaction.Text != "👍" {
		t.Fatalf("emoji = %q", ev.Reaction.Text)
	}
	if ev.Reaction.Key.ID != "TARGET1" || !ev.Reaction.Key.FromMe {
		t.Fatalf("reaction key = %+v", ev.Reaction.Key)
	}
}

// TestParseRevoke: protocolMessage type REVOKE -> delete event.
func TestParseRevoke(t *testing.T) {
	m := &waproto.Message{
		ProtocolMessage: &waproto.ProtocolMessage{
			Type: waproto.ProtocolMessage_REVOKE.Enum(),
			Key: &waproto.MessageKey{
				Id:        proto.String("DELME"),
				RemoteJid: proto.String("5551111111@s.whatsapp.net"),
			},
		},
	}
	ev := parseMessage(m)
	if ev.Type != MessageRevoke || ev.Revoked == nil {
		t.Fatalf("type/revoked = %q/%v", ev.Type, ev.Revoked)
	}
	if ev.Revoked.ID != "DELME" {
		t.Fatalf("revoked id = %q", ev.Revoked.ID)
	}
}

// TestParseEdit: protocolMessage type MESSAGE_EDIT carrying the new content.
func TestParseEdit(t *testing.T) {
	m := &waproto.Message{
		ProtocolMessage: &waproto.ProtocolMessage{
			Type: waproto.ProtocolMessage_MESSAGE_EDIT.Enum(),
			Key:  &waproto.MessageKey{Id: proto.String("EDITME")},
			EditedMessage: &waproto.Message{
				Conversation: proto.String("the corrected text"),
			},
		},
	}
	ev := parseMessage(m)
	if ev.Type != MessageEdit || ev.Edited == nil {
		t.Fatalf("type/edited = %q/%v", ev.Type, ev.Edited)
	}
	if ev.Edited.ID != "EDITME" {
		t.Fatalf("edited id = %q", ev.Edited.ID)
	}
	if ev.Text != "the corrected text" {
		t.Fatalf("edited text = %q", ev.Text)
	}
}

// TestParsePoll.
func TestParsePoll(t *testing.T) {
	m := &waproto.Message{
		PollCreationMessage: &waproto.PollCreationMessage{
			Name: proto.String("Favorite team?"),
			Options: []*waproto.PollCreationMessage_Option{
				{OptionName: proto.String("Palmeiras")},
				{OptionName: proto.String("Corinthians")},
			},
			SelectableOptionsCount: proto.Uint32(1),
		},
	}
	ev := parseMessage(m)
	if ev.Type != MessagePoll || ev.Poll == nil {
		t.Fatal("not poll")
	}
	if ev.Poll.Name != "Favorite team?" || len(ev.Poll.Options) != 2 {
		t.Fatalf("poll = %+v", ev.Poll)
	}
	if ev.Poll.Options[1] != "Corinthians" {
		t.Fatalf("option = %q", ev.Poll.Options[1])
	}
}

// TestParseDeviceSentUnwrap: deviceSentMessage is unwrapped to its inner content.
func TestParseDeviceSentUnwrap(t *testing.T) {
	m := &waproto.Message{
		DeviceSentMessage: &waproto.DeviceSentMessage{
			DestinationJid: proto.String("5551111111@s.whatsapp.net"),
			Message:        &waproto.Message{Conversation: proto.String("from my phone")},
		},
	}
	ev := parseMessage(m)
	if ev.Type != MessageText || ev.Text != "from my phone" {
		t.Fatalf("device-sent unwrap: type=%q text=%q", ev.Type, ev.Text)
	}
}

// TestParseRoundTripWire: a serialized+unmarshaled Message parses the same way
// (exercises the real decode path).
func TestParseRoundTripWire(t *testing.T) {
	orig := &waproto.Message{ImageMessage: &waproto.ImageMessage{
		Mimetype: proto.String("image/png"),
		Caption:  proto.String("wire"),
	}}
	raw, err := proto.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	var m waproto.Message
	if err := proto.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	ev := parseMessage(&m)
	if ev.Type != MessageImage || ev.Text != "wire" {
		t.Fatalf("round trip: type=%q text=%q", ev.Type, ev.Text)
	}
}
