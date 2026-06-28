package client

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/jfelipesjc/wa-go/internal/media"
	"github.com/jfelipesjc/wa-go/internal/waproto"
	"google.golang.org/protobuf/proto"
)

// mediaUploader performs the live HTTP upload of an already-encrypted media blob
// to WhatsApp's media servers, returning the directPath and url that must be put
// in the message protobuf. This is the single network seam of the media send
// path: the rest (key generation, encryption, protobuf construction) is offline.
//
// This build ships no implementation (c.uploader is nil), so callers either
// install one via WithMediaUploader or pass a pre-uploaded MediaRef in MediaOpts.
type mediaUploader interface {
	Upload(ctx context.Context, enc []byte, mediaType media.MediaType) (directPath, url string, err error)
	// UploadNewsletter uploads RAW (unencrypted) bytes to the channel media
	// endpoint and returns directPath/url plus the media handle (media_id).
	UploadNewsletter(ctx context.Context, data []byte, mediaType media.MediaType) (directPath, url, handle string, err error)
}

// MediaRef is a pre-computed upload reference. When the caller has already
// uploaded the encrypted blob (e.g. via a separate live transport), it can pass
// the resulting directPath/url here and skip the in-band uploader entirely.
type MediaRef struct {
	DirectPath string
	URL        string
}

// MediaOpts carries the optional metadata that lands in the media protobuf plus
// the upload strategy. Width/Height/Seconds/etc. are caller-supplied because
// this build does not decode media to probe dimensions.
//
// Upload strategy (documented behavior):
//   - If Ref is non-nil, its DirectPath/URL are used directly (no uploader call).
//   - Else if the Client has a uploader (WithMediaUploader), it is invoked.
//   - Else SendImage/etc. return a clear "media upload not configured" error.
type MediaOpts struct {
	Ref      *MediaRef // pre-uploaded reference; bypasses the uploader
	Mimetype string
	Caption  string

	// Visual / temporal metadata (set what applies to the media kind).
	Width   uint32
	Height  uint32
	Seconds uint32 // audio/video duration
	PTT     bool   // audio: push-to-talk (voice note)

	// Document-specific.
	FileName  string
	PageCount uint32

	// JpegThumbnail / PngThumbnail (sticker) preview bytes, optional.
	JpegThumbnail []byte
	PngThumbnail  []byte

	// Reply / mention context, optional.
	ContextInfo *waproto.ContextInfo
}

// ErrMediaUploadNotConfigured is returned by the Send* media helpers when no
// MediaRef was supplied and no live uploader is installed.
var ErrMediaUploadNotConfigured = errors.New("client: media upload not configured (pass MediaOpts.Ref or WithMediaUploader)")

// mediaSendInfo bundles everything the protobuf builders need after encryption.
type mediaSendInfo struct {
	mediaKey          [32]byte
	fileSha256        [32]byte
	fileEncSha256     [32]byte
	fileLength        uint64
	directPath        string
	url               string
	mediaKeyTimestamp int64
}

// prepareMedia generates a random media key, encrypts the plaintext for the
// given media type, resolves the upload reference (Ref or uploader), and returns
// the assembled mediaSendInfo. This is the shared offline-then-upload step every
// Send* media helper runs before building its protobuf.
func (c *Client) prepareMedia(ctx context.Context, data []byte, mediaType media.MediaType, opts MediaOpts) (*mediaSendInfo, error) {
	var mediaKey [32]byte
	if _, err := rand.Read(mediaKey[:]); err != nil {
		return nil, fmt.Errorf("client: media key random: %w", err)
	}

	enc, fileSha, fileEncSha, err := media.Encrypt(data, mediaKey, mediaType)
	if err != nil {
		return nil, fmt.Errorf("client: media encrypt: %w", err)
	}

	var directPath, url string
	switch {
	case opts.Ref != nil:
		directPath, url = opts.Ref.DirectPath, opts.Ref.URL
	case c.uploader != nil:
		directPath, url, err = c.uploader.Upload(ctx, enc, mediaType)
		if err != nil {
			return nil, fmt.Errorf("client: media upload: %w", err)
		}
	default:
		return nil, ErrMediaUploadNotConfigured
	}

	return &mediaSendInfo{
		mediaKey:          mediaKey,
		fileSha256:        fileSha,
		fileEncSha256:     fileEncSha,
		fileLength:        uint64(len(data)),
		directPath:        directPath,
		url:               url,
		mediaKeyTimestamp: time.Now().Unix(),
	}, nil
}

// SendImage encrypts data as an image, (uploads it,) and sends an ImageMessage.
func (c *Client) SendImage(ctx context.Context, toJID string, data []byte, opts MediaOpts) (string, error) {
	if isNewsletterJID(toJID) {
		return c.SendNewsletterImage(ctx, toJID, data, opts)
	}
	info, err := c.prepareMedia(ctx, data, media.Image, opts)
	if err != nil {
		return "", err
	}
	return c.sendRouted(ctx, toJID, buildImageMessage(info, opts), sendOpts{stanzaType: "media", mediaType: "image", pacerHint: len(opts.Caption)})
}

// SendVideo encrypts data as a video, (uploads it,) and sends a VideoMessage.
func (c *Client) SendVideo(ctx context.Context, toJID string, data []byte, opts MediaOpts) (string, error) {
	if isNewsletterJID(toJID) {
		return c.SendNewsletterVideo(ctx, toJID, data, opts)
	}
	info, err := c.prepareMedia(ctx, data, media.Video, opts)
	if err != nil {
		return "", err
	}
	return c.sendRouted(ctx, toJID, buildVideoMessage(info, opts), sendOpts{stanzaType: "media", mediaType: "video", pacerHint: len(opts.Caption)})
}

// SendPtv encrypts data as a video and sends it as a PTV (Push-To-Video / round
// video note): same media pipeline as SendVideo, but the payload rides in
// Message.ptvMessage so clients render it as a circular auto-playing note.
func (c *Client) SendPtv(ctx context.Context, toJID string, data []byte, opts MediaOpts) (string, error) {
	if isNewsletterJID(toJID) {
		return "", fmt.Errorf("client: PTV (round video) send to a channel (%q) is not supported", toJID)
	}
	info, err := c.prepareMedia(ctx, data, media.Video, opts)
	if err != nil {
		return "", err
	}
	vm := buildVideoMessage(info, opts).GetVideoMessage()
	msg := &waproto.Message{PtvMessage: vm}
	return c.sendRouted(ctx, toJID, msg, sendOpts{stanzaType: "media", mediaType: "ptv", pacerHint: len(opts.Caption)})
}

// SendPtvBytes is the convenience form of SendPtv (mimetype only).
func (c *Client) SendPtvBytes(ctx context.Context, toJID string, data []byte, mimetype string) (string, error) {
	if mimetype == "" {
		mimetype = "video/mp4"
	}
	return c.SendPtv(ctx, toJID, data, MediaOpts{Mimetype: mimetype})
}

// SendAudio encrypts data as audio, (uploads it,) and sends an AudioMessage.
func (c *Client) SendAudio(ctx context.Context, toJID string, data []byte, opts MediaOpts) (string, error) {
	if isNewsletterJID(toJID) {
		return c.SendNewsletterAudio(ctx, toJID, data, opts)
	}
	info, err := c.prepareMedia(ctx, data, media.Audio, opts)
	if err != nil {
		return "", err
	}
	return c.sendRouted(ctx, toJID, buildAudioMessage(info, opts), sendOpts{stanzaType: "media", mediaType: audioMediaType(opts)})
}

// SendDocument encrypts data as a document, (uploads it,) and sends a
// DocumentMessage.
func (c *Client) SendDocument(ctx context.Context, toJID string, data []byte, opts MediaOpts) (string, error) {
	if isNewsletterJID(toJID) {
		return c.SendNewsletterDocument(ctx, toJID, data, opts)
	}
	info, err := c.prepareMedia(ctx, data, media.Document, opts)
	if err != nil {
		return "", err
	}
	return c.sendRouted(ctx, toJID, buildDocumentMessage(info, opts), sendOpts{stanzaType: "media", mediaType: "document", pacerHint: len(opts.Caption)})
}

// SendSticker encrypts data as a sticker (image media type, per Baileys) and
// sends a StickerMessage.
func (c *Client) SendSticker(ctx context.Context, toJID string, data []byte, opts MediaOpts) (string, error) {
	if isNewsletterJID(toJID) {
		return "", fmt.Errorf("client: sticker send to a channel (%q) is not supported", toJID)
	}
	// Stickers use the Image HKDF info ("WhatsApp Image Keys") in Baileys.
	info, err := c.prepareMedia(ctx, data, media.Image, opts)
	if err != nil {
		return "", err
	}
	return c.sendRouted(ctx, toJID, buildStickerMessage(info, opts), sendOpts{stanzaType: "media", mediaType: "sticker"})
}

// audioMediaType returns the enc `mediatype` for an audio send: "ptt" for a
// voice note, else "audio" (mirrors Baileys getMediaType).
func audioMediaType(opts MediaOpts) string {
	if opts.PTT {
		return "ptt"
	}
	return "audio"
}

// --- pure protobuf builders (testable without network) ---

func buildImageMessage(info *mediaSendInfo, opts MediaOpts) *waproto.Message {
	im := &waproto.ImageMessage{
		MediaKey:          info.mediaKey[:],
		FileSha256:        info.fileSha256[:],
		FileEncSha256:     info.fileEncSha256[:],
		FileLength:        proto.Uint64(info.fileLength),
		DirectPath:        proto.String(info.directPath),
		Url:               proto.String(info.url),
		MediaKeyTimestamp: proto.Int64(info.mediaKeyTimestamp),
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
	if opts.ContextInfo != nil {
		im.ContextInfo = opts.ContextInfo
	}
	return &waproto.Message{ImageMessage: im}
}

func buildVideoMessage(info *mediaSendInfo, opts MediaOpts) *waproto.Message {
	vm := &waproto.VideoMessage{
		MediaKey:          info.mediaKey[:],
		FileSha256:        info.fileSha256[:],
		FileEncSha256:     info.fileEncSha256[:],
		FileLength:        proto.Uint64(info.fileLength),
		DirectPath:        proto.String(info.directPath),
		Url:               proto.String(info.url),
		MediaKeyTimestamp: proto.Int64(info.mediaKeyTimestamp),
	}
	if opts.Mimetype != "" {
		vm.Mimetype = proto.String(opts.Mimetype)
	}
	if opts.Caption != "" {
		vm.Caption = proto.String(opts.Caption)
	}
	if opts.Width != 0 {
		vm.Width = proto.Uint32(opts.Width)
	}
	if opts.Height != 0 {
		vm.Height = proto.Uint32(opts.Height)
	}
	if opts.Seconds != 0 {
		vm.Seconds = proto.Uint32(opts.Seconds)
	}
	if len(opts.JpegThumbnail) > 0 {
		vm.JpegThumbnail = opts.JpegThumbnail
	}
	if opts.ContextInfo != nil {
		vm.ContextInfo = opts.ContextInfo
	}
	return &waproto.Message{VideoMessage: vm}
}

func buildAudioMessage(info *mediaSendInfo, opts MediaOpts) *waproto.Message {
	am := &waproto.AudioMessage{
		MediaKey:          info.mediaKey[:],
		FileSha256:        info.fileSha256[:],
		FileEncSha256:     info.fileEncSha256[:],
		FileLength:        proto.Uint64(info.fileLength),
		DirectPath:        proto.String(info.directPath),
		Url:               proto.String(info.url),
		MediaKeyTimestamp: proto.Int64(info.mediaKeyTimestamp),
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
	if opts.ContextInfo != nil {
		am.ContextInfo = opts.ContextInfo
	}
	return &waproto.Message{AudioMessage: am}
}

func buildDocumentMessage(info *mediaSendInfo, opts MediaOpts) *waproto.Message {
	dm := &waproto.DocumentMessage{
		MediaKey:          info.mediaKey[:],
		FileSha256:        info.fileSha256[:],
		FileEncSha256:     info.fileEncSha256[:],
		FileLength:        proto.Uint64(info.fileLength),
		DirectPath:        proto.String(info.directPath),
		Url:               proto.String(info.url),
		MediaKeyTimestamp: proto.Int64(info.mediaKeyTimestamp),
	}
	if opts.Mimetype != "" {
		dm.Mimetype = proto.String(opts.Mimetype)
	}
	if opts.FileName != "" {
		dm.FileName = proto.String(opts.FileName)
	}
	if opts.Caption != "" {
		dm.Caption = proto.String(opts.Caption)
	}
	if opts.PageCount != 0 {
		dm.PageCount = proto.Uint32(opts.PageCount)
	}
	if len(opts.JpegThumbnail) > 0 {
		dm.JpegThumbnail = opts.JpegThumbnail
	}
	if opts.ContextInfo != nil {
		dm.ContextInfo = opts.ContextInfo
	}
	return &waproto.Message{DocumentMessage: dm}
}

func buildStickerMessage(info *mediaSendInfo, opts MediaOpts) *waproto.Message {
	sm := &waproto.StickerMessage{
		MediaKey:          info.mediaKey[:],
		FileSha256:        info.fileSha256[:],
		FileEncSha256:     info.fileEncSha256[:],
		FileLength:        proto.Uint64(info.fileLength),
		DirectPath:        proto.String(info.directPath),
		Url:               proto.String(info.url),
		MediaKeyTimestamp: proto.Int64(info.mediaKeyTimestamp),
	}
	if opts.Mimetype != "" {
		sm.Mimetype = proto.String(opts.Mimetype)
	} else {
		sm.Mimetype = proto.String("image/webp")
	}
	if opts.Width != 0 {
		sm.Width = proto.Uint32(opts.Width)
	}
	if opts.Height != 0 {
		sm.Height = proto.Uint32(opts.Height)
	}
	if len(opts.PngThumbnail) > 0 {
		sm.PngThumbnail = opts.PngThumbnail
	}
	if opts.ContextInfo != nil {
		sm.ContextInfo = opts.ContextInfo
	}
	return &waproto.Message{StickerMessage: sm}
}
