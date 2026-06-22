// Package client: call_offer.go implements the OUTBOUND side of WhatsApp calls —
// the <call><offer> stanza and its matching <call><terminate>. This complements
// call.go, which handles inbound <call> stanzas and outbound rejection.
//
// IMPORTANT — SCOPE AND LIMITATIONS (this is a niche, deliberately partial feature):
//
// Placing a real WhatsApp call is a full WebRTC negotiation: the <offer> stanza
// must carry the caller's media-session encryption material (the SRTP/E2E call
// keys, ICE/transport candidates and codec parameters), and the device must then
// run a WebRTC/SRTP media stack to actually carry audio/video. Baileys itself
// does NOT implement outbound calling — its Socket only PARSES inbound <call>
// stanzas (offer/terminate/reject/relaylatency in messages-recv.js) and can
// rejectCall; there is no offerCall in Baileys. There is therefore no reference
// implementation, and no way to validate call crypto offline.
//
// Accordingly this file implements ONLY the SIGNALLING STANZA STRUCTURE of an
// offer (and its terminate), faithfully modelled on the inbound <offer> shape
// that call.go parses (call-id, call-creator, a <video/> marker for video). It
// does NOT, and intentionally must not, fabricate call encryption: the <offer>
// built here is a skeleton with the <audio>/<video> media descriptors marked but
// WITHOUT real media-session keys or transport. As such it will not establish a
// working call against the live WhatsApp servers — the server expects the E2E
// call keys this build cannot produce. We do not invent call crypto we cannot
// verify.
//
// What IS usable today: a structurally-correct, unit-tested representation of the
// offer/terminate stanzas, ready for a future integration that wires in a real
// call media/E2E layer. TerminateCall is fully usable as a fire-and-forget hangup
// for a known call-id (it carries no media), mirroring rejectCall's shape.
//
// Offer stanza (skeleton — modelled on call.go's inbound <call><offer>):
//
//	<call to=<peer> id=<stanzaID>>
//	  <offer call-id=<callID> call-creator=<me>>
//	    <audio enc=skmsg rate=.../>        # media descriptor (NO real keys)
//	    [<video enc=skmsg rate=.../>]      # only when video=true
//	    <encopt keygen=2/>                 # placeholder media-encryption opts
//	  </offer>
//	</call>
//
// Terminate stanza (modelled on rejectCall in call.go / messages-recv.js):
//
//	<call from=<me> to=<peer>>
//	  <terminate call-id=<callID> count=0/>
//	</call>
package client

import (
	"context"
	"errors"

	"github.com/felipeleal/wa-go/internal/wire"
)

// buildCallOffer builds the (skeleton) outbound call-offer stanza. me is the
// caller's JID (call-creator); to is the callee's JID; callID is the freshly
// generated call identifier; video selects whether a <video> media descriptor is
// included. See the package doc: the media descriptors carry NO real call
// encryption keys, so this stanza is structural only.
func buildCallOffer(id, me, to, callID string, video bool) wire.Node {
	offerContent := []wire.Node{
		// Audio media descriptor. enc/rate mirror the kind of attrs WhatsApp's
		// real offers carry; they are placeholders here (no negotiated keys).
		{Tag: "audio", Attrs: map[string]string{"enc": "skmsg", "rate": "8000"}},
	}
	if video {
		offerContent = append(offerContent,
			wire.Node{Tag: "video", Attrs: map[string]string{"enc": "skmsg", "rate": "8000"}})
	}
	// encopt: placeholder for the media-encryption options block. A real offer
	// would carry the call's E2E key material here; we do not fabricate it.
	offerContent = append(offerContent,
		wire.Node{Tag: "encopt", Attrs: map[string]string{"keygen": "2"}})

	return wire.Node{
		Tag: "call",
		Attrs: map[string]string{
			"id": id,
			"to": to,
		},
		Content: []wire.Node{
			{
				Tag: "offer",
				Attrs: map[string]string{
					"call-id":      callID,
					"call-creator": me,
				},
				Content: offerContent,
			},
		},
	}
}

// buildCallTerminate builds the call-terminate (hangup) stanza, modelled on
// call.go's rejectCallNode / Baileys rejectCall:
//
//	<call from=<me> to=<peer>>
//	  <terminate call-id=<callID> count=0/>
//	</call>
func buildCallTerminate(me, callID, to string) wire.Node {
	return wire.Node{
		Tag: "call",
		Attrs: map[string]string{
			"from": me,
			"to":   to,
		},
		Content: []wire.Node{
			{
				Tag: "terminate",
				Attrs: map[string]string{
					"call-id": callID,
					"count":   "0",
				},
			},
		},
	}
}

// OfferCall sends a call-offer stanza to toJID and returns the generated callID.
//
// NICHE / INCOMPLETE: as documented at the top of this file, this sends only the
// SIGNALLING SKELETON of an offer — it does NOT carry the WebRTC/SRTP media-session
// E2E keys a real call requires, so it will not establish a working call against
// the live WhatsApp servers. It exists so callers can drive the stanza layer (and
// terminate a call they track) while the media/E2E layer remains future work.
// video selects an audio-only (false) or audio+video (true) offer skeleton.
func (c *Client) OfferCall(ctx context.Context, toJID string, video bool) (callID string, err error) {
	if toJID == "" {
		return "", errors.New("client: OfferCall requires a destination JID")
	}
	sess, ok := c.activeSession()
	if !ok {
		return "", errors.New("client: OfferCall requires a live session")
	}
	me := ""
	if sess.creds != nil {
		me = sess.creds.Me
	}
	callID = generateMessageID()
	stanza := buildCallOffer(c.nextIQID("wa-go-call-"), me, toJID, callID, video)
	if err := sess.send(stanza); err != nil {
		return "", err
	}
	return callID, nil
}

// TerminateCall sends a terminate (hangup) for callID to toJID. Like RejectCall
// it is fire-and-forget: the terminate stanza carries no media and the server
// does not reply with a matching <iq>, so it returns once the stanza is written.
func (c *Client) TerminateCall(ctx context.Context, callID, toJID string) error {
	if callID == "" || toJID == "" {
		return errors.New("client: TerminateCall requires callID and toJID")
	}
	sess, ok := c.activeSession()
	if !ok {
		return errors.New("client: TerminateCall requires a live session")
	}
	me := ""
	if sess.creds != nil {
		me = sess.creds.Me
	}
	return sess.send(buildCallTerminate(me, callID, toJID))
}
