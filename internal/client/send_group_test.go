package client

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jfelipesjc/wa-go/internal/keys"
	"github.com/jfelipesjc/wa-go/internal/signal"
	"github.com/jfelipesjc/wa-go/internal/store"
	"github.com/jfelipesjc/wa-go/internal/waproto"
	"google.golang.org/protobuf/proto"
)

// TestSendGroupText_ProducesDecryptableSKMsg exercises the full group send path
// offline: alice sends "ola grupo" to a group whose only (test) participant is
// bob. A scripted conn answers the usync + prekey-bundle iqs, then we capture
// the <message>:
//   - the <participants> carry a 1:1 pkmsg whose plaintext is the SKDM Message;
//     bob's responder X3DH decrypts it and installs alice's sender key.
//   - the <enc type=skmsg> carries the content; bob decrypts it with the now
//     installed sender key, recovering the WAProto.Message text.
func TestSendGroupText_ProducesDecryptableSKMsg(t *testing.T) {
	v := loadVVectors(t)

	aliceIdentity := v.Alice.IdentityKeyPair.keyPair(t)
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "alice.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	c := New(st)
	aliceJID := "5550000000@s.whatsapp.net"
	creds := &store.Creds{
		IdentityKey:    store.CredKeyPair{Priv: aliceIdentity.Priv[:], Pub: aliceIdentity.Pub[:]},
		RegistrationID: v.Alice.RegistrationID,
		Me:             aliceJID,
		Registered:     true,
	}

	bobIdentity := v.Bob.IdentityKeyPair.keyPair(t)
	bobSignedPre := v.Bob.SignedPreKey.KeyPair.keyPair(t)
	bobPreKey := v.Bob.PreKey.KeyPair.keyPair(t)
	bobJID := "5551111111@s.whatsapp.net"
	const groupJID = "120363000000000000@g.us"

	signedMsg := signalPubKey33(bobSignedPre.Pub)
	spkSig, err := keys.Sign(bobIdentity.Priv, signedMsg)
	if err != nil {
		t.Fatalf("sign spk: %v", err)
	}

	conn := newGatedConn()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	loopDone := make(chan struct{})
	go func() {
		_ = c.loginLoop(ctx, conn, creds)
		close(loopDone)
	}()
	_ = waitActive(t, c)

	go func() {
		for {
			n, ok := conn.nextWritten(ctx)
			if !ok {
				return
			}
			switch n.Attrs["xmlns"] {
			case "usync":
				conn.feed(usyncResultFor(n.Attrs["id"], bobJID))
			case "encrypt":
				conn.feed(preKeyResultFor(n.Attrs["id"], bobJID, bobIdentity.Pub, v.Bob.SignedPreKey.ID, bobSignedPre.Pub, spkSig, v.Bob.PreKey.ID, bobPreKey.Pub, v.Bob.RegistrationID))
			}
		}
	}()

	wantText := "ola grupo"
	msgID, err := c.SendGroupText(ctx, groupJID, []string{bobJID}, wantText)
	if err != nil {
		t.Fatalf("SendGroupText: %v", err)
	}

	stanza := waitForTag(t, conn, "message")
	if stanza.Attrs["to"] != groupJID || stanza.Attrs["id"] != msgID {
		t.Fatalf("group message attrs wrong: %+v", stanza.Attrs)
	}

	// (a) Extract + decrypt the 1:1 SKDM pkmsg as bob, install the sender key.
	participants, ok := childByTag(stanza, "participants")
	if !ok {
		t.Fatal("group message missing <participants>")
	}
	tos := childrenByTag(participants, "to")
	if len(tos) != 1 || tos[0].Attrs["jid"] != bobJID {
		t.Fatalf("participants wrong: %+v", tos)
	}
	skdmEnc, ok := childByTag(tos[0], "enc")
	if !ok || skdmEnc.Attrs["type"] != "pkmsg" {
		t.Fatalf("SKDM enc wrong: %+v", skdmEnc.Attrs)
	}
	skdmPadded, _, err := signal.ProcessPreKeyMessage(nodeBytes(skdmEnc), bobIdentity, bobSignedPre, &bobPreKey, v.Bob.RegistrationID)
	if err != nil {
		t.Fatalf("bob ProcessPreKeyMessage(SKDM): %v", err)
	}
	skdmPlain, err := unpadMessage(skdmPadded)
	if err != nil {
		t.Fatalf("unpad skdm: %v", err)
	}
	var skdmMsg waproto.Message
	if err := proto.Unmarshal(skdmPlain, &skdmMsg); err != nil {
		t.Fatalf("unmarshal skdm message: %v", err)
	}
	skdm := senderKeyDistribution(&skdmMsg)
	if skdm == nil {
		t.Fatal("no SKDM in distributed message")
	}
	bobRec := &signal.SenderKeyRecord{}
	bobCipher := signal.NewGroupCipher(bobRec)
	bobCipher.ProcessSenderKeyDistribution(skdm)

	// (b) Decrypt the skmsg content with the installed sender key.
	skEnc, ok := childByTag(stanza, "enc")
	if !ok || skEnc.Attrs["type"] != "skmsg" {
		t.Fatalf("content enc wrong: %+v", skEnc.Attrs)
	}
	contentPadded, err := bobCipher.DecryptGroup(nodeBytes(skEnc))
	if err != nil {
		t.Fatalf("bob DecryptGroup: %v", err)
	}
	contentPlain, err := unpadMessage(contentPadded)
	if err != nil {
		t.Fatalf("unpad content: %v", err)
	}
	var content waproto.Message
	if err := proto.Unmarshal(contentPlain, &content); err != nil {
		t.Fatalf("unmarshal content: %v", err)
	}
	if content.GetConversation() != wantText {
		t.Fatalf("group text = %q, want %q", content.GetConversation(), wantText)
	}

	// Our sender key record must have been persisted for (group, me).
	if blob, ok, _ := st.LoadSenderKey(groupJID, aliceJID); !ok || len(blob) == 0 {
		t.Fatal("sender key record not persisted for (group, me)")
	}

	cancel()
	conn.close()
	<-loopDone
}
