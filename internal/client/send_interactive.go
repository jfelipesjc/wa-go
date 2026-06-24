package client

import (
	"context"

	"github.com/felipeleal/wa-go/internal/waproto"
	"google.golang.org/protobuf/proto"
)

// This file adds the interactive / rich-message senders (buttons, list,
// template, native-flow interactive) plus the view-once wrapper. Each public
// method builds a WAProto.Message with a pure builder and delegates to the
// shared 1:1 send core (sendMessage in send.go); the builders are split out so
// they can be unit-tested offline without any network.

// --- Buttons -----------------------------------------------------------------

// Button is a single reply button in a ButtonsMessage. ID is the machine value
// echoed back in the user's response; Text is what the user sees.
type Button struct {
	ID   string
	Text string
}

// SendButtons sends a ButtonsMessage: a body text with up to three quick-reply
// buttons and an optional footer.
func (c *Client) SendButtons(ctx context.Context, toJID, text, footer string, buttons []Button) (string, error) {
	return c.sendRouted(ctx, toJID, buildButtonsMessage(text, footer, buttons), sendOpts{pacerHint: len(text)})
}

// buildButtonsMessage is the pure constructor for a ButtonsMessage. The body is
// carried in contentText and each button is a RESPONSE (quick-reply) button.
func buildButtonsMessage(text, footer string, buttons []Button) *waproto.Message {
	bm := &waproto.ButtonsMessage{
		ContentText: proto.String(text),
		HeaderType:  waproto.ButtonsMessage_EMPTY.Enum(),
	}
	if footer != "" {
		bm.FooterText = proto.String(footer)
	}
	for _, b := range buttons {
		bm.Buttons = append(bm.Buttons, &waproto.ButtonsMessage_Button{
			ButtonId: proto.String(b.ID),
			ButtonText: &waproto.ButtonsMessage_Button_ButtonText{
				DisplayText: proto.String(b.Text),
			},
			Type: waproto.ButtonsMessage_Button_RESPONSE.Enum(),
		})
	}
	return &waproto.Message{ButtonsMessage: bm}
}

// --- List --------------------------------------------------------------------

// ListRow is a single selectable row inside a ListSection. It is shared with
// the receive path (events.go / receive_parse.go), which fills RowID from the
// incoming listMessage's rowId.
type ListRow struct {
	RowID       string
	Title       string
	Description string
}

// ListSection groups rows under a section title in a ListMessage.
type ListSection struct {
	Title string
	Rows  []ListRow
}

// SendList sends a ListMessage: a body text and a button that opens a single-
// select list grouped into sections.
func (c *Client) SendList(ctx context.Context, toJID, text, buttonText string, sections []ListSection) (string, error) {
	return c.sendRouted(ctx, toJID, buildListMessage(text, buttonText, sections), sendOpts{pacerHint: len(text)})
}

// buildListMessage is the pure constructor for a ListMessage.
func buildListMessage(text, buttonText string, sections []ListSection) *waproto.Message {
	lm := &waproto.ListMessage{
		Description: proto.String(text),
		ButtonText:  proto.String(buttonText),
		ListType:    waproto.ListMessage_SINGLE_SELECT.Enum(),
	}
	for _, s := range sections {
		section := &waproto.ListMessage_Section{}
		if s.Title != "" {
			section.Title = proto.String(s.Title)
		}
		for _, r := range s.Rows {
			row := &waproto.ListMessage_Row{
				Title: proto.String(r.Title),
				RowId: proto.String(r.RowID),
			}
			if r.Description != "" {
				row.Description = proto.String(r.Description)
			}
			section.Rows = append(section.Rows, row)
		}
		lm.Sections = append(lm.Sections, section)
	}
	return &waproto.Message{ListMessage: lm}
}

// --- Template ----------------------------------------------------------------

// SendTemplate sends a TemplateMessage built from a pre-constructed hydrated
// four-row template. Callers compose the hydrated template (title/content/
// footer + hydrated buttons) with the helpers below or directly with waproto.
func (c *Client) SendTemplate(ctx context.Context, toJID string, hydrated *waproto.TemplateMessage_HydratedFourRowTemplate) (string, error) {
	return c.sendRouted(ctx, toJID, buildTemplateMessage(hydrated), sendOpts{pacerHint: len(hydrated.GetHydratedContentText())})
}

// buildTemplateMessage is the pure constructor wrapping a hydrated template in a
// TemplateMessage.
func buildTemplateMessage(hydrated *waproto.TemplateMessage_HydratedFourRowTemplate) *waproto.Message {
	return &waproto.Message{
		TemplateMessage: &waproto.TemplateMessage{
			HydratedTemplate: hydrated,
		},
	}
}

// quickReplyButton, urlButton and callButton are small helpers to build hydrated
// template buttons without touching the deeply-nested waproto types directly.

// quickReplyButton builds a hydrated quick-reply (response) button.
func quickReplyButton(displayText, id string) *waproto.TemplateMessage_HydratedButton {
	return &waproto.TemplateMessage_HydratedButton{
		QuickReplyButton: &waproto.TemplateMessage_HydratedButton_HydratedQuickReplyButton{
			DisplayText: proto.String(displayText),
			Id:          proto.String(id),
		},
	}
}

// urlButton builds a hydrated URL (call-to-action) button.
func urlButton(displayText, url string) *waproto.TemplateMessage_HydratedButton {
	return &waproto.TemplateMessage_HydratedButton{
		UrlButton: &waproto.TemplateMessage_HydratedButton_HydratedURLButton{
			DisplayText: proto.String(displayText),
			Url:         proto.String(url),
		},
	}
}

// callButton builds a hydrated call button.
func callButton(displayText, phoneNumber string) *waproto.TemplateMessage_HydratedButton {
	return &waproto.TemplateMessage_HydratedButton{
		CallButton: &waproto.TemplateMessage_HydratedButton_HydratedCallButton{
			DisplayText: proto.String(displayText),
			PhoneNumber: proto.String(phoneNumber),
		},
	}
}

// --- Interactive (native flow) -----------------------------------------------

// NativeFlowButton is a single native-flow button in an InteractiveMessage. Name
// is the flow type (e.g. "quick_reply", "cta_url", "single_select") and
// ParamsJSON is its JSON parameter blob.
type NativeFlowButton struct {
	Name       string
	ParamsJSON string
}

// SendInteractive sends an InteractiveMessage carrying a native-flow button set,
// a body text and an optional footer.
func (c *Client) SendInteractive(ctx context.Context, toJID, body, footer string, buttons []NativeFlowButton) (string, error) {
	return c.sendRouted(ctx, toJID, buildInteractiveMessage(body, footer, buttons), sendOpts{pacerHint: len(body)})
}

// buildInteractiveMessage is the pure constructor for an InteractiveMessage with
// a nativeFlowMessage payload.
func buildInteractiveMessage(body, footer string, buttons []NativeFlowButton) *waproto.Message {
	flow := &waproto.InteractiveMessage_NativeFlowMessage{}
	for _, b := range buttons {
		nfb := &waproto.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
			Name: proto.String(b.Name),
		}
		if b.ParamsJSON != "" {
			nfb.ButtonParamsJson = proto.String(b.ParamsJSON)
		}
		flow.Buttons = append(flow.Buttons, nfb)
	}
	im := &waproto.InteractiveMessage{
		Body:              &waproto.InteractiveMessage_Body{Text: proto.String(body)},
		NativeFlowMessage: flow,
	}
	if footer != "" {
		im.Footer = &waproto.InteractiveMessage_Footer{Text: proto.String(footer)}
	}
	return &waproto.Message{InteractiveMessage: im}
}

// --- View-once wrapper -------------------------------------------------------

// SendViewOnce wraps inner in a viewOnceMessage (FutureProofMessage) and sends
// it, so the recipient can open the contained message only once. inner is a
// fully-built WAProto.Message (typically media built via the media builders).
func (c *Client) SendViewOnce(ctx context.Context, toJID string, inner *waproto.Message) (string, error) {
	return c.sendRouted(ctx, toJID, wrapViewOnce(inner), sendOpts{stanzaType: "media"})
}

// wrapViewOnce is the pure constructor that embeds inner in a
// viewOnceMessage(FutureProofMessage).
func wrapViewOnce(inner *waproto.Message) *waproto.Message {
	return &waproto.Message{
		ViewOnceMessage: &waproto.FutureProofMessage{Message: inner},
	}
}

// wrapEphemeral is the pure constructor that embeds inner in an
// ephemeralMessage(FutureProofMessage). Disappearing semantics are governed by
// the chat's ephemeral setting; this only carries the wrapper.
func wrapEphemeral(inner *waproto.Message) *waproto.Message {
	return &waproto.Message{
		EphemeralMessage: &waproto.FutureProofMessage{Message: inner},
	}
}
