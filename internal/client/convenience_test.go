package client

import (
	"testing"
	"time"

	"github.com/felipeleal/wa-go/internal/media"
)

// React must build a MessageKey carrying the supplied remote JID, message id and
// fromMe flag, then fold it into a ReactionMessage with the emoji text.
func TestBuildReactionKey(t *testing.T) {
	key := buildReactionKey("5551111111@s.whatsapp.net", "MSGID42", true)
	if key.GetRemoteJid() != "5551111111@s.whatsapp.net" {
		t.Fatalf("RemoteJid = %q", key.GetRemoteJid())
	}
	if key.GetId() != "MSGID42" {
		t.Fatalf("Id = %q", key.GetId())
	}
	if !key.GetFromMe() {
		t.Fatalf("FromMe = false, want true")
	}

	// The key threads into a well-formed ReactionMessage.
	m := buildReactionMessage(buildReactionKey("g@g.us", "X", false), "👍", time.Now())
	rm := m.GetReactionMessage()
	if rm == nil || rm.GetText() != "👍" {
		t.Fatalf("reaction text wrong: %+v", rm)
	}
	if rm.GetKey().GetId() != "X" || rm.GetKey().GetFromMe() {
		t.Fatalf("reaction key wrong: %+v", rm.GetKey())
	}
}

// The *Bytes media helpers must construct MediaOpts and reach the upload path; on
// a client with no uploader configured they surface ErrMediaUploadNotConfigured,
// proving the bytes/opts plumbing is wired through to SendImage/etc.
func TestSendBytesHelpers_ReachMediaPath(t *testing.T) {
	c := &Client{}
	data := []byte("payload")
	cases := []struct {
		name string
		call func() (string, error)
	}{
		{"image", func() (string, error) { return c.SendImageBytes(nil, "j@x", data, "cap", "image/jpeg") }},
		{"video", func() (string, error) { return c.SendVideoBytes(nil, "j@x", data, "cap", "video/mp4") }},
		{"audio", func() (string, error) { return c.SendAudioBytes(nil, "j@x", data, "audio/ogg") }},
		{"document", func() (string, error) { return c.SendDocumentBytes(nil, "j@x", data, "f.pdf", "application/pdf") }},
	}
	for _, tc := range cases {
		if _, err := tc.call(); err != ErrMediaUploadNotConfigured {
			t.Fatalf("%s: err = %v, want ErrMediaUploadNotConfigured", tc.name, err)
		}
	}
}

// EnableDefaultMediaTransfer installs a live uploader so prepareMedia no longer
// returns ErrMediaUploadNotConfigured (it now fails later, at media_conn fetch,
// which proves the uploader is wired).
func TestEnableDefaultMediaTransfer_InstallsUploader(t *testing.T) {
	c := &Client{}
	if c.uploader != nil {
		t.Fatal("uploader should start nil")
	}
	c.EnableDefaultMediaTransfer()
	if c.uploader == nil {
		t.Fatal("EnableDefaultMediaTransfer did not install an uploader")
	}
	// With an uploader installed, prepareMedia passes the config gate and only
	// fails when it tries to resolve a media_conn (no session).
	_, err := c.prepareMedia(nil, []byte("d"), media.Image, MediaOpts{})
	if err == ErrMediaUploadNotConfigured {
		t.Fatal("uploader installed but still reported not configured")
	}
}
