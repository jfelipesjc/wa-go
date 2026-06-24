package client

import (
	"context"
	"errors"

	"github.com/felipeleal/wa-go/internal/waproto"
)

// DownloadStoredMedia downloads and decrypts the media carried by a stored
// message (a *waproto.WebMessageInfo, e.g. ChatStore's StoredMessage.Raw),
// returning the plaintext bytes and the media mimetype. It returns an error if
// the message carries no downloadable media (text, reaction, etc.). This backs
// Evolution's getBase64FromMediaMessage endpoint: the caller base64-encodes the
// returned bytes.
func (c *Client) DownloadStoredMedia(ctx context.Context, raw *waproto.WebMessageInfo) ([]byte, string, error) {
	if raw == nil || raw.GetMessage() == nil {
		return nil, "", errors.New("client: message has no payload")
	}
	ev := parseMessage(raw.GetMessage())
	if ev.Media == nil {
		return nil, "", errors.New("client: message carries no downloadable media")
	}
	data, err := c.DownloadMedia(ctx, ev.Media)
	if err != nil {
		return nil, "", err
	}
	return data, ev.Media.Mimetype, nil
}
