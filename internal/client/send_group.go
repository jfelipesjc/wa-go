package client

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/felipeleal/wa-go/internal/keys"
	"github.com/felipeleal/wa-go/internal/signal"
	"github.com/felipeleal/wa-go/internal/waproto"
	"github.com/felipeleal/wa-go/internal/wire"
	"google.golang.org/protobuf/proto"
)

// groupParticipantJIDs fetches the group's metadata and returns the participant
// JIDs to fan out to. It lets the convenience senders (SendText/SendImage/…)
// accept a @g.us destination directly: they resolve the member list here instead
// of requiring the caller to pass it. Prefers each participant's phone JID
// (@s.whatsapp.net) for usync; falls back to whatever JID the metadata carries.
func (c *Client) groupParticipantJIDs(ctx context.Context, groupJID string) ([]string, error) {
	meta, err := c.GroupMetadata(ctx, groupJID)
	if err != nil {
		return nil, fmt.Errorf("client: group metadata for send: %w", err)
	}
	parts := make([]string, 0, len(meta.Participants))
	for _, p := range meta.Participants {
		if p.JID != "" {
			parts = append(parts, p.JID)
		}
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("client: group %s has no resolvable participants", groupJID)
	}
	return parts, nil
}

// SendGroupText sends a text message to a group, given the group JID and the
// list of member JIDs to fan out to.
//
// Member list: this build does not query group metadata (the <iq xmlns=w:g2>
// participant fetch is not implemented), so the caller passes the participant
// JIDs explicitly. Each participant's devices are resolved via usync.
func (c *Client) SendGroupText(ctx context.Context, groupJID string, participants []string, text string) (string, error) {
	return c.sendGroupMessage(ctx, groupJID, participants, buildTextMessage(text), sendOpts{pacerHint: len(text)})
}

// sendGroupMessage is the generic group send core (the analogue of sendMessage
// for groups). It mirrors Baileys' relayMessage for a `@g.us` destination:
//
//  1. consult the pacer,
//  2. usync every participant to enumerate their devices,
//  3. assertSessions: ensure a 1:1 Signal session with each device,
//  4. load/create our SenderKeyRecord for (group, me); if fresh, mint a sender
//     key (CreateSenderKeyDistribution) to distribute,
//  5. encrypt the content as a SenderKeyMessage (<enc type=skmsg>),
//  6. when distributing, wrap the SKDM in a Message and encrypt it 1:1 to every
//     device, placed in <participants>,
//  7. assemble <message to=group type=...><participants>(1:1 SKDM)</participants>
//     <enc type=skmsg>(content)</enc>[<device-identity>]</message> and send,
//  8. persist our advanced SenderKeyRecord.
func (c *Client) sendGroupMessage(ctx context.Context, groupJID string, participants []string, msg *waproto.Message, opts sendOpts) (string, error) {
	sess, ok := c.activeSession()
	if !ok {
		return "", errors.New("client: not logged in (no active session)")
	}
	if !isGroupJID(groupJID) {
		return "", fmt.Errorf("client: %q is not a group JID", groupJID)
	}
	if len(participants) == 0 {
		return "", errors.New("client: group send requires participant JIDs")
	}

	if c.pacer != nil {
		if !c.pacer.Allow() {
			return "", errors.New("client: send rate limit exceeded")
		}
		if _, err := c.pacer.Wait(ctx, opts.pacerHint); err != nil {
			return "", fmt.Errorf("client: pacer wait: %w", err)
		}
	}

	msgID := generateMessageID()

	// (2) usync every participant -> their devices.
	ctx, cancel := context.WithTimeout(ctx, sendIQTimeout)
	defer cancel()

	devices, err := c.fetchDevices(ctx, sess, participants...)
	if err != nil {
		return "", fmt.Errorf("client: group usync: %w", err)
	}
	if len(devices) == 0 {
		return "", errors.New("client: no participant devices resolved")
	}

	// (3) ensure 1:1 sessions with each device (needed to deliver the SKDM).
	if err := c.assertSessions(ctx, sess, devices); err != nil {
		return "", err
	}

	// (4) load/create our sender key for this group.
	me := sess.creds.Me
	if me == "" {
		return "", errors.New("client: missing own JID (creds.Me) for group send")
	}
	rec, err := c.loadSenderKeyRecord(groupJID, me)
	if err != nil {
		return "", err
	}
	cipher := signal.NewGroupCipher(rec)

	var skdm *signal.SenderKeyDistributionMessage
	if rec.IsEmpty() {
		skdm, err = c.mintSenderKey(cipher)
		if err != nil {
			return "", err
		}
	}

	// (5) encrypt the content as a SenderKeyMessage.
	plaintext, err := encodeWAMessage(msg)
	if err != nil {
		return "", err
	}
	skMsg, err := cipher.EncryptGroup(plaintext)
	if err != nil {
		return "", fmt.Errorf("client: group encrypt: %w", err)
	}

	// (6) distribute the SKDM 1:1 to every device, when we have one to send.
	var participantNodes []wire.Node
	if skdm != nil {
		skdmMsg := buildSenderKeyDistributionMessage(groupJID, skdm)
		skdmPlain, err := encodeWAMessage(skdmMsg)
		if err != nil {
			return "", err
		}
		participantNodes, err = c.encryptForDevices(sess.creds, devices, skdmPlain, "")
		if err != nil {
			return "", err
		}
		if len(participantNodes) == 0 {
			return "", errors.New("client: no recipients could be encrypted for (group SKDM)")
		}
	}

	// (7) assemble and send the group stanza.
	stanzaType := opts.stanzaType
	if stanzaType == "" {
		stanzaType = "text"
	}
	stanza := buildGroupMessageStanza(msgID, groupJID, stanzaType, opts.mediaType, participantNodes, skMsg, sess.creds.Account)
	if err := sess.send(stanza); err != nil {
		return "", fmt.Errorf("client: send group message: %w", err)
	}

	// (8) persist our advanced sender key chain.
	if err := c.storeSenderKeyRecord(groupJID, me, cipher.Record()); err != nil {
		return "", err
	}
	return msgID, nil
}

// mintSenderKey generates a fresh sender key (random keyID, 32-byte chain seed,
// fresh signing key pair) in the record via CreateSenderKeyDistribution and
// returns the SKDM to distribute. Mirrors GroupSessionBuilder.create.
func (c *Client) mintSenderKey(cipher *signal.GroupCipher) (*signal.SenderKeyDistributionMessage, error) {
	var idBuf [4]byte
	if _, err := rand.Read(idBuf[:]); err != nil {
		return nil, fmt.Errorf("client: sender key id random: %w", err)
	}
	// libsignal sender key ids are 31-bit (KeyHelper.generateSenderKeyId).
	keyID := binary.BigEndian.Uint32(idBuf[:]) & 0x7fffffff

	var chainSeed [32]byte
	if _, err := rand.Read(chainSeed[:]); err != nil {
		return nil, fmt.Errorf("client: sender chain seed random: %w", err)
	}
	signing, err := keys.GenKeyPair()
	if err != nil {
		return nil, err
	}
	return cipher.CreateSenderKeyDistribution(keyID, chainSeed, signing), nil
}

// buildSenderKeyDistributionMessage wraps a signal SKDM into a WAProto.Message
// carrying the axolotl-serialized SKDM bytes plus the group id, the form sent
// 1:1 to each device so they can install the sender key. Pure / testable.
func buildSenderKeyDistributionMessage(groupJID string, skdm *signal.SenderKeyDistributionMessage) *waproto.Message {
	axolotl := signal.SerializeSenderKeyDistributionMessage(skdm.KeyID, skdm.Iteration, skdm.ChainKey, skdm.SigningPub)
	return &waproto.Message{
		SenderKeyDistributionMessage: &waproto.SenderKeyDistributionMessage{
			GroupId:                             proto.String(groupJID),
			AxolotlSenderKeyDistributionMessage: axolotl,
		},
	}
}

// buildGroupMessageStanza assembles the group <message> stanza:
//
//	<message id to=group type=...>
//	  <participants>            (present only when distributing a new SKDM)
//	    <to jid=device0><enc v=2 type=pkmsg>(SKDM)</enc></to>
//	    ...
//	  </participants>
//	  <enc v=2 type=skmsg>(content)</enc>
//	  <device-identity>...      (when any SKDM participant carries a pkmsg)
//	</message>
//
// This matches Baileys' group relay: the per-device <participants> carry the
// SenderKeyDistributionMessage, and the single skmsg <enc> carries the content.
func buildGroupMessageStanza(msgID, groupJID, stanzaType, mediaType string, participants []wire.Node, skMsg, account []byte) wire.Node {
	var content []wire.Node
	if len(participants) > 0 {
		content = append(content, wire.Node{
			Tag:     "participants",
			Attrs:   map[string]string{},
			Content: participants,
		})
	}
	skEncAttrs := map[string]string{"v": "2", "type": "skmsg"}
	if mediaType != "" {
		// Media in a group rides the same sender-key <enc type=skmsg>; the server
		// still needs the mediatype attr to route/keep it (mirrors the 1:1 enc).
		skEncAttrs["mediatype"] = mediaType
	}
	content = append(content, wire.Node{
		Tag:     "enc",
		Attrs:   skEncAttrs,
		Content: append([]byte(nil), skMsg...),
	})
	if len(account) > 0 && participantsHavePkmsg(participants) {
		content = append(content, wire.Node{
			Tag:     "device-identity",
			Attrs:   map[string]string{},
			Content: account,
		})
	}
	return wire.Node{
		Tag: "message",
		Attrs: map[string]string{
			"id":   msgID,
			"to":   groupJID,
			"type": stanzaType,
		},
		Content: content,
	}
}
