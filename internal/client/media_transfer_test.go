package client

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/jfelipesjc/wa-go/internal/media"
	"github.com/jfelipesjc/wa-go/internal/store"
	"github.com/jfelipesjc/wa-go/internal/wire"
)

// --- parseMediaConn (pure) ---

func newTestStore(t *testing.T) store.Store {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "mediaconn.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestParseMediaConn(t *testing.T) {
	reply := wire.Node{
		Tag:   "iq",
		Attrs: map[string]string{"type": "result", "id": "x"},
		Content: []wire.Node{
			{
				Tag:   "media_conn",
				Attrs: map[string]string{"auth": "AUTH-TOKEN-XYZ", "ttl": "300"},
				Content: []wire.Node{
					{Tag: "host", Attrs: map[string]string{"hostname": "media-a.cdn.whatsapp.net"}},
					{Tag: "host", Attrs: map[string]string{"hostname": "media-b.cdn.whatsapp.net"}},
					{Tag: "ignored", Attrs: map[string]string{}},
				},
			},
		},
	}
	conn, err := parseMediaConn(reply)
	if err != nil {
		t.Fatalf("parseMediaConn: %v", err)
	}
	if conn.Auth != "AUTH-TOKEN-XYZ" {
		t.Fatalf("auth = %q", conn.Auth)
	}
	if conn.TTL != 300 {
		t.Fatalf("ttl = %d, want 300", conn.TTL)
	}
	want := []string{"media-a.cdn.whatsapp.net", "media-b.cdn.whatsapp.net"}
	if len(conn.Hosts) != 2 || conn.Hosts[0] != want[0] || conn.Hosts[1] != want[1] {
		t.Fatalf("hosts = %v, want %v", conn.Hosts, want)
	}
}

func TestParseMediaConn_Errors(t *testing.T) {
	cases := []struct {
		name  string
		reply wire.Node
	}{
		{"no media_conn", wire.Node{Tag: "iq"}},
		{"no auth", wire.Node{Tag: "iq", Content: []wire.Node{
			{Tag: "media_conn", Attrs: map[string]string{"ttl": "1"}, Content: []wire.Node{
				{Tag: "host", Attrs: map[string]string{"hostname": "h"}},
			}},
		}}},
		{"no hosts", wire.Node{Tag: "iq", Content: []wire.Node{
			{Tag: "media_conn", Attrs: map[string]string{"auth": "a", "ttl": "1"}},
		}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := parseMediaConn(c.reply); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

// --- mediaConn over a fake live session ---

// fakeSessionIQ wires sendIQ to a canned reply: the send func captures the
// outgoing iq, then deliverIQ is invoked with a reply carrying the same id.
func newClientWithMediaConnReply(t *testing.T, reply wire.Node) *Client {
	t.Helper()
	c := NewWithDialer(newTestStore(t), nil)
	sess := &session{
		creds: &store.Creds{Me: "me@s.whatsapp.net"},
		send: func(n wire.Node) error {
			// Echo the request id into the reply and deliver asynchronously
			// is unnecessary: deliverIQ can run inline because registerIQ
			// uses a buffered (cap 1) channel.
			r := reply
			r.Attrs = map[string]string{"type": "result", "id": n.Attrs["id"]}
			c.deliverIQ(r)
			return nil
		},
	}
	c.setActive(sess)
	return c
}

func TestMediaConn_FetchAndCache(t *testing.T) {
	mc := wire.Node{
		Tag:   "iq",
		Attrs: map[string]string{},
		Content: []wire.Node{
			{
				Tag:   "media_conn",
				Attrs: map[string]string{"auth": "A1", "ttl": "1000"},
				Content: []wire.Node{
					{Tag: "host", Attrs: map[string]string{"hostname": "h1.example"}},
				},
			},
		},
	}
	c := newClientWithMediaConnReply(t, mc)

	got, err := c.mediaConn(context.Background())
	if err != nil {
		t.Fatalf("mediaConn: %v", err)
	}
	if got.Auth != "A1" || got.TTL != 1000 || len(got.Hosts) != 1 || got.Hosts[0] != "h1.example" {
		t.Fatalf("bad conn: %+v", got)
	}

	// Second call must hit cache: break the session so a re-fetch would fail.
	c.setActive(&session{creds: got2Creds(), send: func(wire.Node) error {
		t.Fatal("mediaConn should have used the cache, not re-queried")
		return nil
	}})
	again, err := c.mediaConn(context.Background())
	if err != nil {
		t.Fatalf("cached mediaConn: %v", err)
	}
	if again != got {
		t.Fatal("expected cached pointer")
	}
}

func got2Creds() *store.Creds { return &store.Creds{Me: "me@s.whatsapp.net"} }

func TestMediaConn_NoSession(t *testing.T) {
	c := NewWithDialer(newTestStore(t), nil)
	if _, err := c.mediaConn(context.Background()); err == nil {
		t.Fatal("expected error without live session")
	}
}

// --- DownloadMedia end-to-end (offline, fake fetcher) ---

type clientFakeFetcher struct {
	resp    *http.Response
	lastReq *http.Request
	body    []byte
}

func (f *clientFakeFetcher) Do(req *http.Request) (*http.Response, error) {
	f.lastReq = req
	if req.Body != nil {
		f.body, _ = io.ReadAll(req.Body)
	}
	return f.resp, nil
}

// vectors mirror the media package's golden file; reload here for the e2e test.
type cliVectors struct {
	MediaKeyHex string `json:"mediaKeyHex"`
	Plaintexts  map[string]struct {
		PlaintextHex string `json:"plaintextHex"`
	} `json:"plaintexts"`
	Cases []struct {
		Plaintext     string `json:"plaintext"`
		MediaType     string `json:"mediaType"`
		CiphertextHex string `json:"ciphertextHex"`
	} `json:"cases"`
}

func loadCliVectors(t *testing.T) cliVectors {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean("../../testdata/media/media_vectors.json"))
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var v cliVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return v
}

func TestDownloadMedia_EndToEnd(t *testing.T) {
	v := loadCliVectors(t)
	mediaKey, err := hex.DecodeString(v.MediaKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	// Pick the image case.
	var imgCase = -1
	for i, c := range v.Cases {
		if c.MediaType == "image" {
			imgCase = i
			break
		}
	}
	if imgCase < 0 {
		t.Fatal("no image case")
	}
	c := v.Cases[imgCase]
	enc, _ := hex.DecodeString(c.CiphertextHex)
	wantPlain, _ := hex.DecodeString(v.Plaintexts[c.Plaintext].PlaintextHex)

	mc := wire.Node{Content: []wire.Node{
		{Tag: "media_conn", Attrs: map[string]string{"auth": "A", "ttl": "1000"}, Content: []wire.Node{
			{Tag: "host", Attrs: map[string]string{"hostname": "media-dl.example"}},
		}},
	}}
	client := newClientWithMediaConnReply(t, mc)

	f := &clientFakeFetcher{resp: &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(enc)), Header: make(http.Header)}}
	info := &MediaInfo{
		Kind:       MediaImage,
		MediaKey:   mediaKey,
		DirectPath: "/v/t62/blob_n.enc",
	}
	got, err := client.DownloadMediaWithFetcher(context.Background(), f, info)
	if err != nil {
		t.Fatalf("DownloadMedia: %v", err)
	}
	if !bytes.Equal(got, wantPlain) {
		t.Fatalf("plaintext mismatch")
	}
	if f.lastReq.URL.String() != "https://media-dl.example/v/t62/blob_n.enc" {
		t.Fatalf("URL = %q", f.lastReq.URL.String())
	}
}

// --- live uploader wiring (offline, fake fetcher) ---

func TestEnableMediaTransfer_Upload(t *testing.T) {
	mc := wire.Node{Content: []wire.Node{
		{Tag: "media_conn", Attrs: map[string]string{"auth": "AUTH", "ttl": "1000"}, Content: []wire.Node{
			{Tag: "host", Attrs: map[string]string{"hostname": "up.example"}},
		}},
	}}
	c := newClientWithMediaConnReply(t, mc)

	f := &clientFakeFetcher{resp: &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(bytes.NewReader([]byte(`{"direct_path":"/dp","url":"https://u","handle":"H"}`))),
		Header:     make(http.Header),
	}}
	c.EnableMediaTransfer(f)

	if c.uploader == nil {
		t.Fatal("uploader not installed")
	}
	enc := []byte("encrypted-blob-bytes")
	dp, url, err := c.uploader.Upload(context.Background(), enc, media.Image)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if dp != "/dp" || url != "https://u" {
		t.Fatalf("dp=%q url=%q", dp, url)
	}
	if !bytes.Equal(f.body, enc) {
		t.Fatalf("uploaded body != enc")
	}
	if f.lastReq.Method != http.MethodPost {
		t.Fatalf("method = %q", f.lastReq.Method)
	}
}
