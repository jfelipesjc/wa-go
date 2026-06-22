package client

import (
	"context"
	"errors"

	"github.com/felipeleal/wa-go/internal/wire"
)

// Labels (WhatsApp Business chat/message labels).
//
// In WhatsApp, labels are an app-state feature. Baileys' chatModificationToAppPatch
// encodes label operations as patches in the "regular" collection at apiVersion 3:
//
//   - create/rename/delete a label  -> SyncActionValue.labelEditAction,
//     index ["label_edit", labelID]
//   - attach/detach a label to a chat -> SyncActionValue.labelAssociationAction
//     {labeled: bool}, index [LabelAssociationType.Chat, labelID, chatJID]
//   - attach/detach a label to a message -> labelAssociationAction{labeled},
//     index [LabelAssociationType.Message, labelID, chatJID, messageID, "0", "0"]
//
// where LabelAssociationType.Chat == "label_jid" and
// LabelAssociationType.Message == "label_message".
//
// CAVEAT — waproto gap: the generated waproto.SyncActionValue in this build does
// NOT yet expose LabelEditAction or LabelAssociationAction (only star/contact/
// mute/pin/archive/markRead/clear/delete actions are present). Regenerating
// waproto is owned by another flow, so we cannot construct the SyncActionValue
// the app-state encoder (appstate.EncodePatch) requires for these mutations.
//
// To keep this layer useful and fully testable today, we:
//   - expose the exact index arrays Baileys uses, as pure functions
//     (labelEditIndex / chatLabelIndex / messageLabelIndex), so the only missing
//     piece once the proto lands is wiring the SyncActionValue;
//   - expose AddChatLabel/RemoveChatLabel/AddMessageLabel as methods that build
//     the patch via the shared chatmod path and return errLabelProtoMissing until
//     the value type exists, rather than silently no-op;
//   - implement GetLabels via the read-only w:biz iq, which needs no app-state.

// LabelAssociationType prefixes, copied verbatim from Baileys' Types/Label.
const (
	labelAssocChat    = "label_jid"
	labelAssocMessage = "label_message"
	labelEditPrefix   = "label_edit"
)

// collLabel and labelAPIVersion are the app-state collection and SyncActionData
// version Baileys uses for every label patch.
const (
	collLabel       = collRegular
	labelAPIVersion = 3
)

// errLabelProtoMissing reports that label app-state mutations cannot be encoded
// because the generated waproto.SyncActionValue lacks the label action fields.
var errLabelProtoMissing = errors.New("client: label app-state actions require waproto.SyncActionValue.{LabelEditAction,LabelAssociationAction}, which are not present in this waproto build; regenerate waproto to enable AddChatLabel/RemoveChatLabel/AddMessageLabel")

// --- pure index builders (Baileys-compatible) ---

// labelEditIndex is the app-state index for creating/editing/deleting a label.
func labelEditIndex(labelID string) []string {
	return []string{labelEditPrefix, labelID}
}

// chatLabelIndex is the app-state index for (de)associating a label with a chat.
func chatLabelIndex(labelID, chatJID string) []string {
	return []string{labelAssocChat, labelID, chatJID}
}

// messageLabelIndex is the app-state index for (de)associating a label with a
// single message. The trailing "0","0" mirror Baileys (fromMe/unused padding).
func messageLabelIndex(labelID, chatJID, messageID string) []string {
	return []string{labelAssocMessage, labelID, chatJID, messageID, "0", "0"}
}

// --- read path: GetLabels via iq (no app-state required) ---

// Label is one chat/message label as returned by GetLabels.
type Label struct {
	ID    string
	Name  string
	Color string // numeric color index as a string, "" if unset
}

// GetLabels fetches the account's defined labels via the w:biz labels iq:
//
//	<iq to=@s.whatsapp.net type=get xmlns=w:biz id=...><labels/></iq>
//
// and parses the <labels><label id= name= color=/></labels> reply. The account
// must be a WhatsApp Business account for labels to exist; a non-business account
// returns an empty slice.
func (c *Client) GetLabels(ctx context.Context) ([]Label, error) {
	sess, ok := c.activeSession()
	if !ok {
		return nil, errors.New("client: not logged in (no active session)")
	}
	req := buildGetLabelsIQ(c.nextIQID("wa-go-labels-"))
	reply, err := c.sendIQ(ctx, sess, req)
	if err != nil {
		return nil, err
	}
	return parseLabels(reply), nil
}

// buildGetLabelsIQ is the pure constructor for the labels query iq.
func buildGetLabelsIQ(id string) wire.Node {
	return wire.Node{
		Tag: "iq",
		Attrs: map[string]string{
			"to":    sWhatsAppNet,
			"type":  "get",
			"xmlns": "w:biz",
			"id":    id,
		},
		Content: []wire.Node{{Tag: "labels"}},
	}
}

// parseLabels extracts Label entries from a labels iq reply. It tolerates either
// <iq><labels><label/></labels></iq> or a top-level <labels> node.
func parseLabels(reply wire.Node) []Label {
	labels := findChild(reply, "labels")
	if labels == nil {
		// Some replies put <label> children directly under <iq>.
		labels = &reply
	}
	children, _ := labels.Content.([]wire.Node)
	out := make([]Label, 0, len(children))
	for _, ch := range children {
		if ch.Tag != "label" {
			continue
		}
		out = append(out, Label{
			ID:    ch.Attrs["id"],
			Name:  ch.Attrs["name"],
			Color: ch.Attrs["color"],
		})
	}
	return out
}

// findChild returns the first direct child of n with the given tag, or nil.
func findChild(n wire.Node, tag string) *wire.Node {
	children, ok := n.Content.([]wire.Node)
	if !ok {
		return nil
	}
	for i := range children {
		if children[i].Tag == tag {
			return &children[i]
		}
	}
	return nil
}

// --- write path: label associations via app-state ---
//
// These build the correct index but cannot complete encoding until waproto
// carries the label SyncActionValue fields (see errLabelProtoMissing). They are
// kept as public methods so the API surface is stable: once the proto lands the
// only change is constructing the SyncActionValue inside ChatAction.

// AddChatLabel attaches the given label to a chat.
func (c *Client) AddChatLabel(ctx context.Context, chatJID, labelID string) error {
	return c.labelAssociation(ctx, chatJID, chatLabelIndex(labelID, chatJID), true)
}

// RemoveChatLabel detaches the given label from a chat.
func (c *Client) RemoveChatLabel(ctx context.Context, chatJID, labelID string) error {
	return c.labelAssociation(ctx, chatJID, chatLabelIndex(labelID, chatJID), false)
}

// AddMessageLabel attaches the given label to a single message in a chat.
func (c *Client) AddMessageLabel(ctx context.Context, chatJID, labelID, messageID string) error {
	return c.labelAssociation(ctx, chatJID, messageLabelIndex(labelID, chatJID, messageID), true)
}

// RemoveMessageLabel detaches the given label from a single message in a chat.
func (c *Client) RemoveMessageLabel(ctx context.Context, chatJID, labelID, messageID string) error {
	return c.labelAssociation(ctx, chatJID, messageLabelIndex(labelID, chatJID, messageID), false)
}

// labelAssociation is the shared write core. It validates the session and the
// index, then reports errLabelProtoMissing because the SyncActionValue the
// app-state encoder needs is not available in this waproto build. The index is
// fully resolved (and validated by tests) so the wiring is a one-liner once the
// proto is regenerated:
//
//	value := &waproto.SyncActionValue{
//	    LabelAssociationAction: &waproto.SyncActionValue_LabelAssociationAction{Labeled: proto.Bool(labeled)},
//	}
//	return c.ChatModify(ctx, chatJID, ChatAction{
//	    collection: collLabel, apiVersion: labelAPIVersion, index: index, value: value,
//	})
func (c *Client) labelAssociation(ctx context.Context, chatJID string, index []string, labeled bool) error {
	if _, ok := c.activeSession(); !ok {
		return errors.New("client: not logged in (no active session)")
	}
	if len(index) == 0 || chatJID == "" {
		return errors.New("client: label association requires a chat jid and label id")
	}
	_ = labeled
	_ = collLabel
	_ = labelAPIVersion
	return errLabelProtoMissing
}
