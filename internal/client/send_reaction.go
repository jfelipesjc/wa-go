package client

import (
	"context"
	"time"

	"github.com/felipeleal/wa-go/internal/waproto"
	"google.golang.org/protobuf/proto"
)

// SendReaction sends an emoji reaction to a target message. An empty emoji
// removes a previous reaction (Baileys' convention). targetKey identifies the
// message being reacted to (its remoteJid/id/fromMe and, in groups, the
// participant). Mirrors Baileys' ReactionMessage relay.
func (c *Client) SendReaction(ctx context.Context, toJID string, targetKey *waproto.MessageKey, emoji string) (string, error) {
	return c.sendMessage(ctx, toJID, buildReactionMessage(targetKey, emoji, time.Now()), sendOpts{})
}

// buildReactionMessage is the pure constructor for a ReactionMessage. The
// senderTimestampMs is taken from now (Baileys sets it to the send time).
func buildReactionMessage(targetKey *waproto.MessageKey, emoji string, now time.Time) *waproto.Message {
	return &waproto.Message{
		ReactionMessage: &waproto.ReactionMessage{
			Key:               targetKey,
			Text:              proto.String(emoji),
			SenderTimestampMs: proto.Int64(now.UnixMilli()),
		},
	}
}
