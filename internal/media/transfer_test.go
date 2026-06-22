package media

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// fakeFetcher records the last request it served and returns a canned response.
type fakeFetcher struct {
	resp    *http.Response
	err     error
	lastReq *http.Request
	body    []byte // captured request body
}

func (f *fakeFetcher) Do(req *http.Request) (*http.Response, error) {
	f.lastReq = req
	if req.Body != nil {
		f.body, _ = io.ReadAll(req.Body)
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func newResp(status int, body []byte) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     make(http.Header),
	}
}

func mediaTypeByName(name string) MediaType {
	switch name {
	case "image":
		return Image
	case "video":
		return Video
	case "audio":
		return Audio
	case "document":
		return Document
	}
	return Image
}

// TestDownload_DecryptsVector serves the ciphertext from a golden vector via a
// fake fetcher and checks Download yields the expected plaintext, and that the
// download URL is built from directPath + host with no auth/token params.
func TestDownload_DecryptsVector(t *testing.T) {
	v, mediaKey := loadVectors(t)

	for _, c := range v.Cases {
		c := c
		t.Run(c.Plaintext+"_"+c.MediaType, func(t *testing.T) {
			enc := mustHex(t, c.CiphertextHex)
			wantPlain := mustHex(t, v.Plaintexts[c.Plaintext].PlaintextHex)
			mt := mediaTypeByName(c.MediaType)

			f := &fakeFetcher{resp: newResp(200, enc)}
			directPath := "/v/t62.7118-24/12345_n.enc"
			hosts := []string{"media-abc.cdn.whatsapp.net", "fallback.example"}

			got, err := Download(context.Background(), f, directPath, mediaKey[:], mt, hosts, "AUTHTOKEN")
			if err != nil {
				t.Fatalf("Download: %v", err)
			}
			if !bytes.Equal(got, wantPlain) {
				t.Fatalf("plaintext mismatch: got %d bytes, want %d", len(got), len(wantPlain))
			}

			// URL: https://<first host><directPath>, no query params (Baileys parity).
			wantURL := "https://media-abc.cdn.whatsapp.net" + directPath
			if f.lastReq.URL.String() != wantURL {
				t.Fatalf("download URL = %q, want %q", f.lastReq.URL.String(), wantURL)
			}
			if f.lastReq.Method != http.MethodGet {
				t.Fatalf("method = %q, want GET", f.lastReq.Method)
			}
			if f.lastReq.URL.RawQuery != "" {
				t.Fatalf("download URL must carry no query params, got %q", f.lastReq.URL.RawQuery)
			}
			if got := f.lastReq.Header.Get("Origin"); got != defaultOrigin {
				t.Fatalf("Origin = %q, want %q", got, defaultOrigin)
			}
		})
	}
}

// TestDownload_DirectURL uses a full https URL (no directPath) verbatim.
func TestDownload_DirectURL(t *testing.T) {
	v, mediaKey := loadVectors(t)
	c := v.Cases[0]
	enc := mustHex(t, c.CiphertextHex)
	wantPlain := mustHex(t, v.Plaintexts[c.Plaintext].PlaintextHex)

	f := &fakeFetcher{resp: newResp(200, enc)}
	url := "https://full.example.net/path/blob.enc?x=1"
	got, err := Download(context.Background(), f, url, mediaKey[:], mediaTypeByName(c.MediaType), nil, "")
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if !bytes.Equal(got, wantPlain) {
		t.Fatalf("plaintext mismatch")
	}
	if f.lastReq.URL.String() != url {
		t.Fatalf("URL = %q, want %q", f.lastReq.URL.String(), url)
	}
}

// TestDownload_BadMAC flips a byte in the ciphertext and expects ErrBadMAC.
func TestDownload_BadMAC(t *testing.T) {
	v, mediaKey := loadVectors(t)
	c := v.Cases[0]
	enc := mustHex(t, c.CiphertextHex)
	tampered := append([]byte(nil), enc...)
	tampered[0] ^= 0xFF

	f := &fakeFetcher{resp: newResp(200, tampered)}
	_, err := Download(context.Background(), f, "/p/x.enc", mediaKey[:], mediaTypeByName(c.MediaType), []string{"h"}, "")
	if !errors.Is(err, ErrBadMAC) {
		t.Fatalf("err = %v, want ErrBadMAC", err)
	}
}

// TestDownload_HTTPError surfaces a non-2xx status.
func TestDownload_HTTPError(t *testing.T) {
	_, mediaKey := loadVectors(t)
	f := &fakeFetcher{resp: newResp(404, []byte("not found"))}
	_, err := Download(context.Background(), f, "/p/x.enc", mediaKey[:], Image, []string{"h"}, "")
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v, want 404 error", err)
	}
}

// TestDownload_NoLocation errors when neither directPath nor url is given.
func TestDownload_NoLocation(t *testing.T) {
	_, mediaKey := loadVectors(t)
	f := &fakeFetcher{resp: newResp(200, nil)}
	_, err := Download(context.Background(), f, "", mediaKey[:], Image, []string{"h"}, "")
	if !errors.Is(err, ErrNoMediaLocation) {
		t.Fatalf("err = %v, want ErrNoMediaLocation", err)
	}
}

// TestUpload_PostsAndParses checks Upload POSTs enc as the body to the correct
// URL and returns the parsed direct_path/url/handle.
func TestUpload_PostsAndParses(t *testing.T) {
	v, _ := loadVectors(t)
	c := v.Cases[0]
	enc := mustHex(t, c.CiphertextHex)
	fileEncSha := mustHex(t, c.FileEncSha256)

	respBody := []byte(`{"direct_path":"/v/t62/abc_n.enc","url":"https://m.example/dl","handle":"H123"}`)
	f := &fakeFetcher{resp: newResp(200, respBody)}

	hosts := []string{"upload.example.whatsapp.net"}
	auth := "auth token+with/special=chars"
	dp, url, handle, err := Upload(context.Background(), f, enc, Image, hosts, auth, fileEncSha)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if dp != "/v/t62/abc_n.enc" || url != "https://m.example/dl" || handle != "H123" {
		t.Fatalf("parsed wrong: dp=%q url=%q handle=%q", dp, url, handle)
	}

	// Method + body.
	if f.lastReq.Method != http.MethodPost {
		t.Fatalf("method = %q, want POST", f.lastReq.Method)
	}
	if !bytes.Equal(f.body, enc) {
		t.Fatalf("request body != enc (got %d bytes, want %d)", len(f.body), len(enc))
	}
	if ct := f.lastReq.Header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}

	// URL: https://<host>/mms/image/<encB64url>?auth=<esc>&token=<encB64url>
	hashB64 := base64.RawURLEncoding.EncodeToString(fileEncSha)
	u := f.lastReq.URL
	if u.Host != "upload.example.whatsapp.net" {
		t.Fatalf("host = %q", u.Host)
	}
	if u.Path != "/mms/image/"+hashB64 {
		t.Fatalf("path = %q, want /mms/image/%s", u.Path, hashB64)
	}
	if got := u.Query().Get("auth"); got != auth {
		t.Fatalf("auth param = %q, want %q", got, auth)
	}
	if got := u.Query().Get("token"); got != hashB64 {
		t.Fatalf("token param = %q, want %q", got, hashB64)
	}
}

// TestUpload_ComputesEncShaWhenNil lets Upload derive fileEncSha256 from enc.
func TestUpload_ComputesEncShaWhenNil(t *testing.T) {
	enc := []byte("some-encrypted-bytes-payload-xxxx")
	respBody := []byte(`{"direct_path":"/dp","url":"u"}`)
	f := &fakeFetcher{resp: newResp(200, respBody)}
	dp, _, _, err := Upload(context.Background(), f, enc, Document, []string{"h"}, "a", nil)
	if err != nil || dp != "/dp" {
		t.Fatalf("Upload: dp=%q err=%v", dp, err)
	}
	if !strings.HasPrefix(f.lastReq.URL.Path, "/mms/document/") {
		t.Fatalf("path = %q", f.lastReq.URL.Path)
	}
}

// TestUpload_HostFailover skips a failing host and succeeds on the next.
func TestUpload_HostFailover(t *testing.T) {
	enc := []byte("payload")
	calls := 0
	f := &seqFetcher{do: func(req *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return newResp(500, []byte("boom")), nil
		}
		return newResp(200, []byte(`{"direct_path":"/ok","url":"u"}`)), nil
	}}
	dp, _, _, err := Upload(context.Background(), f, enc, Image, []string{"bad.host", "good.host"}, "a", nil)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if dp != "/ok" {
		t.Fatalf("dp = %q, want /ok", dp)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

// TestUpload_NoHosts errors with an empty host list.
func TestUpload_NoHosts(t *testing.T) {
	f := &fakeFetcher{resp: newResp(200, nil)}
	_, _, _, err := Upload(context.Background(), f, []byte("x"), Image, nil, "a", nil)
	if err == nil {
		t.Fatal("expected error for empty host list")
	}
}

// seqFetcher runs an arbitrary handler per request, for multi-call scenarios.
type seqFetcher struct {
	do func(req *http.Request) (*http.Response, error)
}

func (s *seqFetcher) Do(req *http.Request) (*http.Response, error) { return s.do(req) }

// TestEncodeEncSha256ForUpload matches Baileys' base64url-no-pad encoding.
func TestEncodeEncSha256ForUpload(t *testing.T) {
	// Bytes chosen so std base64 contains both '+' and '/'.
	in := mustHex(t, "fbf0")
	got := encodeEncSha256ForUpload(in)
	// std base64("\xfb\xf0") = "+/A=" -> base64url no pad = "-_A"
	if got != "-_A" {
		t.Fatalf("encode = %q, want %q", got, "-_A")
	}
	if hex.EncodeToString(in) != "fbf0" {
		t.Fatal("sanity")
	}
}
