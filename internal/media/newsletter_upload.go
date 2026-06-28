package media

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/url"
)

// newsletterUploadPath returns the channel (newsletter) upload path for mt, e.g.
// "/newsletter/newsletter-image". Mirrors whatsmeow's rawUpload newsletter
// prefix (uploadPrefix="newsletter", mmsType="newsletter-<type>").
func newsletterUploadPath(mt MediaType) (string, error) {
	switch mt {
	case Image, Video, Audio, Document:
		return "/newsletter/newsletter-" + mt.String(), nil
	default:
		return "", fmt.Errorf("media: unsupported newsletter media type %v", mt)
	}
}

// UploadNewsletter POSTs RAW (unencrypted) media bytes to the channel media
// endpoint and returns directPath/url/handle. Channels are not E2E-encrypted, so
// — unlike the /mms/ path — the body is the plaintext and the token is
// base64url(sha256(plaintext)) (there is no mediaKey/fileEncSha256). The returned
// handle is the message node's media_id. Mirrors whatsmeow UploadNewsletter /
// rawUpload(newsletter=true).
func UploadNewsletter(ctx context.Context, f Fetcher, data []byte, mt MediaType, hosts []string, auth string) (directPath, fileURL, handle string, err error) {
	if len(hosts) == 0 {
		return "", "", "", errors.New("media: newsletter upload requires at least one host")
	}
	path, err := newsletterUploadPath(mt)
	if err != nil {
		return "", "", "", err
	}
	sum := sha256.Sum256(data)
	token := encodeEncSha256ForUpload(sum[:])

	var lastErr error
	for _, host := range hosts {
		if host == "" {
			continue
		}
		upURL := fmt.Sprintf("https://%s%s/%s?auth=%s&token=%s",
			host, path, token, url.QueryEscape(auth), token)
		dp, u, h, perr := uploadOnce(ctx, f, upURL, data)
		if perr != nil {
			lastErr = perr
			continue
		}
		if dp == "" && u == "" {
			lastErr = fmt.Errorf("media: newsletter upload to %s returned no direct_path/url", host)
			continue
		}
		return dp, u, h, nil
	}
	if lastErr == nil {
		lastErr = errors.New("media: no usable newsletter upload host")
	}
	return "", "", "", fmt.Errorf("media: newsletter upload failed on all hosts: %w", lastErr)
}
