package waproto

import (
	"testing"

	"google.golang.org/protobuf/proto"
)

// TestViewOnceWrapperRoundTrip: a Message wrapping an inner Message (with an
// ImageMessage) inside a viewOnceMessage FutureProofMessage survives a
// marshal/unmarshal round-trip with the nested image preserved.
func TestViewOnceWrapperRoundTrip(t *testing.T) {
	orig := &Message{
		ViewOnceMessage: &FutureProofMessage{
			Message: &Message{
				ImageMessage: &ImageMessage{
					Url:      proto.String("https://mmg.whatsapp.net/d/f/once.enc"),
					Mimetype: proto.String("image/jpeg"),
					Caption:  proto.String("view once"),
					ViewOnce: proto.Bool(true),
				},
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

	inner := got.GetViewOnceMessage().GetMessage()
	if inner == nil {
		t.Fatal("viewOnceMessage.message is nil")
	}
	im := inner.GetImageMessage()
	if im == nil {
		t.Fatal("inner imageMessage is nil")
	}
	if im.GetCaption() != "view once" {
		t.Errorf("caption = %q, want %q", im.GetCaption(), "view once")
	}
	if !im.GetViewOnce() {
		t.Error("viewOnce = false, want true")
	}
}

// TestButtonsMessageRoundTrip checks the nested buttons structure round-trips.
func TestButtonsMessageRoundTrip(t *testing.T) {
	orig := &Message{
		ButtonsMessage: &ButtonsMessage{
			ContentText: proto.String("Pick one"),
			FooterText:  proto.String("footer"),
			HeaderType:  ButtonsMessage_TEXT.Enum(),
			Text:        proto.String("Header"),
			Buttons: []*ButtonsMessage_Button{
				{
					ButtonId: proto.String("b1"),
					ButtonText: &ButtonsMessage_Button_ButtonText{
						DisplayText: proto.String("Yes"),
					},
					Type: ButtonsMessage_Button_RESPONSE.Enum(),
				},
				{
					ButtonId: proto.String("b2"),
					ButtonText: &ButtonsMessage_Button_ButtonText{
						DisplayText: proto.String("No"),
					},
					Type: ButtonsMessage_Button_RESPONSE.Enum(),
				},
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

	bm := got.GetButtonsMessage()
	if bm == nil {
		t.Fatal("buttonsMessage is nil")
	}
	if bm.GetContentText() != "Pick one" {
		t.Errorf("contentText = %q", bm.GetContentText())
	}
	if bm.GetText() != "Header" {
		t.Errorf("header text = %q", bm.GetText())
	}
	if bm.GetHeaderType() != ButtonsMessage_TEXT {
		t.Errorf("headerType = %v", bm.GetHeaderType())
	}
	btns := bm.GetButtons()
	if len(btns) != 2 {
		t.Fatalf("buttons len = %d, want 2", len(btns))
	}
	if btns[0].GetButtonId() != "b1" || btns[0].GetButtonText().GetDisplayText() != "Yes" {
		t.Errorf("button[0] = %+v", btns[0])
	}
	if btns[1].GetButtonText().GetDisplayText() != "No" {
		t.Errorf("button[1] displayText = %q", btns[1].GetButtonText().GetDisplayText())
	}
}

// TestListMessageRoundTrip checks the nested sections/rows round-trip.
func TestListMessageRoundTrip(t *testing.T) {
	orig := &Message{
		ListMessage: &ListMessage{
			Title:       proto.String("Menu"),
			Description: proto.String("Choose an item"),
			ButtonText:  proto.String("Open"),
			FooterText:  proto.String("footer"),
			ListType:    ListMessage_SINGLE_SELECT.Enum(),
			Sections: []*ListMessage_Section{
				{
					Title: proto.String("Drinks"),
					Rows: []*ListMessage_Row{
						{Title: proto.String("Coffee"), Description: proto.String("hot"), RowId: proto.String("r1")},
						{Title: proto.String("Tea"), Description: proto.String("warm"), RowId: proto.String("r2")},
					},
				},
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

	lm := got.GetListMessage()
	if lm == nil {
		t.Fatal("listMessage is nil")
	}
	if lm.GetTitle() != "Menu" || lm.GetButtonText() != "Open" {
		t.Errorf("list header = %q / %q", lm.GetTitle(), lm.GetButtonText())
	}
	if lm.GetListType() != ListMessage_SINGLE_SELECT {
		t.Errorf("listType = %v", lm.GetListType())
	}
	secs := lm.GetSections()
	if len(secs) != 1 {
		t.Fatalf("sections len = %d, want 1", len(secs))
	}
	rows := secs[0].GetRows()
	if len(rows) != 2 {
		t.Fatalf("rows len = %d, want 2", len(rows))
	}
	if rows[0].GetTitle() != "Coffee" || rows[0].GetRowId() != "r1" {
		t.Errorf("row[0] = %+v", rows[0])
	}
	if rows[1].GetRowId() != "r2" {
		t.Errorf("row[1] rowId = %q", rows[1].GetRowId())
	}
}

// TestTemplateMessageRoundTrip checks the hydrated template + buttons round-trip.
func TestTemplateMessageRoundTrip(t *testing.T) {
	orig := &Message{
		TemplateMessage: &TemplateMessage{
			HydratedTemplate: &TemplateMessage_HydratedFourRowTemplate{
				HydratedTitleText:   proto.String("Title"),
				HydratedContentText: proto.String("Body content"),
				HydratedFooterText:  proto.String("Footer"),
				HydratedButtons: []*TemplateMessage_HydratedButton{
					{
						Index: proto.Uint32(0),
						QuickReplyButton: &TemplateMessage_HydratedButton_HydratedQuickReplyButton{
							DisplayText: proto.String("Reply"),
							Id:          proto.String("q1"),
						},
					},
					{
						Index: proto.Uint32(1),
						UrlButton: &TemplateMessage_HydratedButton_HydratedURLButton{
							DisplayText: proto.String("Visit"),
							Url:         proto.String("https://example.com"),
						},
					},
				},
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

	tpl := got.GetTemplateMessage().GetHydratedTemplate()
	if tpl == nil {
		t.Fatal("hydratedTemplate is nil")
	}
	if tpl.GetHydratedContentText() != "Body content" {
		t.Errorf("content = %q", tpl.GetHydratedContentText())
	}
	if tpl.GetHydratedTitleText() != "Title" {
		t.Errorf("title = %q", tpl.GetHydratedTitleText())
	}
	hb := tpl.GetHydratedButtons()
	if len(hb) != 2 {
		t.Fatalf("hydratedButtons len = %d, want 2", len(hb))
	}
	if hb[0].GetQuickReplyButton().GetDisplayText() != "Reply" || hb[0].GetQuickReplyButton().GetId() != "q1" {
		t.Errorf("button[0] quickReply = %+v", hb[0].GetQuickReplyButton())
	}
	if hb[1].GetUrlButton().GetUrl() != "https://example.com" {
		t.Errorf("button[1] url = %q", hb[1].GetUrlButton().GetUrl())
	}
}
