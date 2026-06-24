package client

import (
	"context"
	"time"

	"github.com/felipeleal/wa-go/internal/waproto"
	"google.golang.org/protobuf/proto"
)

// EditText edits a previously sent text message. It sends a ProtocolMessage of
// type MESSAGE_EDIT whose key points at the original message and whose
// editedMessage carries the replacement content, mirroring Baileys' edit relay.
func (c *Client) EditText(ctx context.Context, toJID string, targetKey *waproto.MessageKey, newText string) (string, error) {
	return c.sendRouted(ctx, toJID, buildEditMessage(targetKey, newText, time.Now()), sendOpts{pacerHint: len(newText)})
}

// DeleteMessage revokes (deletes for everyone) a previously sent message. It
// sends a ProtocolMessage of type REVOKE keyed to the target message, matching
// Baileys' delete relay.
func (c *Client) DeleteMessage(ctx context.Context, toJID string, targetKey *waproto.MessageKey) (string, error) {
	return c.sendRouted(ctx, toJID, buildRevokeMessage(targetKey), sendOpts{})
}

// buildEditMessage is the pure constructor for a MESSAGE_EDIT ProtocolMessage.
// The new content is wrapped as editedMessage{conversation: newText}.
func buildEditMessage(targetKey *waproto.MessageKey, newText string, now time.Time) *waproto.Message {
	editType := waproto.ProtocolMessage_MESSAGE_EDIT
	return &waproto.Message{
		ProtocolMessage: &waproto.ProtocolMessage{
			Key:           targetKey,
			Type:          &editType,
			EditedMessage: buildTextMessage(newText),
			TimestampMs:   proto.Int64(now.UnixMilli()),
		},
	}
}

// buildRevokeMessage is the pure constructor for a REVOKE ProtocolMessage.
func buildRevokeMessage(targetKey *waproto.MessageKey) *waproto.Message {
	revokeType := waproto.ProtocolMessage_REVOKE
	return &waproto.Message{
		ProtocolMessage: &waproto.ProtocolMessage{
			Key:  targetKey,
			Type: &revokeType,
		},
	}
}
