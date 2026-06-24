package client

import (
	"context"

	"github.com/felipeleal/wa-go/internal/waproto"
	"google.golang.org/protobuf/proto"
)

// SendText sends a 1:1 text message to toJID and returns the generated message
// id once the stanza has been written to the wire. It mirrors Baileys'
// relayMessage for a plain conversation: WAProto.Message{conversation: text}.
func (c *Client) SendText(ctx context.Context, toJID, text string) (string, error) {
	if isGroupJID(toJID) {
		parts, err := c.groupParticipantJIDs(ctx, toJID)
		if err != nil {
			return "", err
		}
		return c.sendGroupMessage(ctx, toJID, parts, buildTextMessage(text), sendOpts{pacerHint: len(text)})
	}
	return c.sendMessage(ctx, toJID, buildTextMessage(text), sendOpts{pacerHint: len(text)})
}

// SendTextReply sends a text reply quoting an earlier message. It builds an
// ExtendedTextMessage whose ContextInfo carries the quoted message's stanza id,
// the quoting participant (the quoted message's author), and the quoted
// WAProto.Message body, matching Baileys' generateForwardMessageContent /
// quoted-message handling.
//
// quotedKey identifies the message being replied to (its remoteJid/id/
// participant); quotedMsg is the original message body that gets embedded as
// ContextInfo.quotedMessage so clients can render the reply preview.
func (c *Client) SendTextReply(ctx context.Context, toJID, text string, quotedKey *waproto.MessageKey, quotedMsg *waproto.Message) (string, error) {
	return c.sendMessage(ctx, toJID, buildTextReplyMessage(text, quotedKey, quotedMsg), sendOpts{pacerHint: len(text)})
}

// SendTextMention sends a text that @-mentions the given JIDs. The mentioned
// JIDs are listed in ContextInfo.mentionedJid (Baileys' mentions option); the
// text itself should contain the matching "@<number>" tokens.
func (c *Client) SendTextMention(ctx context.Context, toJID, text string, mentions []string) (string, error) {
	return c.sendMessage(ctx, toJID, buildTextMentionMessage(text, mentions), sendOpts{pacerHint: len(text)})
}

// buildTextMessage is the pure constructor for a plain conversation message.
func buildTextMessage(text string) *waproto.Message {
	return &waproto.Message{Conversation: proto.String(text)}
}

// buildTextReplyMessage is the pure constructor for a quoted reply. A reply must
// use ExtendedTextMessage (conversation has no ContextInfo), so the text is
// carried there together with the quote context.
func buildTextReplyMessage(text string, quotedKey *waproto.MessageKey, quotedMsg *waproto.Message) *waproto.Message {
	ci := &waproto.ContextInfo{}
	if quotedKey != nil {
		if id := quotedKey.GetId(); id != "" {
			ci.StanzaId = proto.String(id)
		}
		// The quoting participant is the author of the quoted message.
		if p := quotedKey.GetParticipant(); p != "" {
			ci.Participant = proto.String(p)
		} else if rj := quotedKey.GetRemoteJid(); rj != "" {
			ci.Participant = proto.String(rj)
		}
		if rj := quotedKey.GetRemoteJid(); rj != "" {
			ci.RemoteJid = proto.String(rj)
		}
	}
	if quotedMsg != nil {
		ci.QuotedMessage = quotedMsg
	}
	return &waproto.Message{
		ExtendedTextMessage: &waproto.ExtendedTextMessage{
			Text:        proto.String(text),
			ContextInfo: ci,
		},
	}
}

// buildTextMentionMessage is the pure constructor for an @-mention message. Like
// replies, mentions carry a ContextInfo, so ExtendedTextMessage is used.
func buildTextMentionMessage(text string, mentions []string) *waproto.Message {
	return &waproto.Message{
		ExtendedTextMessage: &waproto.ExtendedTextMessage{
			Text: proto.String(text),
			ContextInfo: &waproto.ContextInfo{
				MentionedJid: append([]string(nil), mentions...),
			},
		},
	}
}
