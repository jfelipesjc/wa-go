package client

import (
	"bytes"
	"testing"
	"time"

	"github.com/felipeleal/wa-go/internal/media"
	"github.com/felipeleal/wa-go/internal/signal"
	"github.com/felipeleal/wa-go/internal/waproto"
	"github.com/felipeleal/wa-go/internal/wire"
	"google.golang.org/protobuf/proto"
)

// --- text builders ---

func TestBuildTextMessage(t *testing.T) {
	m := buildTextMessage("ola")
	if m.GetConversation() != "ola" {
		t.Fatalf("conversation = %q", m.GetConversation())
	}
}

func TestBuildTextReplyMessage(t *testing.T) {
	qk := &waproto.MessageKey{
		RemoteJid:   proto.String("5551111111@s.whatsapp.net"),
		Id:          proto.String("QUOTED1"),
		Participant: proto.String("5552222222@s.whatsapp.net"),
		FromMe:      proto.Bool(false),
	}
	quoted := buildTextMessage("original")
	m := buildTextReplyMessage("a reply", qk, quoted)

	et := m.GetExtendedTextMessage()
	if et == nil {
		t.Fatal("missing extendedTextMessage")
	}
	if et.GetText() != "a reply" {
		t.Fatalf("text = %q", et.GetText())
	}
	ci := et.GetContextInfo()
	if ci == nil {
		t.Fatal("missing contextInfo")
	}
	if ci.GetStanzaId() != "QUOTED1" {
		t.Fatalf("stanzaId = %q", ci.GetStanzaId())
	}
	if ci.GetParticipant() != "5552222222@s.whatsapp.net" {
		t.Fatalf("participant = %q", ci.GetParticipant())
	}
	if ci.GetRemoteJid() != "5551111111@s.whatsapp.net" {
		t.Fatalf("remoteJid = %q", ci.GetRemoteJid())
	}
	if ci.GetQuotedMessage().GetConversation() != "original" {
		t.Fatalf("quotedMessage = %q", ci.GetQuotedMessage().GetConversation())
	}
}

func TestBuildTextMentionMessage(t *testing.T) {
	mentions := []string{"5551111111@s.whatsapp.net", "5552222222@s.whatsapp.net"}
	m := buildTextMentionMessage("hi @111 @222", mentions)
	et := m.GetExtendedTextMessage()
	if et == nil || et.GetText() != "hi @111 @222" {
		t.Fatalf("text wrong: %+v", et)
	}
	got := et.GetContextInfo().GetMentionedJid()
	if len(got) != 2 || got[0] != mentions[0] || got[1] != mentions[1] {
		t.Fatalf("mentionedJid = %v", got)
	}
}

// --- reaction / edit / delete ---

func TestBuildReactionMessage(t *testing.T) {
	tk := &waproto.MessageKey{
		RemoteJid: proto.String("5551111111@s.whatsapp.net"),
		Id:        proto.String("TARGET1"),
		FromMe:    proto.Bool(true),
	}
	now := time.Unix(1700000000, 0)
	m := buildReactionMessage(tk, "👍", now)
	rm := m.GetReactionMessage()
	if rm == nil {
		t.Fatal("missing reactionMessage")
	}
	if rm.GetText() != "👍" {
		t.Fatalf("emoji = %q", rm.GetText())
	}
	if rm.GetKey().GetId() != "TARGET1" {
		t.Fatalf("key id = %q", rm.GetKey().GetId())
	}
	if rm.GetSenderTimestampMs() != now.UnixMilli() {
		t.Fatalf("senderTimestampMs = %d", rm.GetSenderTimestampMs())
	}
}

func TestBuildEditMessage(t *testing.T) {
	tk := &waproto.MessageKey{Id: proto.String("EDIT1"), FromMe: proto.Bool(true)}
	m := buildEditMessage(tk, "new text", time.Unix(1700000000, 0))
	pm := m.GetProtocolMessage()
	if pm == nil {
		t.Fatal("missing protocolMessage")
	}
	if pm.GetType() != waproto.ProtocolMessage_MESSAGE_EDIT {
		t.Fatalf("type = %v, want MESSAGE_EDIT", pm.GetType())
	}
	if pm.GetKey().GetId() != "EDIT1" {
		t.Fatalf("key id = %q", pm.GetKey().GetId())
	}
	if pm.GetEditedMessage().GetConversation() != "new text" {
		t.Fatalf("editedMessage = %q", pm.GetEditedMessage().GetConversation())
	}
}

func TestBuildRevokeMessage(t *testing.T) {
	tk := &waproto.MessageKey{Id: proto.String("DEL1"), FromMe: proto.Bool(true)}
	m := buildRevokeMessage(tk)
	pm := m.GetProtocolMessage()
	if pm == nil {
		t.Fatal("missing protocolMessage")
	}
	if pm.GetType() != waproto.ProtocolMessage_REVOKE {
		t.Fatalf("type = %v, want REVOKE", pm.GetType())
	}
	if pm.GetKey().GetId() != "DEL1" {
		t.Fatalf("key id = %q", pm.GetKey().GetId())
	}
}

// --- media builders ---

func TestBuildImageMessage_MediaFields(t *testing.T) {
	data := []byte("this is a fake jpeg payload, longer than one aes block to be safe")
	var mediaKey [32]byte
	for i := range mediaKey {
		mediaKey[i] = byte(i)
	}
	enc, fileSha, fileEncSha, err := media.Encrypt(data, mediaKey, media.Image)
	if err != nil {
		t.Fatalf("media.Encrypt: %v", err)
	}
	_ = enc

	info := &mediaSendInfo{
		mediaKey:      mediaKey,
		fileSha256:    fileSha,
		fileEncSha256: fileEncSha,
		fileLength:    uint64(len(data)),
		directPath:    "/v/t62.7118-24/dp",
		url:           "https://mmg.whatsapp.net/d",
	}
	opts := MediaOpts{Mimetype: "image/jpeg", Caption: "look", Width: 640, Height: 480}
	m := buildImageMessage(info, opts)
	im := m.GetImageMessage()
	if im == nil {
		t.Fatal("missing imageMessage")
	}
	if !bytes.Equal(im.GetMediaKey(), mediaKey[:]) {
		t.Fatal("mediaKey mismatch")
	}
	if !bytes.Equal(im.GetFileSha256(), fileSha[:]) {
		t.Fatal("fileSha256 mismatch")
	}
	if !bytes.Equal(im.GetFileEncSha256(), fileEncSha[:]) {
		t.Fatal("fileEncSha256 mismatch")
	}
	if im.GetFileLength() != uint64(len(data)) {
		t.Fatalf("fileLength = %d", im.GetFileLength())
	}
	if im.GetMimetype() != "image/jpeg" || im.GetCaption() != "look" {
		t.Fatalf("mimetype/caption wrong: %q %q", im.GetMimetype(), im.GetCaption())
	}
	if im.GetWidth() != 640 || im.GetHeight() != 480 {
		t.Fatalf("dims wrong: %d x %d", im.GetWidth(), im.GetHeight())
	}
	if im.GetDirectPath() != info.directPath || im.GetUrl() != info.url {
		t.Fatalf("directPath/url wrong: %q %q", im.GetDirectPath(), im.GetUrl())
	}
}

func TestBuildAudioMessage_PTT(t *testing.T) {
	info := &mediaSendInfo{fileLength: 100, directPath: "dp", url: "u"}
	m := buildAudioMessage(info, MediaOpts{Mimetype: "audio/ogg; codecs=opus", Seconds: 5, PTT: true})
	am := m.GetAudioMessage()
	if am == nil || !am.GetPtt() || am.GetSeconds() != 5 {
		t.Fatalf("audio fields wrong: %+v", am)
	}
}

func TestBuildDocumentMessage(t *testing.T) {
	info := &mediaSendInfo{fileLength: 2048, directPath: "dp", url: "u"}
	m := buildDocumentMessage(info, MediaOpts{Mimetype: "application/pdf", FileName: "x.pdf", PageCount: 3})
	dm := m.GetDocumentMessage()
	if dm == nil || dm.GetFileName() != "x.pdf" || dm.GetPageCount() != 3 || dm.GetMimetype() != "application/pdf" {
		t.Fatalf("document fields wrong: %+v", dm)
	}
}

func TestBuildStickerMessage_DefaultMime(t *testing.T) {
	info := &mediaSendInfo{fileLength: 10, directPath: "dp", url: "u"}
	m := buildStickerMessage(info, MediaOpts{})
	sm := m.GetStickerMessage()
	if sm == nil || sm.GetMimetype() != "image/webp" {
		t.Fatalf("sticker default mime wrong: %+v", sm)
	}
}

// SendImage with no uploader and no Ref must return a clear error.
func TestSendImage_RequiresUploadConfig(t *testing.T) {
	c := &Client{}
	_, err := c.prepareMedia(nil, []byte("data"), media.Image, MediaOpts{})
	if err != ErrMediaUploadNotConfigured {
		t.Fatalf("err = %v, want ErrMediaUploadNotConfigured", err)
	}
}

func TestPrepareMedia_WithRef(t *testing.T) {
	c := &Client{}
	info, err := c.prepareMedia(nil, []byte("hello media payload"), media.Image, MediaOpts{
		Ref: &MediaRef{DirectPath: "/dp", URL: "https://u"},
	})
	if err != nil {
		t.Fatalf("prepareMedia: %v", err)
	}
	if info.directPath != "/dp" || info.url != "https://u" {
		t.Fatalf("ref not applied: %+v", info)
	}
	if info.mediaKey == [32]byte{} {
		t.Fatal("mediaKey not generated")
	}
	if info.fileLength != uint64(len("hello media payload")) {
		t.Fatalf("fileLength = %d", info.fileLength)
	}
}

// --- group builders: SKDM round-trip + stanza structure ---

// TestGroupSenderKeyRoundTrip mirrors the send-side group crypto: we mint a
// sender key, build the SKDM Message, then a peer parses the SKDM, installs it,
// and decrypts the EncryptGroup ciphertext back to the original plaintext.
func TestGroupSenderKeyRoundTrip(t *testing.T) {
	const groupJID = "120363000000000000@g.us"

	// Sender side: mint a sender key via the GroupCipher (as sendGroupMessage does).
	sendRec := &signal.SenderKeyRecord{}
	sendCipher := signal.NewGroupCipher(sendRec)
	c := &Client{}
	skdm, err := c.mintSenderKey(sendCipher)
	if err != nil {
		t.Fatalf("mintSenderKey: %v", err)
	}

	// Build the SKDM distribution Message and confirm it carries axolotl bytes.
	skdmMsg := buildSenderKeyDistributionMessage(groupJID, skdm)
	wrap := skdmMsg.GetSenderKeyDistributionMessage()
	if wrap == nil || wrap.GetGroupId() != groupJID {
		t.Fatalf("skdm wrap wrong: %+v", wrap)
	}
	axolotl := wrap.GetAxolotlSenderKeyDistributionMessage()
	if len(axolotl) == 0 {
		t.Fatal("empty axolotl skdm bytes")
	}

	// Encrypt content as skmsg.
	want := buildTextMessage("hello group")
	plain, err := encodeWAMessage(want)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	skMsg, err := sendCipher.EncryptGroup(plain)
	if err != nil {
		t.Fatalf("EncryptGroup: %v", err)
	}

	// Peer side: parse the SKDM, install, decrypt.
	parsed, err := signal.ParseSenderKeyDistributionMessage(axolotl)
	if err != nil {
		t.Fatalf("ParseSKDM: %v", err)
	}
	recvRec := &signal.SenderKeyRecord{}
	recvCipher := signal.NewGroupCipher(recvRec)
	recvCipher.ProcessSenderKeyDistribution(parsed)
	gotPadded, err := recvCipher.DecryptGroup(skMsg)
	if err != nil {
		t.Fatalf("DecryptGroup: %v", err)
	}
	gotPlain, err := unpadMessage(gotPadded)
	if err != nil {
		t.Fatalf("unpad: %v", err)
	}
	var gotMsg waproto.Message
	if err := proto.Unmarshal(gotPlain, &gotMsg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if gotMsg.GetConversation() != "hello group" {
		t.Fatalf("round-trip = %q", gotMsg.GetConversation())
	}
}

func TestBuildGroupMessageStanza_Structure(t *testing.T) {
	const groupJID = "120363000000000000@g.us"
	// Two participant SKDM nodes; one carries a pkmsg so device-identity attaches.
	participants := []wire.Node{
		toEncNode("5551111111@s.whatsapp.net", "pkmsg", []byte("skdm-ct-1")),
		toEncNode("5552222222@s.whatsapp.net", "msg", []byte("skdm-ct-2")),
	}
	skMsg := []byte("skmsg-content-ciphertext")
	account := []byte("device-identity-blob")

	stanza := buildGroupMessageStanza("MID1", groupJID, "text", participants, skMsg, account)

	if stanza.Tag != "message" || stanza.Attrs["to"] != groupJID || stanza.Attrs["type"] != "text" || stanza.Attrs["id"] != "MID1" {
		t.Fatalf("stanza attrs wrong: %+v", stanza.Attrs)
	}
	parts, ok := childByTag(stanza, "participants")
	if !ok {
		t.Fatal("missing <participants>")
	}
	if len(childrenByTag(parts, "to")) != 2 {
		t.Fatalf("want 2 <to> nodes")
	}
	enc, ok := childByTag(stanza, "enc")
	if !ok {
		t.Fatal("missing skmsg <enc>")
	}
	if enc.Attrs["type"] != "skmsg" || enc.Attrs["v"] != "2" {
		t.Fatalf("enc attrs wrong: %+v", enc.Attrs)
	}
	if !bytes.Equal(nodeBytes(enc), skMsg) {
		t.Fatal("skmsg content mismatch")
	}
	if _, ok := childByTag(stanza, "device-identity"); !ok {
		t.Fatal("expected <device-identity> (pkmsg present)")
	}
}

// When no SKDM is being distributed (sender key already established), the stanza
// has no <participants> and no device-identity, just the skmsg <enc>.
func TestBuildGroupMessageStanza_NoDistribution(t *testing.T) {
	stanza := buildGroupMessageStanza("MID2", "120363@g.us", "text", nil, []byte("ct"), []byte("acct"))
	if _, ok := childByTag(stanza, "participants"); ok {
		t.Fatal("unexpected <participants>")
	}
	if _, ok := childByTag(stanza, "device-identity"); ok {
		t.Fatal("unexpected <device-identity>")
	}
	if _, ok := childByTag(stanza, "enc"); !ok {
		t.Fatal("missing skmsg <enc>")
	}
}

// --- presence / typing / read builders ---

func TestPresenceNode(t *testing.T) {
	n := presenceNode(PresenceAvailable, "5550@s.whatsapp.net")
	if n.Tag != "presence" || n.Attrs["type"] != "available" || n.Attrs["from"] != "5550@s.whatsapp.net" {
		t.Fatalf("presence node wrong: %+v", n)
	}
	n2 := presenceNode(PresenceUnavailable, "")
	if n2.Attrs["type"] != "unavailable" {
		t.Fatalf("unavailable type wrong: %+v", n2.Attrs)
	}
	if _, ok := n2.Attrs["from"]; ok {
		t.Fatal("from should be omitted when me empty")
	}
}

func TestChatStateNode(t *testing.T) {
	n := chatStateNode("5551111111@s.whatsapp.net", ChatStateComposing)
	if n.Tag != "chatstate" || n.Attrs["to"] != "5551111111@s.whatsapp.net" {
		t.Fatalf("chatstate attrs wrong: %+v", n.Attrs)
	}
	if _, ok := childByTag(n, "composing"); !ok {
		t.Fatal("missing <composing>")
	}
	n2 := chatStateNode("x@s.whatsapp.net", ChatStatePaused)
	if _, ok := childByTag(n2, "paused"); !ok {
		t.Fatal("missing <paused>")
	}
}

func TestReadReceiptNode_Single(t *testing.T) {
	n := readReceiptNode("5551111111@s.whatsapp.net", "", []string{"M1"})
	if n.Tag != "receipt" || n.Attrs["type"] != "read" || n.Attrs["id"] != "M1" || n.Attrs["to"] != "5551111111@s.whatsapp.net" {
		t.Fatalf("receipt attrs wrong: %+v", n.Attrs)
	}
	if _, ok := childByTag(n, "list"); ok {
		t.Fatal("single id should not have <list>")
	}
}

func TestReadReceiptNode_MultiWithParticipant(t *testing.T) {
	n := readReceiptNode("120363@g.us", "5552222222@s.whatsapp.net", []string{"M1", "M2", "M3"})
	if n.Attrs["participant"] != "5552222222@s.whatsapp.net" {
		t.Fatalf("participant = %q", n.Attrs["participant"])
	}
	if n.Attrs["id"] != "M1" {
		t.Fatalf("id = %q", n.Attrs["id"])
	}
	list, ok := childByTag(n, "list")
	if !ok {
		t.Fatal("missing <list>")
	}
	items := childrenByTag(list, "item")
	if len(items) != 2 || items[0].Attrs["id"] != "M2" || items[1].Attrs["id"] != "M3" {
		t.Fatalf("items wrong: %+v", items)
	}
}
