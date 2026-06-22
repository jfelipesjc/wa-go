// Package client: newsletter_admin.go implements the admin/owner side of
// WhatsApp Channels (newsletters): editing metadata, ownership transfer,
// demoting admins, reaction-mode configuration, fetching channel messages,
// subscribing to live updates and counting admins.
//
// Two transports are used, mirroring Baileys (lib/Socket/newsletter.js):
//
//   - w:mex (GraphQL-style) for the operations that go through
//     executeWMexQuery: UPDATE_METADATA, ADMIN_COUNT, CHANGE_OWNER, DEMOTE.
//     Same envelope as newsletter.go (buildMexQuery / extractMexData).
//
//   - xmlns="newsletter" plain iq for newsletterFetchMessages and
//     subscribeNewsletterUpdates, which Baileys issues directly with query().
//
// Query IDs and data paths copied verbatim from Baileys (lib/Types/Mex.js):
//
//	UPDATE_METADATA = "24250201037901610"  path xwa2_newsletter_update
//	ADMIN_COUNT     = "7130823597031706"   path xwa2_newsletter_admin
//	CHANGE_OWNER    = "7341777602580933"   path xwa2_newsletter_change_owner
//	DEMOTE          = "6551828931592903"   path xwa2_newsletter_demote
//
// Variables per op (lib/Socket/newsletter.js):
//
//	UPDATE  {"newsletter_id":<jid>,"updates":{...,"settings":<null|reaction obj>}}
//	         where updates may carry name / description / picture(base64|"").
//	ADMIN_COUNT  {"newsletter_id":<jid>}                       -> {admin_count}
//	CHANGE_OWNER {"newsletter_id":<jid>,"user_id":<newOwner>}
//	DEMOTE       {"newsletter_id":<jid>,"user_id":<userJid>}
//
// Reaction mode: Baileys' newsletterUpdate sets settings:null on a normal
// metadata edit; the reaction mode is carried by overriding that settings
// object with {"reaction_codes":{"value":<MODE>}} where MODE is one of
// ALL | BASIC | NONE | BLOCKLIST (WhatsApp's NewsletterReactionMode enum).
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/felipeleal/wa-go/internal/wire"
)

// xmlnsNewsletter is the namespace for the plain-iq newsletter transport
// (fetch messages / live updates).
const xmlnsNewsletter = "newsletter"

// Admin newsletter query IDs, copied verbatim from Baileys (lib/Types/Mex.js).
const (
	nlQueryUpdate      = "24250201037901610"
	nlQueryAdminCount  = "7130823597031706"
	nlQueryChangeOwner = "7341777602580933"
	nlQueryDemote      = "6551828931592903"
)

// Admin newsletter w:mex data paths (lib/Types/Mex.js XWAPaths). Note the
// admin-count path is "xwa2_newsletter_admin" (NOT ..._admin_count).
const (
	nlPathUpdate      = "xwa2_newsletter_update"
	nlPathAdminCount  = "xwa2_newsletter_admin"
	nlPathChangeOwner = "xwa2_newsletter_change_owner"
	nlPathDemote      = "xwa2_newsletter_demote"
)

// NewsletterReactionMode is the channel-wide reaction policy. The string values
// match WhatsApp's NewsletterReactionMode enum carried in the settings object.
type NewsletterReactionMode string

const (
	// ReactionModeAll allows any emoji reaction.
	ReactionModeAll NewsletterReactionMode = "ALL"
	// ReactionModeBasic allows only the basic reaction set.
	ReactionModeBasic NewsletterReactionMode = "BASIC"
	// ReactionModeNone disables reactions.
	ReactionModeNone NewsletterReactionMode = "NONE"
	// ReactionModeBlocklist allows all except a blocklisted set.
	ReactionModeBlocklist NewsletterReactionMode = "BLOCKLIST"
)

// validReactionMode reports whether m is one of the accepted enum values
// (case-sensitive, as sent on the wire). The public method also accepts the
// lowercase aliases all/basic/none/blocklist.
func validReactionMode(m NewsletterReactionMode) bool {
	switch m {
	case ReactionModeAll, ReactionModeBasic, ReactionModeNone, ReactionModeBlocklist:
		return true
	}
	return false
}

// NewsletterUpdateInput carries the optional metadata fields an admin may edit.
// A nil pointer means "leave unchanged"; only set fields are sent. Picture is a
// base64-encoded JPEG (Baileys passes img.toString('base64')); the empty string
// removes the picture (matching newsletterRemovePicture).
type NewsletterUpdateInput struct {
	Name        *string
	Description *string
	Picture     *string
}

// IsEmpty reports whether no field is set.
func (u NewsletterUpdateInput) IsEmpty() bool {
	return u.Name == nil && u.Description == nil && u.Picture == nil
}

// NewsletterMessage is a single message parsed out of a newsletterFetchMessages
// reply. The channel transport returns <message server_id=.. t=..> nodes whose
// leaf is the serialized message; this build surfaces the routing metadata and
// the raw inner bytes for the caller to decode further.
type NewsletterMessage struct {
	ServerID  string
	Timestamp int64
	Type      string
	Content   []byte
}

// --- variable builders (pure; testable without a session) ---

// newsletterUpdateVariables builds the UPDATE_METADATA variables. settings is
// passed through as-is (nil -> JSON null, matching Baileys' settings:null);
// callers wanting a reaction-mode change pass a non-nil settings object.
func newsletterUpdateVariables(jid string, in NewsletterUpdateInput, settings any) map[string]any {
	updates := map[string]any{}
	if in.Name != nil {
		updates["name"] = *in.Name
	}
	if in.Description != nil {
		updates["description"] = *in.Description
	}
	if in.Picture != nil {
		updates["picture"] = *in.Picture
	}
	// Baileys always sets settings (null on a plain edit) inside updates.
	updates["settings"] = settings
	return map[string]any{
		"newsletter_id": jid,
		"updates":       updates,
	}
}

// reactionModeSettings builds the settings object that carries a reaction-mode
// change: {"reaction_codes":{"value":<MODE>}}.
func reactionModeSettings(mode NewsletterReactionMode) map[string]any {
	return map[string]any{
		"reaction_codes": map[string]any{
			"value": string(mode),
		},
	}
}

// newsletterUserVariables builds {"newsletter_id":jid,"user_id":user} shared by
// CHANGE_OWNER and DEMOTE.
func newsletterUserVariables(jid, user string) map[string]any {
	return map[string]any{
		"newsletter_id": jid,
		"user_id":       user,
	}
}

// --- xmlns=newsletter iq builders (pure) ---

// buildNewsletterFetchMessages builds the plain-iq request that fetches channel
// messages (Baileys' newsletterFetchMessages): a <message_updates> child with
// count and optional since. since<=0 omits the attribute.
func buildNewsletterFetchMessages(id, jid string, count int, since int64) wire.Node {
	attrs := map[string]string{"count": strconv.Itoa(count)}
	if since > 0 {
		attrs["since"] = strconv.FormatInt(since, 10)
	}
	return wire.Node{
		Tag: "iq",
		Attrs: map[string]string{
			"id":    id,
			"type":  "get",
			"xmlns": xmlnsNewsletter,
			"to":    jid,
		},
		Content: []wire.Node{
			{Tag: "message_updates", Attrs: attrs},
		},
	}
}

// buildSubscribeLiveUpdates builds the plain-iq request that subscribes to a
// channel's live updates (Baileys' subscribeNewsletterUpdates).
func buildSubscribeLiveUpdates(id, jid string) wire.Node {
	return wire.Node{
		Tag: "iq",
		Attrs: map[string]string{
			"id":    id,
			"type":  "set",
			"xmlns": xmlnsNewsletter,
			"to":    jid,
		},
		Content: []wire.Node{
			{Tag: "live_updates", Attrs: map[string]string{}},
		},
	}
}

// --- response parsing (pure) ---

// parseAdminCount extracts the admin_count integer from the ADMIN_COUNT data
// payload. Baileys reads response.admin_count; the count may arrive as a JSON
// number or a string.
func parseAdminCount(raw json.RawMessage) (int, error) {
	var obj struct {
		AdminCount json.RawMessage `json:"admin_count"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return 0, fmt.Errorf("client: decode admin_count payload: %w", err)
	}
	if len(obj.AdminCount) == 0 {
		return 0, errors.New("client: admin_count payload missing admin_count")
	}
	// number form
	var n int
	if err := json.Unmarshal(obj.AdminCount, &n); err == nil {
		return n, nil
	}
	// string form
	var s string
	if err := json.Unmarshal(obj.AdminCount, &s); err == nil {
		if v, err := strconv.Atoi(s); err == nil {
			return v, nil
		}
	}
	return 0, fmt.Errorf("client: admin_count not an integer: %s", obj.AdminCount)
}

// parseNewsletterMessages walks an xmlns=newsletter fetch reply and extracts the
// <message> nodes. The reply nests them under a <messages> (or
// <message_updates>) wrapper; this scans both the wrapper and the iq itself so
// it is robust to either layout.
func parseNewsletterMessages(reply wire.Node) []NewsletterMessage {
	var out []NewsletterMessage
	collect := func(parent wire.Node) {
		for _, m := range childrenByTag(parent, "message") {
			out = append(out, NewsletterMessage{
				ServerID:  m.Attrs["server_id"],
				Timestamp: atoiSafe(m.Attrs["t"]),
				Type:      m.Attrs["type"],
				Content:   nodeBytes(m),
			})
		}
	}
	// Direct children of the iq.
	collect(reply)
	// Common wrappers.
	for _, wrapTag := range []string{"messages", "message_updates"} {
		if w, ok := childByTag(reply, wrapTag); ok {
			collect(w)
		}
	}
	return out
}

// atoiSafe parses a base-10 int64, returning 0 on failure.
func atoiSafe(s string) int64 {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// --- public methods ---

// NewsletterUpdate edits a channel's metadata (name, description and/or
// picture). Unset fields are left unchanged. Returns the updated metadata.
func (c *Client) NewsletterUpdate(ctx context.Context, jid string, in NewsletterUpdateInput) (*NewsletterInfo, error) {
	if jid == "" {
		return nil, errors.New("client: NewsletterUpdate requires a jid")
	}
	if in.IsEmpty() {
		return nil, errors.New("client: NewsletterUpdate requires at least one field")
	}
	raw, err := c.runMexQuery(ctx, nlQueryUpdate, nlPathUpdate, newsletterUpdateVariables(jid, in, nil))
	if err != nil {
		return nil, err
	}
	return parseNewsletter(raw)
}

// NewsletterReactionMode sets the channel-wide reaction policy. mode accepts the
// enum values all | basic | none | blocklist (case-insensitive).
func (c *Client) NewsletterReactionMode(ctx context.Context, jid string, mode NewsletterReactionMode) error {
	if jid == "" {
		return errors.New("client: NewsletterReactionMode requires a jid")
	}
	norm := normalizeReactionMode(mode)
	if !validReactionMode(norm) {
		return fmt.Errorf("client: invalid reaction mode %q (want all|basic|none|blocklist)", mode)
	}
	_, err := c.runMexQuery(ctx, nlQueryUpdate, nlPathUpdate,
		newsletterUpdateVariables(jid, NewsletterUpdateInput{}, reactionModeSettings(norm)))
	return err
}

// normalizeReactionMode upper-cases the mode so callers may pass lowercase
// aliases (all/basic/none/blocklist).
func normalizeReactionMode(m NewsletterReactionMode) NewsletterReactionMode {
	switch m {
	case "all", "ALL":
		return ReactionModeAll
	case "basic", "BASIC":
		return ReactionModeBasic
	case "none", "NONE":
		return ReactionModeNone
	case "blocklist", "BLOCKLIST":
		return ReactionModeBlocklist
	}
	return m
}

// NewsletterChangeOwner transfers ownership of a channel to newOwnerJid.
func (c *Client) NewsletterChangeOwner(ctx context.Context, jid, newOwnerJid string) error {
	if jid == "" || newOwnerJid == "" {
		return errors.New("client: NewsletterChangeOwner requires jid and new owner jid")
	}
	_, err := c.runMexQuery(ctx, nlQueryChangeOwner, nlPathChangeOwner, newsletterUserVariables(jid, newOwnerJid))
	return err
}

// NewsletterDemote demotes an admin of a channel back to a regular subscriber.
func (c *Client) NewsletterDemote(ctx context.Context, jid, userJid string) error {
	if jid == "" || userJid == "" {
		return errors.New("client: NewsletterDemote requires jid and user jid")
	}
	_, err := c.runMexQuery(ctx, nlQueryDemote, nlPathDemote, newsletterUserVariables(jid, userJid))
	return err
}

// NewsletterAdminCount returns the number of admins of a channel.
func (c *Client) NewsletterAdminCount(ctx context.Context, jid string) (int, error) {
	if jid == "" {
		return 0, errors.New("client: NewsletterAdminCount requires a jid")
	}
	raw, err := c.runMexQuery(ctx, nlQueryAdminCount, nlPathAdminCount, newsletterIDVariables(jid))
	if err != nil {
		return 0, err
	}
	return parseAdminCount(raw)
}

// NewsletterFetchMessages fetches up to count messages from a channel, optionally
// only those at/after the since timestamp (Unix seconds; <=0 to omit). Uses the
// plain xmlns=newsletter transport.
func (c *Client) NewsletterFetchMessages(ctx context.Context, jid string, count int, since int64) ([]NewsletterMessage, error) {
	if jid == "" {
		return nil, errors.New("client: NewsletterFetchMessages requires a jid")
	}
	if count <= 0 {
		return nil, errors.New("client: NewsletterFetchMessages requires count > 0")
	}
	sess, ok := c.activeSession()
	if !ok {
		return nil, errors.New("client: newsletter op requires a live session")
	}
	req := buildNewsletterFetchMessages(c.nextIQID("wa-go-nl-msgs-"), jid, count, since)
	reply, err := c.sendIQ(ctx, sess, req)
	if err != nil {
		return nil, err
	}
	return parseNewsletterMessages(reply), nil
}

// LiveUpdatesSubscription is the result of subscribing to a channel's live
// updates: the server-granted duration (seconds) the subscription stays active.
type LiveUpdatesSubscription struct {
	Duration string
}

// SubscribeLiveUpdates subscribes to a channel's live updates and returns the
// granted duration. Uses the plain xmlns=newsletter transport.
func (c *Client) SubscribeLiveUpdates(ctx context.Context, jid string) (*LiveUpdatesSubscription, error) {
	if jid == "" {
		return nil, errors.New("client: SubscribeLiveUpdates requires a jid")
	}
	sess, ok := c.activeSession()
	if !ok {
		return nil, errors.New("client: newsletter op requires a live session")
	}
	req := buildSubscribeLiveUpdates(c.nextIQID("wa-go-nl-live-"), jid)
	reply, err := c.sendIQ(ctx, sess, req)
	if err != nil {
		return nil, err
	}
	node, ok := childByTag(reply, "live_updates")
	if !ok {
		return nil, nil
	}
	dur := node.Attrs["duration"]
	if dur == "" {
		return nil, nil
	}
	return &LiveUpdatesSubscription{Duration: dur}, nil
}
