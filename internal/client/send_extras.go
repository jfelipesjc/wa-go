package client

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/felipeleal/wa-go/internal/waproto"
	"google.golang.org/protobuf/proto"
)

// SendPoll sends a poll (PollCreationMessage). name is the question, options the
// choices, selectableCount how many a voter may pick (1 = single choice, 0 also
// treated as 1). The random 32-byte messageSecret carried in MessageContextInfo
// is what voters' EncryptPollVote/DecryptPollVote key off. Works 1:1 and in
// groups (routed by destination JID).
func (c *Client) SendPoll(ctx context.Context, toJID, name string, options []string, selectableCount int) (string, error) {
	if name == "" {
		return "", errors.New("client: SendPoll requires a name")
	}
	if len(options) < 2 {
		return "", errors.New("client: SendPoll requires at least 2 options")
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", fmt.Errorf("client: poll secret: %w", err)
	}
	opts := make([]*waproto.PollCreationMessage_Option, len(options))
	for i, o := range options {
		opts[i] = &waproto.PollCreationMessage_Option{OptionName: proto.String(o)}
	}
	if selectableCount < 1 {
		selectableCount = 1
	}
	msg := &waproto.Message{
		PollCreationMessage: &waproto.PollCreationMessage{
			Name:                   proto.String(name),
			Options:                opts,
			SelectableOptionsCount: proto.Uint32(uint32(selectableCount)),
		},
		MessageContextInfo: &waproto.MessageContextInfo{
			MessageSecret: secret,
		},
	}
	return c.sendRouted(ctx, toJID, msg, sendOpts{pacerHint: len(name)})
}

// PinMessage pins (pin=true) or unpins (pin=false) a specific message in a chat
// for everyone (PinInChatMessage). targetKey identifies the message to pin (its
// remoteJid/id/fromMe, plus participant in groups). Distinct from PinChat, which
// pins the whole chat in the list.
func (c *Client) PinMessage(ctx context.Context, toJID string, targetKey *waproto.MessageKey, pin bool) (string, error) {
	if targetKey == nil {
		return "", errors.New("client: PinMessage requires a target message key")
	}
	t := waproto.PinInChatType_PIN_IN_CHAT_FOR_ALL
	if !pin {
		t = waproto.PinInChatType_PIN_IN_CHAT_UNPIN_FOR_ALL
	}
	msg := &waproto.Message{
		PinInChatMessage: &waproto.PinInChatMessage{
			Key:               targetKey,
			Type:              t.Enum(),
			SenderTimestampMs: proto.Int64(time.Now().UnixMilli()),
		},
	}
	return c.sendRouted(ctx, toJID, msg, sendOpts{})
}
