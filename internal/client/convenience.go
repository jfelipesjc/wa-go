package client

// convenience.go provides cross-module-friendly helpers: thin wrappers over the
// richer Send* methods that take only stdlib-typed arguments. A separate Go
// module (e.g. the Evolution-style service) cannot construct this package's
// internal types (waproto.MessageKey, MediaOpts), so these methods build them
// internally from plain strings/bytes and delegate to the existing senders.

import (
	"context"
	"net/http"

	"github.com/felipeleal/wa-go/internal/waproto"
)

// React sends an emoji reaction to a target message identified by its remote
// JID, message id and fromMe flag. It builds the waproto.MessageKey internally
// so callers outside this module need not name that type. An empty emoji removes
// a previous reaction (Baileys convention). Returns the reaction message id.
func (c *Client) React(ctx context.Context, toJID, targetMsgID string, fromMe bool, emoji string) (string, error) {
	return c.SendReaction(ctx, toJID, buildReactionKey(toJID, targetMsgID, fromMe), emoji)
}

// buildReactionKey is the pure constructor for the target MessageKey of a
// reaction (split out so React's key plumbing is testable without a session).
func buildReactionKey(toJID, targetMsgID string, fromMe bool) *waproto.MessageKey {
	return &waproto.MessageKey{
		RemoteJid: &toJID,
		Id:        &targetMsgID,
		FromMe:    &fromMe,
	}
}

// DeleteMessageByID revokes (deletes for everyone) a previously sent message,
// identified by its remote JID, message id and fromMe flag. It builds the
// waproto.MessageKey internally (so callers outside this module need not name
// that type) and delegates to DeleteMessage. Returns the revoke message id.
func (c *Client) DeleteMessageByID(ctx context.Context, toJID, targetMsgID string, fromMe bool) (string, error) {
	return c.DeleteMessage(ctx, toJID, buildReactionKey(toJID, targetMsgID, fromMe))
}

// EditTextByID edits a previously sent text message, identified by its remote
// JID, message id and fromMe flag, replacing its body with newText. It builds
// the waproto.MessageKey internally and delegates to EditText.
func (c *Client) EditTextByID(ctx context.Context, toJID, targetMsgID string, fromMe bool, newText string) (string, error) {
	return c.EditText(ctx, toJID, buildReactionKey(toJID, targetMsgID, fromMe), newText)
}

// SendButtonsSimple sends a ButtonsMessage built from parallel id/text slices
// (stdlib-only args), pairing buttonIDs[i] with buttonTexts[i]. Excess ids/texts
// without a counterpart are ignored. Delegates to SendButtons.
func (c *Client) SendButtonsSimple(ctx context.Context, toJID, text, footer string, buttonIDs, buttonTexts []string) (string, error) {
	return c.SendButtons(ctx, toJID, text, footer, buildButtonsFromSlices(buttonIDs, buttonTexts))
}

// buildButtonsFromSlices is the pure constructor pairing parallel id/text slices
// into []Button, bounded by the shorter slice (split out so the pairing is
// testable without a session).
func buildButtonsFromSlices(buttonIDs, buttonTexts []string) []Button {
	n := len(buttonIDs)
	if len(buttonTexts) < n {
		n = len(buttonTexts)
	}
	buttons := make([]Button, 0, n)
	for i := 0; i < n; i++ {
		buttons = append(buttons, Button{ID: buttonIDs[i], Text: buttonTexts[i]})
	}
	return buttons
}

// SendListSimple sends a ListMessage built from parallel stdlib-only slices: one
// section per sectionTitles entry, and for each section i the rows are taken from
// rowTitles[i]/rowDescs[i]/rowIDs[i] (parallel inner slices). Missing inner
// entries are tolerated (bounded by the shortest of the three for that section).
// Delegates to SendList.
func (c *Client) SendListSimple(ctx context.Context, toJID, text, buttonText string, sectionTitles []string, rowTitles, rowDescs, rowIDs [][]string) (string, error) {
	return c.SendList(ctx, toJID, text, buttonText, buildSectionsFromSlices(sectionTitles, rowTitles, rowDescs, rowIDs))
}

// buildSectionsFromSlices is the pure constructor assembling []ListSection from
// the parallel section/row slices (split out so the assembly is testable without
// a session). For section i, the row count is the shortest of rowTitles[i],
// rowDescs[i] and rowIDs[i]; a nil/short inner slice yields fewer rows.
func buildSectionsFromSlices(sectionTitles []string, rowTitles, rowDescs, rowIDs [][]string) []ListSection {
	sections := make([]ListSection, 0, len(sectionTitles))
	for i, title := range sectionTitles {
		var titles, descs, ids []string
		if i < len(rowTitles) {
			titles = rowTitles[i]
		}
		if i < len(rowDescs) {
			descs = rowDescs[i]
		}
		if i < len(rowIDs) {
			ids = rowIDs[i]
		}
		n := len(titles)
		if len(ids) < n {
			n = len(ids)
		}
		rows := make([]ListRow, 0, n)
		for j := 0; j < n; j++ {
			row := ListRow{Title: titles[j], RowID: ids[j]}
			if j < len(descs) {
				row.Description = descs[j]
			}
			rows = append(rows, row)
		}
		sections = append(sections, ListSection{Title: title, Rows: rows})
	}
	return sections
}

// SendImageBytes sends raw image bytes with an optional caption and mimetype,
// building MediaOpts internally. Requires a configured media uploader (see
// EnableMediaTransfer / EnableDefaultMediaTransfer); without one SendImage
// returns ErrMediaUploadNotConfigured.
func (c *Client) SendImageBytes(ctx context.Context, toJID string, data []byte, caption, mimetype string) (string, error) {
	return c.SendImage(ctx, toJID, data, MediaOpts{Caption: caption, Mimetype: mimetype})
}

// SendVideoBytes sends raw video bytes with an optional caption and mimetype.
func (c *Client) SendVideoBytes(ctx context.Context, toJID string, data []byte, caption, mimetype string) (string, error) {
	return c.SendVideo(ctx, toJID, data, MediaOpts{Caption: caption, Mimetype: mimetype})
}

// SendAudioBytes sends raw audio bytes with an optional mimetype.
func (c *Client) SendAudioBytes(ctx context.Context, toJID string, data []byte, mimetype string) (string, error) {
	return c.SendAudio(ctx, toJID, data, MediaOpts{Mimetype: mimetype})
}

// SendDocumentBytes sends raw document bytes with a filename and mimetype.
func (c *Client) SendDocumentBytes(ctx context.Context, toJID string, data []byte, filename, mimetype string) (string, error) {
	return c.SendDocument(ctx, toJID, data, MediaOpts{FileName: filename, Mimetype: mimetype})
}

// EnableDefaultMediaTransfer installs the live media uploader backed by
// http.DefaultClient, so the Send* media helpers can upload through the live
// media_conn. It is the zero-argument default of EnableMediaTransfer for callers
// that do not need to inject a custom HTTP transport.
func (c *Client) EnableDefaultMediaTransfer() {
	c.EnableMediaTransfer(http.DefaultClient)
}
