// Package client: call.go implements WhatsApp voice/video call handling — the
// inbound <call> stanza parser and the outbound call rejection. These mirror
// Baileys' Socket/messages-recv.js call handling (the "CB:call" handler and its
// rejectCall function).
//
// Inbound <call> stanza (Baileys handleCall):
//
//	<call from=<peer> t=<unix-secs> [offline=...]>
//	  <offer call-id=... call-creator=... [caller_pn=...] [type=group]>
//	    [<video/>]                # present => video call
//	    ...
//	  </offer>
//	</call>
//
// The single info child's tag selects the call lifecycle stage:
//   - <offer>     : an incoming call is ringing
//   - <terminate> : the call ended (hung up / timed out)
//   - <reject>    : the call was rejected
//   - <relaylatency>/<preaccept>/... : intermediate signalling we surface as
//     the raw stage string without further interpretation.
//
// The info child carries call-id and, in place of the top-level from, the
// originating party as either its own from attr or call-creator (Baileys:
// `infoChild.attrs.from || infoChild.attrs['call-creator']`). A nested <video>
// child marks a video offer; type=group marks a group call.
//
// Outbound rejection (Baileys rejectCall), confirmed against messages-recv.js:
//
//	<call from=<me> to=<callFrom>>
//	  <reject call-id=<callID> call-creator=<callFrom> count=0/>
//	</call>
//
// Note: this build's read loop (pairing.go's loginLoop) does not yet dispatch
// <call> stanzas. parseCallNode + CallEvent are provided as the pure building
// blocks; wiring the loop to emit CallEvent (and to auto-reject, if desired) is
// a future integration that does not require editing this file. Until then a
// caller can register an OnIncomingNode hook, match node.Tag == "call", run
// parseCallNode, and act (e.g. call RejectCall).
package client

import (
	"context"
	"errors"
	"strconv"

	"github.com/jfelipesjc/wa-go/internal/wire"
)

// CallStage enumerates the lifecycle stage of an inbound <call> stanza, taken
// from the tag of its single info child (offer/terminate/reject/...).
type CallStage string

const (
	// CallStageOffer is an incoming, ringing call.
	CallStageOffer CallStage = "offer"
	// CallStageTerminate is a call that ended (hang up / timeout).
	CallStageTerminate CallStage = "terminate"
	// CallStageReject is a call that was rejected.
	CallStageReject CallStage = "reject"
)

// CallInfo is the parsed form of an inbound <call> stanza. It carries the
// minimum a caller needs to react (e.g. to RejectCall an offer).
type CallInfo struct {
	// ID is the call-id attribute from the info child.
	ID string
	// From is the originating party: the info child's from attr, falling back to
	// call-creator (Baileys precedence), else the top-level <call> from attr.
	From string
	// Stage is the info child's tag (offer/terminate/reject/...).
	Stage CallStage
	// Offer is true when Stage == offer (a convenience over Stage).
	Offer bool
	// Video is true when an offer carries a nested <video> child.
	Video bool
	// Group is true when the info child has type=group (a group call).
	Group bool
	// Timestamp is the top-level <call> t attribute (unix seconds), 0 if absent
	// or unparseable.
	Timestamp int64
	// CallerPN is the optional caller_pn attribute (the caller's phone-number
	// JID), when the server provides it.
	CallerPN string
}

// CallEvent is the Client event a future read-loop integration would emit for
// each inbound <call> stanza. It is part of the Event sum type so it can flow on
// the same channel as the other events once the loop dispatches it.
type CallEvent struct {
	Info CallInfo
}

func (CallEvent) isEvent() {}

// parseCallNode parses an inbound <call> stanza into a CallInfo. It is a pure
// function (no Client state) so it can be unit-tested with synthetic nodes and
// reused by whatever code dispatches <call> from the read loop. It errors when
// the node is not a <call> or has no info child.
func parseCallNode(node wire.Node) (*CallInfo, error) {
	if node.Tag != "call" {
		return nil, errors.New("client: parseCallNode: not a <call> node")
	}
	kids := children(node)
	if len(kids) == 0 {
		return nil, errors.New("client: parseCallNode: <call> has no info child")
	}
	info := kids[0]

	out := &CallInfo{
		ID:    info.Attrs["call-id"],
		Stage: CallStage(info.Tag),
	}
	// Originating party: infoChild.from || infoChild.call-creator || call.from.
	out.From = info.Attrs["from"]
	if out.From == "" {
		out.From = info.Attrs["call-creator"]
	}
	if out.From == "" {
		out.From = node.Attrs["from"]
	}
	out.CallerPN = info.Attrs["caller_pn"]
	out.Group = info.Attrs["type"] == "group"
	out.Offer = out.Stage == CallStageOffer
	if _, ok := childByTag(info, "video"); ok {
		out.Video = true
	}
	if t := node.Attrs["t"]; t != "" {
		if v, err := strconv.ParseInt(t, 10, 64); err == nil {
			out.Timestamp = v
		}
	}
	return out, nil
}

// rejectCallNode builds the call-rejection stanza, byte-for-byte as Baileys'
// rejectCall (messages-recv.js):
//
//	<call from=<me> to=<callFrom>>
//	  <reject call-id=<callID> call-creator=<callFrom> count=0/>
//	</call>
//
// me is the local account JID (creds.Me); callFrom is the call originator
// (CallInfo.From). count is always "0" (Baileys hardcodes it).
func rejectCallNode(me, callID, callFrom string) wire.Node {
	return wire.Node{
		Tag: "call",
		Attrs: map[string]string{
			"from": me,
			"to":   callFrom,
		},
		Content: []wire.Node{
			{
				Tag: "reject",
				Attrs: map[string]string{
					"call-id":      callID,
					"call-creator": callFrom,
					"count":        "0",
				},
			},
		},
	}
}

// callAckNode builds the <ack> for an inbound <call> stanza. WhatsApp expects an
// ack for each call stanza (class="call"), echoing its id and the info child's
// tag/call-id, addressed back to the originator. Mirrors Baileys' handleCall ack
// (sendMessageAck with the call's offer/terminate child). Without it the server
// re-sends the offer and may treat the device as unresponsive.
//
// Shape: <ack from=<me> to=<call.from> id=<call.id> class=call [type=<stage>]>.
func callAckNode(node wire.Node, me string) wire.Node {
	attrs := map[string]string{
		"id":    node.Attrs["id"],
		"to":    node.Attrs["from"],
		"class": "call",
	}
	if me != "" {
		attrs["from"] = me
	}
	// The info child's tag (offer/terminate/reject) is carried as the ack type so
	// the server can correlate the ack with the call lifecycle stage.
	if kids := children(node); len(kids) > 0 {
		attrs["type"] = kids[0].Tag
	}
	return wire.Node{Tag: "ack", Attrs: attrs}
}

// RejectCall rejects an incoming call identified by callID originating from
// callFrom (both taken from a parsed CallInfo). It sends the <call><reject>
// stanza on the live session. Unlike Baileys it does not await an iq result:
// the reject stanza is fire-and-forget (it has no id and the server does not
// reply with a matching <iq>), so RejectCall returns once the stanza is written.
func (c *Client) RejectCall(ctx context.Context, callID, callFrom string) error {
	if callID == "" || callFrom == "" {
		return errors.New("client: RejectCall requires callID and callFrom")
	}
	sess, ok := c.activeSession()
	if !ok {
		return errors.New("client: RejectCall requires a live session")
	}
	me := ""
	if sess.creds != nil {
		me = sess.creds.Me
	}
	return sess.send(rejectCallNode(me, callID, callFrom))
}
