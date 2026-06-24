package client

import (
	"context"
	"errors"
	"time"

	"github.com/felipeleal/wa-go/internal/waproto"
	"github.com/felipeleal/wa-go/internal/wire"
	"google.golang.org/protobuf/proto"
)

// Disappearing (ephemeral) messages.
//
// WhatsApp toggles per-chat disappearing-message timers in two different ways
// depending on whether the chat is a 1:1 conversation or a group, mirroring
// Baileys:
//
//   - 1:1 chat: send a normal MESSAGE stanza carrying a ProtocolMessage of type
//     EPHEMERAL_SETTING with ephemeralExpiration set to the timer in seconds (0
//     disables). The peer's devices pick up the new setting from the message.
//     (Baileys' toggleDisappearingMessages for non-group jids.)
//
//   - Group: send an <iq xmlns=w:g2 type=set to=<group>> with a single child
//     node: <ephemeral expiration=<seconds>/> to enable, or <not_ephemeral/> to
//     disable. (Baileys' groupToggleEphemeral.)
//
// Common WhatsApp presets are 24h (86400), 7d (604800) and 90d (7776000); any
// value is accepted on the wire, and 0 turns the feature off.

// Well-known disappearing-message durations exposed for convenience. Callers may
// pass any duration; these mirror the WhatsApp UI presets.
const (
	DisappearingOff = 0
	Disappearing24h = 24 * time.Hour
	Disappearing7d  = 7 * 24 * time.Hour
	Disappearing90d = 90 * 24 * time.Hour
)

// SetDisappearingMessages sets (or, with duration 0, disables) the
// disappearing-message timer for a chat. It dispatches on the JID: group JIDs
// (@g.us) go through the w:g2 iq path; everything else is treated as a 1:1 chat
// and gets an EPHEMERAL_SETTING ProtocolMessage. duration is rounded down to
// whole seconds (the wire unit); a negative duration is rejected.
//
// For 1:1 it returns the generated message id; for groups it returns "" on
// success (the iq carries no message id).
func (c *Client) SetDisappearingMessages(ctx context.Context, jid string, duration time.Duration) (string, error) {
	if duration < 0 {
		return "", errors.New("client: disappearing duration must be >= 0")
	}
	secs := uint32(duration / time.Second)

	if isGroupJID(jid) {
		return "", c.setGroupEphemeral(ctx, jid, secs)
	}
	return c.sendRouted(ctx, jid, buildEphemeralSettingMessage(secs, time.Now()), sendOpts{})
}

// buildEphemeralSettingMessage is the pure constructor for the 1:1 disappearing
// setting: a ProtocolMessage of type EPHEMERAL_SETTING carrying the expiration
// (seconds) and the setting timestamp (millis). expiration 0 disables.
func buildEphemeralSettingMessage(expirationSecs uint32, now time.Time) *waproto.Message {
	settingType := waproto.ProtocolMessage_EPHEMERAL_SETTING
	return &waproto.Message{
		ProtocolMessage: &waproto.ProtocolMessage{
			Type:                      &settingType,
			EphemeralExpiration:       proto.Uint32(expirationSecs),
			EphemeralSettingTimestamp: proto.Int64(now.UnixMilli()),
		},
	}
}

// setGroupEphemeral sends the w:g2 toggle iq for a group and waits for the
// result.
func (c *Client) setGroupEphemeral(ctx context.Context, groupJID string, expirationSecs uint32) error {
	sess, ok := c.activeSession()
	if !ok {
		return errors.New("client: not logged in (no active session)")
	}
	req := buildGroupEphemeralIQ(c.nextIQID("wa-go-group-"), groupJID, expirationSecs)
	_, err := c.sendIQ(ctx, sess, req)
	return err
}

// buildGroupEphemeralIQ is the pure constructor for the group disappearing
// toggle, matching Baileys' groupToggleEphemeral:
//
//	<iq xmlns=w:g2 type=set to=group><ephemeral expiration=<secs>/></iq>   (enable)
//	<iq xmlns=w:g2 type=set to=group><not_ephemeral/></iq>                 (disable)
func buildGroupEphemeralIQ(id, groupJID string, expirationSecs uint32) wire.Node {
	var child wire.Node
	if expirationSecs > 0 {
		child = wire.Node{
			Tag:   "ephemeral",
			Attrs: map[string]string{"expiration": uintToStr(uint64(expirationSecs))},
		}
	} else {
		child = wire.Node{Tag: "not_ephemeral"}
	}
	return buildGroupQuery(id, groupJID, "set", []wire.Node{child})
}
