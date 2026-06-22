package media

// transfer.go adds the network seam on top of the offline crypto in crypto.go:
// the HTTP download (GET + Decrypt) and upload (POST of the encrypted blob) of
// WhatsApp media. It mirrors Baileys' downloadContentFromMessage /
// downloadEncryptedContent and getWAUploadToServer
// (harness/node_modules/@whiskeysockets/baileys/lib/Utils/messages-media.js).
//
// The HTTP transport is injected via the Fetcher interface, so the whole path is
// exercisable offline in tests with a fake that returns canned responses and
// captures requests. *http.Client satisfies Fetcher.

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// defaultMediaHost is Baileys' DEF_MEDIA_HOST: the public fallback host used to
// build a download URL from a directPath when no media_conn host is supplied.
const defaultMediaHost = "mmg.whatsapp.net"

// defaultOrigin is Baileys' DEFAULT_ORIGIN header sent on download/upload.
const defaultOrigin = "https://web.whatsapp.com"

// Fetcher is the minimal HTTP seam used by Download/Upload. *http.Client
// satisfies it; tests inject a fake that returns canned responses and records
// the request.
type Fetcher interface {
	Do(req *http.Request) (*http.Response, error)
}

// ErrNoMediaLocation is returned by Download when neither a directPath nor a
// usable direct URL is available to build the download URL.
var ErrNoMediaLocation = errors.New("media: no directPath or url to download from")

// uploadPath returns Baileys' MEDIA_PATH_MAP entry for a media type. Sticker
// uploads share the image path; this package's MediaType has no Sticker value
// (stickers encrypt as Image), so Image already covers it.
func uploadPath(mt MediaType) (string, error) {
	switch mt {
	case Image:
		return "/mms/image", nil
	case Video:
		return "/mms/video", nil
	case Audio:
		return "/mms/audio", nil
	case Document:
		return "/mms/document", nil
	default:
		return "", fmt.Errorf("media: no upload path for media type %d", int(mt))
	}
}

// DownloadURL builds the GET URL for a media blob, mirroring Baileys'
// downloadContentFromMessage: prefer the directPath joined onto a media host
// (https://<host><directPath>), otherwise fall back to the direct url carried by
// the protobuf. When hosts is non-empty its first entry is used as the host;
// else the host parsed from the direct url; else defaultMediaHost.
//
// Note Baileys does NOT append auth/token query params to the download URL — the
// directPath already carries any required signature. We replicate that exactly.
func DownloadURL(directPathOrURL string, hosts []string) (string, error) {
	// A full URL (carrying its own scheme) is used verbatim.
	if strings.HasPrefix(directPathOrURL, "http://") || strings.HasPrefix(directPathOrURL, "https://") {
		return directPathOrURL, nil
	}
	if directPathOrURL == "" {
		return "", ErrNoMediaLocation
	}
	host := defaultMediaHost
	if len(hosts) > 0 && hosts[0] != "" {
		host = hosts[0]
	}
	// directPath is an absolute path beginning with "/".
	path := directPathOrURL
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return "https://" + host + path, nil
}

// Download fetches the encrypted media blob over HTTP (GET) and decrypts it with
// media.Decrypt, verifying the appended MAC. directPathOrURL is the protobuf's
// directPath (preferred) or url; mediaKey/mt are the per-message key and media
// type. hosts comes from a fresh media_conn (its first host is used); auth is
// accepted for parity with the upload path but, like Baileys, is not appended to
// the download URL.
//
// It returns the decrypted plaintext, or ErrBadMAC if the blob fails integrity
// verification, ErrNoMediaLocation if there is nothing to download from, or a
// wrapped HTTP error on a non-2xx response.
func Download(ctx context.Context, f Fetcher, directPathOrURL string, mediaKey []byte, mt MediaType, hosts []string, auth string) ([]byte, error) {
	_ = auth // download URLs are self-signed via directPath; auth unused (Baileys parity).
	if len(mediaKey) != 32 {
		return nil, fmt.Errorf("media: mediaKey must be 32 bytes, got %d", len(mediaKey))
	}
	dlURL, err := DownloadURL(directPathOrURL, hosts)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dlURL, nil)
	if err != nil {
		return nil, fmt.Errorf("media: build download request: %w", err)
	}
	req.Header.Set("Origin", defaultOrigin)

	resp, err := f.Do(req)
	if err != nil {
		return nil, fmt.Errorf("media: download GET %s: %w", dlURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Drain a bounded amount for the error message.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("media: download %s: status %d: %s", dlURL, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	enc, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("media: read download body: %w", err)
	}

	var key [32]byte
	copy(key[:], mediaKey)
	plaintext, err := Decrypt(enc, key, mt)
	if err != nil {
		return nil, err // ErrBadMAC / ErrBadPadding / ErrShortBlob bubble up.
	}
	return plaintext, nil
}

// uploadResponse is the JSON body returned by the WhatsApp media upload endpoint.
type uploadResponse struct {
	DirectPath string `json:"direct_path"`
	URL        string `json:"url"`
	Handle     string `json:"handle"`
}

// encodeEncSha256ForUpload reproduces Baileys'
// encodeBase64EncodedStringForUpload: standard base64 of fileEncSha256, then
// base64url-ify (+→-, /→_), strip '=' padding, and URL-encode. Since the
// base64url alphabet is URL-safe, the final encodeURIComponent is a no-op here;
// we return the raw base64url (no padding), which is what lands in the path and
// the token query param.
func encodeEncSha256ForUpload(fileEncSha256 []byte) string {
	return base64.RawURLEncoding.EncodeToString(fileEncSha256)
}

// UploadURL builds the POST URL for uploading an encrypted blob, mirroring
// Baileys: https://<host><MEDIA_PATH_MAP[type]>/<encSha256B64url>?auth=<auth>&token=<encSha256B64url>
func UploadURL(host string, mt MediaType, auth string, fileEncSha256 []byte) (string, error) {
	path, err := uploadPath(mt)
	if err != nil {
		return "", err
	}
	hashB64 := encodeEncSha256ForUpload(fileEncSha256)
	return fmt.Sprintf("https://%s%s/%s?auth=%s&token=%s",
		host, path, hashB64, url.QueryEscape(auth), hashB64), nil
}

// Upload POSTs the already-encrypted blob enc to WhatsApp's media servers and
// returns the directPath/url/handle from the JSON response. It tries each host
// in order until one returns a usable response (Baileys' getWAUploadToServer
// host-failover). fileEncSha256 is SHA256(enc); if nil it is computed here.
//
// Headers match Baileys: Content-Type: application/octet-stream and
// Origin: https://web.whatsapp.com. The method is POST with enc as the body.
func Upload(ctx context.Context, f Fetcher, enc []byte, mt MediaType, hosts []string, auth string, fileEncSha256 []byte) (directPath, url, handle string, err error) {
	if len(hosts) == 0 {
		return "", "", "", errors.New("media: upload requires at least one host")
	}
	if len(fileEncSha256) == 0 {
		sum := sha256.Sum256(enc)
		fileEncSha256 = sum[:]
	}

	var lastErr error
	for _, host := range hosts {
		if host == "" {
			continue
		}
		upURL, uerr := UploadURL(host, mt, auth, fileEncSha256)
		if uerr != nil {
			return "", "", "", uerr // media-type error is not host-specific.
		}

		dp, u, h, perr := uploadOnce(ctx, f, upURL, enc)
		if perr != nil {
			lastErr = perr
			continue
		}
		if dp == "" && u == "" {
			lastErr = fmt.Errorf("media: upload to %s returned no direct_path/url", host)
			continue
		}
		return dp, u, h, nil
	}
	if lastErr == nil {
		lastErr = errors.New("media: no usable upload host")
	}
	return "", "", "", fmt.Errorf("media: upload failed on all hosts: %w", lastErr)
}

// uploadOnce performs a single POST and parses the JSON response.
func uploadOnce(ctx context.Context, f Fetcher, upURL string, enc []byte) (directPath, url, handle string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upURL, strings.NewReader(string(enc)))
	if err != nil {
		return "", "", "", fmt.Errorf("media: build upload request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Origin", defaultOrigin)
	req.ContentLength = int64(len(enc))

	resp, err := f.Do(req)
	if err != nil {
		return "", "", "", fmt.Errorf("media: upload POST %s: %w", upURL, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", "", fmt.Errorf("media: read upload body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", "", fmt.Errorf("media: upload %s: status %d: %s", upURL, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var ur uploadResponse
	if err := json.Unmarshal(body, &ur); err != nil {
		return "", "", "", fmt.Errorf("media: parse upload response: %w", err)
	}
	return ur.DirectPath, ur.URL, ur.Handle, nil
}

// VerifyEncSha256 reports whether SHA256(enc) equals want, in constant time.
// Helpers may use it to validate a downloaded blob against the protobuf's
// fileEncSha256 before decrypting.
func VerifyEncSha256(enc, want []byte) bool {
	if len(want) != sha256.Size {
		return false
	}
	got := sha256.Sum256(enc)
	return subtle.ConstantTimeCompare(got[:], want) == 1
}
