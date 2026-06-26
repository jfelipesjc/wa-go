package client

import (
	"context"

	"github.com/jfelipesjc/wa-go/internal/waproto"
	"google.golang.org/protobuf/proto"
)

// This file adds the location and contact (vCard) 1:1 senders. Each public
// method builds a WAProto.Message with a pure builder and delegates to the
// shared 1:1 send core (sendMessage in send.go); the builders are split out so
// they can be unit-tested offline without any network. Mirrors the structure of
// send_text.go / send_reaction.go.

// SendLocation sends a LocationMessage pinning a geographic point. lat/lng are
// the coordinates in decimal degrees; name and address are optional labels shown
// in the location card.
func (c *Client) SendLocation(ctx context.Context, toJID string, lat, lng float64, name, address string) (string, error) {
	return c.sendRouted(ctx, toJID, buildLocationMessage(lat, lng, name, address), sendOpts{})
}

// buildLocationMessage is the pure constructor for a LocationMessage. Optional
// name/address are only set when non-empty (oneof fields stay nil otherwise).
func buildLocationMessage(lat, lng float64, name, address string) *waproto.Message {
	lm := &waproto.LocationMessage{
		DegreesLatitude:  proto.Float64(lat),
		DegreesLongitude: proto.Float64(lng),
	}
	if name != "" {
		lm.Name = proto.String(name)
	}
	if address != "" {
		lm.Address = proto.String(address)
	}
	return &waproto.Message{LocationMessage: lm}
}

// SendContact sends a ContactMessage carrying a single vCard. displayName is the
// name shown for the contact; vcard is the raw vCard (VCF) text.
func (c *Client) SendContact(ctx context.Context, toJID, displayName, vcard string) (string, error) {
	return c.sendRouted(ctx, toJID, buildContactMessage(displayName, vcard), sendOpts{})
}

// buildContactMessage is the pure constructor for a ContactMessage.
func buildContactMessage(displayName, vcard string) *waproto.Message {
	cm := &waproto.ContactMessage{
		Vcard: proto.String(vcard),
	}
	if displayName != "" {
		cm.DisplayName = proto.String(displayName)
	}
	return &waproto.Message{ContactMessage: cm}
}
