package client

import (
	"testing"

	"github.com/jfelipesjc/wa-go/internal/waproto"
	"google.golang.org/protobuf/proto"
)

// TestParseViewOnceImage: a viewOnceMessage wrapping an image must unwrap to the
// inner image and set the ViewOnce flag.
func TestParseViewOnceImage(t *testing.T) {
	inner := &waproto.Message{
		ImageMessage: &waproto.ImageMessage{
			Mimetype: proto.String("image/jpeg"),
			Caption:  proto.String("secret"),
		},
	}
	m := &waproto.Message{
		ViewOnceMessage: &waproto.FutureProofMessage{Message: inner},
	}
	ev := parseMessage(m)
	if ev.Type != MessageImage {
		t.Fatalf("type = %q, want image", ev.Type)
	}
	if !ev.ViewOnce {
		t.Fatal("ViewOnce flag not set")
	}
	if ev.Ephemeral {
		t.Fatal("Ephemeral should be false")
	}
	if ev.Text != "secret" {
		t.Fatalf("caption text = %q", ev.Text)
	}
	if ev.Media == nil || ev.Media.Mimetype != "image/jpeg" {
		t.Fatalf("media not unwrapped: %+v", ev.Media)
	}
}

// TestParseViewOnceV2: viewOnceMessageV2 must also set the ViewOnce flag.
func TestParseViewOnceV2(t *testing.T) {
	m := &waproto.Message{
		ViewOnceMessageV2: &waproto.FutureProofMessage{
			Message: &waproto.Message{Conversation: proto.String("once")},
		},
	}
	ev := parseMessage(m)
	if ev.Type != MessageText || ev.Text != "once" {
		t.Fatalf("type/text = %q/%q", ev.Type, ev.Text)
	}
	if !ev.ViewOnce {
		t.Fatal("ViewOnce flag not set for v2")
	}
}

// TestParseEphemeralText: ephemeralMessage wrapping a text body must unwrap and
// set the Ephemeral flag.
func TestParseEphemeralText(t *testing.T) {
	m := &waproto.Message{
		EphemeralMessage: &waproto.FutureProofMessage{
			Message: &waproto.Message{Conversation: proto.String("disappearing")},
		},
	}
	ev := parseMessage(m)
	if ev.Type != MessageText || ev.Text != "disappearing" {
		t.Fatalf("type/text = %q/%q", ev.Type, ev.Text)
	}
	if !ev.Ephemeral {
		t.Fatal("Ephemeral flag not set")
	}
	if ev.ViewOnce {
		t.Fatal("ViewOnce should be false")
	}
}

// TestParseEphemeralViewOnce: nested ephemeral around view-once must set both
// flags and reach the inner content.
func TestParseEphemeralViewOnce(t *testing.T) {
	m := &waproto.Message{
		EphemeralMessage: &waproto.FutureProofMessage{
			Message: &waproto.Message{
				ViewOnceMessageV2: &waproto.FutureProofMessage{
					Message: &waproto.Message{Conversation: proto.String("both")},
				},
			},
		},
	}
	ev := parseMessage(m)
	if ev.Text != "both" {
		t.Fatalf("text = %q", ev.Text)
	}
	if !ev.Ephemeral || !ev.ViewOnce {
		t.Fatalf("flags ephemeral=%v viewOnce=%v, want both true", ev.Ephemeral, ev.ViewOnce)
	}
}

// TestParseDocumentWithCaption: documentWithCaptionMessage is a pure container;
// it unwraps to the document without setting view-once/ephemeral.
func TestParseDocumentWithCaption(t *testing.T) {
	m := &waproto.Message{
		DocumentWithCaptionMessage: &waproto.FutureProofMessage{
			Message: &waproto.Message{
				DocumentMessage: &waproto.DocumentMessage{
					FileName: proto.String("file.pdf"),
					Caption:  proto.String("see attached"),
				},
			},
		},
	}
	ev := parseMessage(m)
	if ev.Type != MessageDocument {
		t.Fatalf("type = %q, want document", ev.Type)
	}
	if ev.ViewOnce || ev.Ephemeral {
		t.Fatalf("flags should be false: viewOnce=%v ephemeral=%v", ev.ViewOnce, ev.Ephemeral)
	}
	if ev.Media == nil || ev.Media.FileName != "file.pdf" {
		t.Fatalf("doc not unwrapped: %+v", ev.Media)
	}
}

// TestParseButtonsMessage: a buttonsMessage becomes MessageButtons with the
// content text and the list of buttons.
func TestParseButtonsMessage(t *testing.T) {
	m := &waproto.Message{
		ButtonsMessage: &waproto.ButtonsMessage{
			ContentText: proto.String("Choose:"),
			Buttons: []*waproto.ButtonsMessage_Button{
				{
					ButtonId:   proto.String("yes"),
					ButtonText: &waproto.ButtonsMessage_Button_ButtonText{DisplayText: proto.String("Yes")},
				},
				{
					ButtonId:   proto.String("no"),
					ButtonText: &waproto.ButtonsMessage_Button_ButtonText{DisplayText: proto.String("No")},
				},
			},
		},
	}
	ev := parseMessage(m)
	if ev.Type != MessageButtons {
		t.Fatalf("type = %q, want buttons", ev.Type)
	}
	if ev.Text != "Choose:" {
		t.Fatalf("text = %q", ev.Text)
	}
	if len(ev.Buttons) != 2 {
		t.Fatalf("buttons = %d, want 2", len(ev.Buttons))
	}
	if ev.Buttons[0].ID != "yes" || ev.Buttons[0].Text != "Yes" {
		t.Fatalf("button[0] = %+v", ev.Buttons[0])
	}
	if ev.Buttons[1].ID != "no" || ev.Buttons[1].Text != "No" {
		t.Fatalf("button[1] = %+v", ev.Buttons[1])
	}
}

// TestParseListMessage: a listMessage becomes MessageList with sections/rows.
func TestParseListMessage(t *testing.T) {
	m := &waproto.Message{
		ListMessage: &waproto.ListMessage{
			Title:       proto.String("Menu"),
			Description: proto.String("Pick one"),
			ButtonText:  proto.String("Open"),
			Sections: []*waproto.ListMessage_Section{
				{
					Title: proto.String("Drinks"),
					Rows: []*waproto.ListMessage_Row{
						{RowId: proto.String("coffee"), Title: proto.String("Coffee"), Description: proto.String("hot")},
						{RowId: proto.String("tea"), Title: proto.String("Tea")},
					},
				},
			},
		},
	}
	ev := parseMessage(m)
	if ev.Type != MessageList {
		t.Fatalf("type = %q, want list", ev.Type)
	}
	if ev.Text != "Pick one" {
		t.Fatalf("text = %q", ev.Text)
	}
	if ev.List == nil {
		t.Fatal("List nil")
	}
	if ev.List.Title != "Menu" || ev.List.ButtonText != "Open" {
		t.Fatalf("list header = %+v", ev.List)
	}
	if len(ev.List.Sections) != 1 || len(ev.List.Sections[0].Rows) != 2 {
		t.Fatalf("sections/rows shape wrong: %+v", ev.List.Sections)
	}
	r0 := ev.List.Sections[0].Rows[0]
	if r0.RowID != "coffee" || r0.Title != "Coffee" || r0.Description != "hot" {
		t.Fatalf("row[0] = %+v", r0)
	}
}

// TestParseTemplateMessage: a templateMessage becomes MessageTemplate with the
// hydrated content text and quick-reply buttons.
func TestParseTemplateMessage(t *testing.T) {
	m := &waproto.Message{
		TemplateMessage: &waproto.TemplateMessage{
			HydratedTemplate: &waproto.TemplateMessage_HydratedFourRowTemplate{
				HydratedContentText: proto.String("Confirm?"),
				HydratedButtons: []*waproto.TemplateMessage_HydratedButton{
					{
						QuickReplyButton: &waproto.TemplateMessage_HydratedButton_HydratedQuickReplyButton{
							Id:          proto.String("ok"),
							DisplayText: proto.String("OK"),
						},
					},
				},
			},
		},
	}
	ev := parseMessage(m)
	if ev.Type != MessageTemplate {
		t.Fatalf("type = %q, want template", ev.Type)
	}
	if ev.Text != "Confirm?" {
		t.Fatalf("text = %q", ev.Text)
	}
	if len(ev.Buttons) != 1 || ev.Buttons[0].ID != "ok" || ev.Buttons[0].Text != "OK" {
		t.Fatalf("buttons = %+v", ev.Buttons)
	}
}

// TestParseInteractiveMessage: an interactiveMessage becomes MessageInteractive
// with the body text.
func TestParseInteractiveMessage(t *testing.T) {
	m := &waproto.Message{
		InteractiveMessage: &waproto.InteractiveMessage{
			Body: &waproto.InteractiveMessage_Body{Text: proto.String("flow body")},
		},
	}
	ev := parseMessage(m)
	if ev.Type != MessageInteractive {
		t.Fatalf("type = %q, want interactive", ev.Type)
	}
	if ev.Text != "flow body" {
		t.Fatalf("text = %q", ev.Text)
	}
}

// TestParseButtonsResponse: a buttonsResponseMessage becomes MessageButtonReply.
func TestParseButtonsResponse(t *testing.T) {
	m := &waproto.Message{
		ButtonsResponseMessage: &waproto.ButtonsResponseMessage{
			SelectedButtonId:    proto.String("yes"),
			SelectedDisplayText: proto.String("Yes"),
		},
	}
	ev := parseMessage(m)
	if ev.Type != MessageButtonReply {
		t.Fatalf("type = %q, want button_reply", ev.Type)
	}
	if ev.ButtonReply == nil || ev.ButtonReply.SelectedID != "yes" || ev.ButtonReply.Text != "Yes" {
		t.Fatalf("ButtonReply = %+v", ev.ButtonReply)
	}
}

// TestParseListResponse: a listResponseMessage becomes MessageListReply.
func TestParseListResponse(t *testing.T) {
	m := &waproto.Message{
		ListResponseMessage: &waproto.ListResponseMessage{
			Title: proto.String("Coffee"),
			SingleSelectReply: &waproto.ListResponseMessage_SingleSelectReply{
				SelectedRowId: proto.String("coffee"),
			},
		},
	}
	ev := parseMessage(m)
	if ev.Type != MessageListReply {
		t.Fatalf("type = %q, want list_reply", ev.Type)
	}
	if ev.ListReply == nil || ev.ListReply.RowID != "coffee" || ev.ListReply.Title != "Coffee" {
		t.Fatalf("ListReply = %+v", ev.ListReply)
	}
}

// TestParseTemplateButtonReply: a templateButtonReplyMessage becomes
// MessageTemplateReply carrying the selected id/text in ButtonReply.
func TestParseTemplateButtonReply(t *testing.T) {
	m := &waproto.Message{
		TemplateButtonReplyMessage: &waproto.TemplateButtonReplyMessage{
			SelectedId:          proto.String("ok"),
			SelectedDisplayText: proto.String("OK"),
		},
	}
	ev := parseMessage(m)
	if ev.Type != MessageTemplateReply {
		t.Fatalf("type = %q, want template_reply", ev.Type)
	}
	if ev.ButtonReply == nil || ev.ButtonReply.SelectedID != "ok" || ev.ButtonReply.Text != "OK" {
		t.Fatalf("ButtonReply = %+v", ev.ButtonReply)
	}
}

// TestParseInteractiveResponse: an interactiveResponseMessage becomes
// MessageInteractiveReply with body text and native-flow params.
func TestParseInteractiveResponse(t *testing.T) {
	m := &waproto.Message{
		InteractiveResponseMessage: &waproto.InteractiveResponseMessage{
			Body: &waproto.InteractiveResponseMessage_Body{Text: proto.String("done")},
			NativeFlowResponseMessage: &waproto.InteractiveResponseMessage_NativeFlowResponseMessage{
				Name:       proto.String("address"),
				ParamsJson: proto.String(`{"k":"v"}`),
			},
		},
	}
	ev := parseMessage(m)
	if ev.Type != MessageInteractiveReply {
		t.Fatalf("type = %q, want interactive_reply", ev.Type)
	}
	if ev.InteractiveReply == nil {
		t.Fatal("InteractiveReply nil")
	}
	if ev.InteractiveReply.Text != "done" || ev.InteractiveReply.Name != "address" || ev.InteractiveReply.ParamsJSON != `{"k":"v"}` {
		t.Fatalf("InteractiveReply = %+v", ev.InteractiveReply)
	}
}

// TestParsePollUpdate: a pollUpdateMessage becomes MessagePollVote exposing the
// poll key reference and the encrypted vote bytes (without decrypting).
func TestParsePollUpdate(t *testing.T) {
	m := &waproto.Message{
		PollUpdateMessage: &waproto.PollUpdateMessage{
			PollCreationMessageKey: &waproto.MessageKey{Id: proto.String("POLL1")},
			Vote: &waproto.PollUpdateMessage_PollEncValue{
				EncPayload: []byte{1, 2, 3},
				EncIv:      []byte{4, 5, 6},
			},
		},
	}
	ev := parseMessage(m)
	if ev.Type != MessagePollVote {
		t.Fatalf("type = %q, want poll_vote", ev.Type)
	}
	if ev.PollVote == nil {
		t.Fatal("PollVote nil")
	}
	if ev.PollVote.PollKey.ID != "POLL1" {
		t.Fatalf("poll key = %+v", ev.PollVote.PollKey)
	}
	if string(ev.PollVote.EncPayload) != string([]byte{1, 2, 3}) || string(ev.PollVote.EncIV) != string([]byte{4, 5, 6}) {
		t.Fatalf("enc bytes = %+v", ev.PollVote)
	}
}
