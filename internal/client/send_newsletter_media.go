// Package client: send_newsletter_media.go posts MEDIA (image/video/audio/
// document) to a channel (@newsletter). Channels are NOT end-to-end encrypted, so
// — unlike 1:1/group media — the bytes are uploaded RAW (media.UploadNewsletter)
// and the protobuf carries no MediaKey/FileEncSha256: only Url/DirectPath/
// FileSha256/FileLength (+ kind-specific metadata). The upload handle is stamped
// as the message node's media_id. Mirrors whatsmeow's sendNewsletter media path.
package client

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/jfelipesjc/wa-go/internal/media"
	"github.com/jfelipesjc/wa-go/internal/waproto"
	"google.golang.org/protobuf/proto"
)

// channelMediaUpload uploads data unencrypted to the channel media endpoint and
// returns the upload refs + handle (media_id) and the plaintext sha256.
func (c *Client) channelMediaUpload(ctx context.Context, data []byte, mt media.MediaType) (directPath, url, handle string, sha [32]byte, err error) {
	if c.uploader == nil {
		return "", "", "", sha, ErrMediaUploadNotConfigured
	}
	dp, u, h, uerr := c.uploader.UploadNewsletter(ctx, data, mt)
	if uerr != nil {
		return "", "", "", sha, fmt.Errorf("client: newsletter media upload: %w", uerr)
	}
	return dp, u, h, sha256.Sum256(data), nil
}

// SendNewsletterImage posts an image to a channel and returns its server_id.
func (c *Client) SendNewsletterImage(ctx context.Context, jid string, data []byte, opts MediaOpts) (string, error) {
	if !isNewsletterJID(jid) {
		return "", fmt.Errorf("client: %q is not a newsletter JID", jid)
	}
	dp, u, handle, sha, err := c.channelMediaUpload(ctx, data, media.Image)
	if err != nil {
		return "", err
	}
	im := &waproto.ImageMessage{
		Url:        proto.String(u),
		DirectPath: proto.String(dp),
		FileSha256:  sha[:],
		FileLength:  proto.Uint64(uint64(len(data))),
	}
	if opts.Mimetype != "" {
		im.Mimetype = proto.String(opts.Mimetype)
	}
	if opts.Caption != "" {
		im.Caption = proto.String(opts.Caption)
	}
	if opts.Width != 0 {
		im.Width = proto.Uint32(opts.Width)
	}
	if opts.Height != 0 {
		im.Height = proto.Uint32(opts.Height)
	}
	if len(opts.JpegThumbnail) > 0 {
		im.JpegThumbnail = opts.JpegThumbnail
	}
	return c.sendNewsletterMessage(ctx, jid, &waproto.Message{ImageMessage: im}, sendOpts{stanzaType: "media", mediaType: "image", mediaID: handle})
}

// SendNewsletterVideo posts a video to a channel and returns its server_id.
func (c *Client) SendNewsletterVideo(ctx context.Context, jid string, data []byte, opts MediaOpts) (string, error) {
	if !isNewsletterJID(jid) {
		return "", fmt.Errorf("client: %q is not a newsletter JID", jid)
	}
	dp, u, handle, sha, err := c.channelMediaUpload(ctx, data, media.Video)
	if err != nil {
		return "", err
	}
	vm := &waproto.VideoMessage{
		Url:        proto.String(u),
		DirectPath: proto.String(dp),
		FileSha256:  sha[:],
		FileLength:  proto.Uint64(uint64(len(data))),
	}
	if opts.Mimetype != "" {
		vm.Mimetype = proto.String(opts.Mimetype)
	}
	if opts.Caption != "" {
		vm.Caption = proto.String(opts.Caption)
	}
	if opts.Seconds != 0 {
		vm.Seconds = proto.Uint32(opts.Seconds)
	}
	if len(opts.JpegThumbnail) > 0 {
		vm.JpegThumbnail = opts.JpegThumbnail
	}
	return c.sendNewsletterMessage(ctx, jid, &waproto.Message{VideoMessage: vm}, sendOpts{stanzaType: "media", mediaType: "video", mediaID: handle})
}

// SendNewsletterAudio posts an audio to a channel and returns its server_id.
func (c *Client) SendNewsletterAudio(ctx context.Context, jid string, data []byte, opts MediaOpts) (string, error) {
	if !isNewsletterJID(jid) {
		return "", fmt.Errorf("client: %q is not a newsletter JID", jid)
	}
	dp, u, handle, sha, err := c.channelMediaUpload(ctx, data, media.Audio)
	if err != nil {
		return "", err
	}
	am := &waproto.AudioMessage{
		Url:        proto.String(u),
		DirectPath: proto.String(dp),
		FileSha256:  sha[:],
		FileLength:  proto.Uint64(uint64(len(data))),
	}
	if opts.Mimetype != "" {
		am.Mimetype = proto.String(opts.Mimetype)
	}
	if opts.Seconds != 0 {
		am.Seconds = proto.Uint32(opts.Seconds)
	}
	if opts.PTT {
		am.Ptt = proto.Bool(true)
	}
	return c.sendNewsletterMessage(ctx, jid, &waproto.Message{AudioMessage: am}, sendOpts{stanzaType: "media", mediaType: "audio", mediaID: handle})
}

// SendNewsletterDocument posts a document to a channel and returns its server_id.
func (c *Client) SendNewsletterDocument(ctx context.Context, jid string, data []byte, opts MediaOpts) (string, error) {
	if !isNewsletterJID(jid) {
		return "", fmt.Errorf("client: %q is not a newsletter JID", jid)
	}
	dp, u, handle, sha, err := c.channelMediaUpload(ctx, data, media.Document)
	if err != nil {
		return "", err
	}
	dm := &waproto.DocumentMessage{
		Url:        proto.String(u),
		DirectPath: proto.String(dp),
		FileSha256:  sha[:],
		FileLength:  proto.Uint64(uint64(len(data))),
	}
	if opts.Mimetype != "" {
		dm.Mimetype = proto.String(opts.Mimetype)
	}
	if opts.FileName != "" {
		dm.FileName = proto.String(opts.FileName)
	}
	if opts.PageCount != 0 {
		dm.PageCount = proto.Uint32(opts.PageCount)
	}
	if len(opts.JpegThumbnail) > 0 {
		dm.JpegThumbnail = opts.JpegThumbnail
	}
	return c.sendNewsletterMessage(ctx, jid, &waproto.Message{DocumentMessage: dm}, sendOpts{stanzaType: "media", mediaType: "document", mediaID: handle})
}
