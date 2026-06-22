package client

// media_transfer.go wires the offline crypto (internal/media) and the media_conn
// descriptor together into the live upload/download seam the Client exposes:
//
//   - liveMediaUploader implements the mediaUploader interface (see send_media.go)
//     by fetching a media_conn and POSTing the encrypted blob via media.Upload.
//     Install it with WithMediaUploader(c.LiveMediaUploader()).
//   - (*Client).DownloadMedia fetches a media_conn and GETs+decrypts the blob
//     referenced by a received MediaInfo via media.Download.
//
// Both share the injectable media.Fetcher seam so the whole path is testable
// offline; the default is http.DefaultClient.

import (
	"context"
	"errors"
	"net/http"

	"github.com/felipeleal/wa-go/internal/media"
)

// liveMediaUploader is the concrete mediaUploader: it resolves a media_conn from
// the Client and uploads encrypted blobs through an injectable HTTP fetcher.
type liveMediaUploader struct {
	c       *Client
	fetcher media.Fetcher
}

// LiveMediaUploader returns a mediaUploader backed by this Client's media_conn
// and http.DefaultClient. Pass it to WithMediaUploader to enable live media
// sends:
//
//	c := client.NewWithOptions(store, dial)
//	client.WithMediaUploader(c.LiveMediaUploader())(c) // or via NewWithOptions
//
// Because the Client field is set only by the WithMediaUploader Option (and this
// build does not edit client.go), the idiomatic call is:
//
//	c := client.NewWithOptions(store, dial, opts...)
//	... then construct with the option once the Client exists ...
//
// In practice callers build the uploader from an already-constructed Client and
// install it via the exported EnableMediaTransfer helper below.
func (c *Client) LiveMediaUploader() mediaUploader {
	return &liveMediaUploader{c: c, fetcher: http.DefaultClient}
}

// LiveMediaUploaderWithFetcher is like LiveMediaUploader but with an injected
// HTTP transport (tests pass a fake media.Fetcher).
func (c *Client) LiveMediaUploaderWithFetcher(f media.Fetcher) mediaUploader {
	if f == nil {
		f = http.DefaultClient
	}
	return &liveMediaUploader{c: c, fetcher: f}
}

// Upload satisfies mediaUploader: it fetches a media_conn and uploads enc,
// returning the directPath/url to embed in the outgoing protobuf.
func (u *liveMediaUploader) Upload(ctx context.Context, enc []byte, mediaType media.MediaType) (directPath, url string, err error) {
	conn, err := u.c.mediaConn(ctx)
	if err != nil {
		return "", "", err
	}
	dp, u2, _, err := media.Upload(ctx, u.fetcher, enc, mediaType, conn.Hosts, conn.Auth, nil)
	if err != nil {
		return "", "", err
	}
	return dp, u2, nil
}

// EnableMediaTransfer installs the live media uploader on this Client so the
// Send* media helpers can upload through the live media_conn. It sets the same
// field WithMediaUploader sets, but on an already-constructed Client (the
// uploader needs a back-reference to the Client to fetch media_conn, which the
// construction-time Option cannot express cleanly).
//
// Pass a custom fetcher (e.g. for tests) or nil for http.DefaultClient.
func (c *Client) EnableMediaTransfer(f media.Fetcher) {
	WithMediaUploader(c.LiveMediaUploaderWithFetcher(f))(c)
}

// mediaInfoType maps a received MediaInfo.Kind to the crypto MediaType used to
// derive keys. Stickers encrypt as Image (Baileys sticker→Image HKDF mapping).
func mediaInfoType(kind MediaKind) (media.MediaType, error) {
	switch kind {
	case MediaImage, MediaSticker:
		return media.Image, nil
	case MediaVideo:
		return media.Video, nil
	case MediaAudio:
		return media.Audio, nil
	case MediaDocument:
		return media.Document, nil
	default:
		return 0, errors.New("client: unsupported media kind for download: " + string(kind))
	}
}

// DownloadMedia fetches and decrypts the media referenced by a received
// MediaInfo (from a MessageEvent). It resolves a media_conn for the host list,
// builds the download URL from DirectPath (preferred) or URL, GETs the encrypted
// blob, and decrypts it with the message's MediaKey, verifying the MAC.
//
// It uses http.DefaultClient; for an injected transport use
// DownloadMediaWithFetcher.
func (c *Client) DownloadMedia(ctx context.Context, info *MediaInfo) ([]byte, error) {
	return c.DownloadMediaWithFetcher(ctx, http.DefaultClient, info)
}

// DownloadMediaWithFetcher is DownloadMedia with an injectable HTTP transport.
func (c *Client) DownloadMediaWithFetcher(ctx context.Context, f media.Fetcher, info *MediaInfo) ([]byte, error) {
	if info == nil {
		return nil, errors.New("client: DownloadMedia requires a non-nil MediaInfo")
	}
	if f == nil {
		f = http.DefaultClient
	}
	mt, err := mediaInfoType(info.Kind)
	if err != nil {
		return nil, err
	}

	// A media_conn is needed for the host when only a directPath is present.
	// When a full URL is carried we still fetch it for the auth/host, but
	// Download tolerates an empty host list (falls back to the default host).
	var hosts []string
	var auth string
	loc := info.DirectPath
	if loc == "" {
		loc = info.URL
	}
	// Only query media_conn when we must derive a host from a directPath.
	if info.DirectPath != "" {
		conn, cerr := c.mediaConn(ctx)
		if cerr != nil {
			return nil, cerr
		}
		hosts, auth = conn.Hosts, conn.Auth
	}

	plaintext, err := media.Download(ctx, f, loc, info.MediaKey, mt, hosts, auth)
	if err != nil {
		return nil, err
	}
	return plaintext, nil
}
