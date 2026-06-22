package client

import (
	"testing"

	"github.com/felipeleal/wa-go/internal/waproto"
	"google.golang.org/protobuf/proto"
)

func TestBuildButtonsMessageInteractive(t *testing.T) {
	m := buildButtonsMessage("body text", "the footer", []Button{
		{ID: "b1", Text: "Yes"},
		{ID: "b2", Text: "No"},
	})
	bm := m.GetButtonsMessage()
	if bm == nil {
		t.Fatal("missing buttonsMessage")
	}
	if bm.GetContentText() != "body text" {
		t.Fatalf("contentText = %q", bm.GetContentText())
	}
	if bm.GetFooterText() != "the footer" {
		t.Fatalf("footerText = %q", bm.GetFooterText())
	}
	if bm.GetHeaderType() != waproto.ButtonsMessage_EMPTY {
		t.Fatalf("headerType = %v", bm.GetHeaderType())
	}
	btns := bm.GetButtons()
	if len(btns) != 2 {
		t.Fatalf("buttons len = %d", len(btns))
	}
	if btns[0].GetButtonId() != "b1" || btns[0].GetButtonText().GetDisplayText() != "Yes" {
		t.Fatalf("button[0] wrong: %+v", btns[0])
	}
	if btns[0].GetType() != waproto.ButtonsMessage_Button_RESPONSE {
		t.Fatalf("button[0] type = %v", btns[0].GetType())
	}
	if btns[1].GetButtonId() != "b2" || btns[1].GetButtonText().GetDisplayText() != "No" {
		t.Fatalf("button[1] wrong: %+v", btns[1])
	}
}

func TestBuildButtonsMessageInteractive_NoFooter(t *testing.T) {
	m := buildButtonsMessage("hi", "", nil)
	if m.GetButtonsMessage().FooterText != nil {
		t.Fatal("footerText should be nil when empty")
	}
}

func TestBuildListMessageInteractive(t *testing.T) {
	m := buildListMessage("choose one", "Open menu", []ListSection{
		{
			Title: "Section A",
			Rows: []ListRow{
				{RowID: "r1", Title: "Row 1", Description: "first"},
				{RowID: "r2", Title: "Row 2"},
			},
		},
	})
	lm := m.GetListMessage()
	if lm == nil {
		t.Fatal("missing listMessage")
	}
	if lm.GetDescription() != "choose one" {
		t.Fatalf("description = %q", lm.GetDescription())
	}
	if lm.GetButtonText() != "Open menu" {
		t.Fatalf("buttonText = %q", lm.GetButtonText())
	}
	if lm.GetListType() != waproto.ListMessage_SINGLE_SELECT {
		t.Fatalf("listType = %v", lm.GetListType())
	}
	secs := lm.GetSections()
	if len(secs) != 1 || secs[0].GetTitle() != "Section A" {
		t.Fatalf("sections wrong: %+v", secs)
	}
	rows := secs[0].GetRows()
	if len(rows) != 2 {
		t.Fatalf("rows len = %d", len(rows))
	}
	if rows[0].GetRowId() != "r1" || rows[0].GetTitle() != "Row 1" || rows[0].GetDescription() != "first" {
		t.Fatalf("row[0] wrong: %+v", rows[0])
	}
	if rows[1].GetRowId() != "r2" || rows[1].Description != nil {
		t.Fatalf("row[1] wrong: %+v", rows[1])
	}
}

func TestBuildTemplateMessageInteractive(t *testing.T) {
	hydrated := &waproto.TemplateMessage_HydratedFourRowTemplate{
		HydratedContentText: proto.String("template body"),
		HydratedFooterText:  proto.String("foot"),
		HydratedButtons: []*waproto.TemplateMessage_HydratedButton{
			quickReplyButton("Reply", "qr1"),
			urlButton("Visit", "https://example.com"),
			callButton("Call us", "+5511999999999"),
		},
	}
	m := buildTemplateMessage(hydrated)
	tm := m.GetTemplateMessage()
	if tm == nil {
		t.Fatal("missing templateMessage")
	}
	h := tm.GetHydratedTemplate()
	if h.GetHydratedContentText() != "template body" || h.GetHydratedFooterText() != "foot" {
		t.Fatalf("template texts wrong: %+v", h)
	}
	btns := h.GetHydratedButtons()
	if len(btns) != 3 {
		t.Fatalf("hydrated buttons len = %d", len(btns))
	}
	if btns[0].GetQuickReplyButton().GetDisplayText() != "Reply" || btns[0].GetQuickReplyButton().GetId() != "qr1" {
		t.Fatalf("quickReply wrong: %+v", btns[0])
	}
	if btns[1].GetUrlButton().GetDisplayText() != "Visit" || btns[1].GetUrlButton().GetUrl() != "https://example.com" {
		t.Fatalf("url button wrong: %+v", btns[1])
	}
	if btns[2].GetCallButton().GetDisplayText() != "Call us" || btns[2].GetCallButton().GetPhoneNumber() != "+5511999999999" {
		t.Fatalf("call button wrong: %+v", btns[2])
	}
}

func TestBuildInteractiveMessage(t *testing.T) {
	m := buildInteractiveMessage("the body", "the footer", []NativeFlowButton{
		{Name: "quick_reply", ParamsJSON: `{"display_text":"Hi","id":"x"}`},
		{Name: "cta_url"},
	})
	im := m.GetInteractiveMessage()
	if im == nil {
		t.Fatal("missing interactiveMessage")
	}
	if im.GetBody().GetText() != "the body" {
		t.Fatalf("body = %q", im.GetBody().GetText())
	}
	if im.GetFooter().GetText() != "the footer" {
		t.Fatalf("footer = %q", im.GetFooter().GetText())
	}
	btns := im.GetNativeFlowMessage().GetButtons()
	if len(btns) != 2 {
		t.Fatalf("native flow buttons len = %d", len(btns))
	}
	if btns[0].GetName() != "quick_reply" || btns[0].GetButtonParamsJson() != `{"display_text":"Hi","id":"x"}` {
		t.Fatalf("button[0] wrong: %+v", btns[0])
	}
	if btns[1].GetName() != "cta_url" || btns[1].ButtonParamsJson != nil {
		t.Fatalf("button[1] wrong: %+v", btns[1])
	}
}

func TestBuildInteractiveMessage_NoFooter(t *testing.T) {
	m := buildInteractiveMessage("body", "", nil)
	if m.GetInteractiveMessage().Footer != nil {
		t.Fatal("footer should be nil when empty")
	}
}

func TestWrapViewOnceInteractive(t *testing.T) {
	inner := buildTextMessage("secret")
	m := wrapViewOnce(inner)
	voc := m.GetViewOnceMessage()
	if voc == nil {
		t.Fatal("missing viewOnceMessage")
	}
	if voc.GetMessage().GetConversation() != "secret" {
		t.Fatalf("inner conversation = %q", voc.GetMessage().GetConversation())
	}
	// nothing else should be set at the top level
	if m.GetConversation() != "" {
		t.Fatal("top-level conversation should be empty")
	}
}

func TestWrapEphemeralInteractive(t *testing.T) {
	inner := buildTextMessage("vanishing")
	m := wrapEphemeral(inner)
	ep := m.GetEphemeralMessage()
	if ep == nil {
		t.Fatal("missing ephemeralMessage")
	}
	if ep.GetMessage().GetConversation() != "vanishing" {
		t.Fatalf("inner conversation = %q", ep.GetMessage().GetConversation())
	}
}
