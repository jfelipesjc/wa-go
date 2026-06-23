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
