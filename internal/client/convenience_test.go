package client

import (
	"testing"
	"time"

	"github.com/jfelipesjc/wa-go/internal/media"
	"github.com/jfelipesjc/wa-go/internal/waproto"
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

// DeleteMessageByID and EditTextByID must build the target MessageKey from the
// plain JID/id/fromMe args (via buildReactionKey) and thread it into the right
// ProtocolMessage. We verify at the builder level (no session needed): the key
// produced by buildReactionKey is what the revoke/edit builders carry.
func TestDeleteEditByID_BuildCorrectKey(t *testing.T) {
	key := buildReactionKey("5551111111@s.whatsapp.net", "TARGET99", true)

	rev := buildRevokeMessage(key)
	pm := rev.GetProtocolMessage()
	if pm == nil || pm.GetType() != waproto.ProtocolMessage_REVOKE {
		t.Fatalf("revoke type wrong: %+v", pm)
	}
	if pm.GetKey().GetId() != "TARGET99" || pm.GetKey().GetRemoteJid() != "5551111111@s.whatsapp.net" || !pm.GetKey().GetFromMe() {
		t.Fatalf("revoke key wrong: %+v", pm.GetKey())
	}

	edit := buildEditMessage(buildReactionKey("g@g.us", "ED1", false), "new body", time.Now())
	epm := edit.GetProtocolMessage()
	if epm.GetKey().GetId() != "ED1" || epm.GetKey().GetFromMe() {
		t.Fatalf("edit key wrong: %+v", epm.GetKey())
	}
	if epm.GetEditedMessage().GetConversation() != "new body" {
		t.Fatalf("edited body = %q", epm.GetEditedMessage().GetConversation())
	}
}

// SendButtonsSimple must pair parallel id/text slices into []Button, bounded by
// the shorter slice.
func TestBuildButtonsFromSlices(t *testing.T) {
	btns := buildButtonsFromSlices([]string{"b1", "b2", "b3"}, []string{"Yes", "No"})
	if len(btns) != 2 {
		t.Fatalf("len = %d, want 2 (bounded by shorter)", len(btns))
	}
	if btns[0] != (Button{ID: "b1", Text: "Yes"}) || btns[1] != (Button{ID: "b2", Text: "No"}) {
		t.Fatalf("buttons wrong: %+v", btns)
	}
	if got := buildButtonsFromSlices(nil, nil); len(got) != 0 {
		t.Fatalf("empty inputs -> %d buttons", len(got))
	}
}

// SendListSimple must assemble one section per title with rows drawn from the
// parallel inner slices, tolerating short/missing descriptions.
func TestBuildSectionsFromSlices(t *testing.T) {
	secs := buildSectionsFromSlices(
		[]string{"Sec A", "Sec B"},
		[][]string{{"R1", "R2"}, {"R3"}},
		[][]string{{"first"}}, // only one desc for section 0, none for section 1
		[][]string{{"r1", "r2"}, {"r3"}},
	)
	if len(secs) != 2 {
		t.Fatalf("sections len = %d", len(secs))
	}
	if secs[0].Title != "Sec A" || len(secs[0].Rows) != 2 {
		t.Fatalf("section[0] wrong: %+v", secs[0])
	}
	if secs[0].Rows[0] != (ListRow{Title: "R1", RowID: "r1", Description: "first"}) {
		t.Fatalf("row[0] wrong: %+v", secs[0].Rows[0])
	}
	if secs[0].Rows[1] != (ListRow{Title: "R2", RowID: "r2"}) {
		t.Fatalf("row[1] should have empty desc: %+v", secs[0].Rows[1])
	}
	if secs[1].Title != "Sec B" || len(secs[1].Rows) != 1 || secs[1].Rows[0].RowID != "r3" {
		t.Fatalf("section[1] wrong: %+v", secs[1])
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
