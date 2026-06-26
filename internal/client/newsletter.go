// Package client: newsletter.go implements WhatsApp Channels (newsletters).
//
// Newsletter operations go over the "w:mex" GraphQL-style transport, the same
// one Baileys uses (lib/Socket/newsletter.js + lib/Socket/mex.js). The request
// is an iq whose single <query query_id=...> child carries a UTF-8 JSON body
// {"variables": {...}} as its *leaf bytes*; the reply is an iq with a <result>
// child whose leaf bytes are JSON {"data": {<dataPath>: ...}} (or {"errors":[...]}).
//
// Confirmed Baileys structures:
//
//	wMexQuery (lib/Socket/mex.js):
//	  <iq xmlns=w:mex to=@s.whatsapp.net type=get id=...>
//	    <query query_id=<queryId}>{"variables":{...}}</query>
//	  </iq>
//	reply: <iq ...><result>{"data":{<dataPath>:{...}}}</result></iq>
//
// Query IDs and data paths copied verbatim from Baileys (lib/Types/Mex.js):
//
//	CREATE       = "8823471724422422"   path xwa2_newsletter_create
//	METADATA     = "6563316087068696"   path xwa2_newsletter
//	FOLLOW       = "24404358912487870"  path xwa2_newsletter_join_v2
//	UNFOLLOW     = "9767147403369991"   path xwa2_newsletter_leave_v2
//	MUTE         = "29766401636284406"  path xwa2_newsletter_mute_v2
//	UNMUTE       = "9864994326891137"   path xwa2_newsletter_unmute_v2
//
// Variables per op (lib/Socket/newsletter.js):
//
//	CREATE   {"input":{"name":<name>,"description":<desc|null>}}
//	METADATA {"fetch_creation_time":true,"fetch_full_image":true,
//	          "fetch_viewer_metadata":true,
//	          "input":{"key":<jidOrInvite>,"type":"JID"|"INVITE"}}
//	FOLLOW / UNFOLLOW / MUTE / UNMUTE  {"newsletter_id":<jid>}
//
// The newsletterMetadata result shape (parseNewsletterCreateResponse /
// parseNewsletterMetadata) nests the interesting fields under thread_metadata;
// NewsletterInfo flattens the subset this build consumes.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/jfelipesjc/wa-go/internal/wire"
)

// xmlnsMex is the namespace for the GraphQL-style newsletter transport.
const xmlnsMex = "w:mex"

// Newsletter query IDs, copied verbatim from Baileys (lib/Types/Mex.js QueryIds).
const (
	nlQueryCreate   = "8823471724422422"
	nlQueryMetadata = "6563316087068696"
	nlQueryFollow   = "24404358912487870"
	nlQueryUnfollow = "9767147403369991"
	nlQueryMute     = "29766401636284406"
	nlQueryUnmute   = "9864994326891137"
)

// Newsletter w:mex data paths (lib/Types/Mex.js XWAPaths). These are the keys
// under the JSON reply's "data" object.
const (
	nlPathCreate   = "xwa2_newsletter_create"
	nlPathMetadata = "xwa2_newsletter"
	nlPathFollow   = "xwa2_newsletter_join_v2"
	nlPathUnfollow = "xwa2_newsletter_leave_v2"
	nlPathMute     = "xwa2_newsletter_mute_v2"
	nlPathUnmute   = "xwa2_newsletter_unmute_v2"
)

// NewsletterKeyType selects how a newsletter is addressed in a metadata fetch:
// by its JID or by an invite code/key.
type NewsletterKeyType string

const (
	NewsletterKeyJID    NewsletterKeyType = "JID"
	NewsletterKeyInvite NewsletterKeyType = "INVITE"
)

// NewsletterInfo is the parsed metadata of a channel, flattening the subset of
// Baileys' newsletter thread_metadata this build consumes.
type NewsletterInfo struct {
	JID             string
	Name            string
	Description     string
	Invite          string
	SubscriberCount int
	Verification    string
	CreationTime    int64
	MuteState       string
}

// --- node builders (pure; testable without a session) ---

// buildMexQuery wraps a JSON {"variables": variables} body in the standard
// w:mex iq envelope (Baileys' wMexQuery). The JSON is carried as the leaf bytes
// of the <query> node.
func buildMexQuery(id, queryID string, variables any) (wire.Node, error) {
	body, err := json.Marshal(struct {
		Variables any `json:"variables"`
	}{Variables: variables})
	if err != nil {
		return wire.Node{}, fmt.Errorf("client: marshal mex variables: %w", err)
	}
	return wire.Node{
		Tag: "iq",
		Attrs: map[string]string{
			"id":    id,
			"xmlns": xmlnsMex,
			"type":  "get",
			"to":    sWhatsAppNet,
		},
		Content: []wire.Node{
			{
				Tag:     "query",
				Attrs:   map[string]string{"query_id": queryID},
				Content: body,
			},
		},
	}, nil
}

// newsletterCreateVariables builds the CREATE variables JSON value.
func newsletterCreateVariables(name, description string) map[string]any {
	var desc any
	if description != "" {
		desc = description
	} // else nil -> JSON null, matching Baileys (description ?? null)
	return map[string]any{
		"input": map[string]any{
			"name":        name,
			"description": desc,
		},
	}
}

// newsletterMetadataVariables builds the METADATA variables JSON value.
func newsletterMetadataVariables(key string, typ NewsletterKeyType) map[string]any {
	return map[string]any{
		"fetch_creation_time":   true,
		"fetch_full_image":      true,
		"fetch_viewer_metadata": true,
		"input": map[string]any{
			"key":  key,
			"type": string(typ),
		},
	}
}

// newsletterIDVariables builds the {"newsletter_id": jid} variables shared by
// follow/unfollow/mute/unmute.
func newsletterIDVariables(jid string) map[string]any {
	return map[string]any{"newsletter_id": jid}
}

// --- response parsing ---

// mexResult is the decoded shape of a w:mex <result> JSON body.
type mexResult struct {
	Data   map[string]json.RawMessage `json:"data"`
	Errors []struct {
		Message    string `json:"message"`
		Extensions struct {
			ErrorCode int `json:"error_code"`
		} `json:"extensions"`
	} `json:"errors"`
}

// extractMexData pulls the JSON value at data[dataPath] from a w:mex reply,
// surfacing GraphQL errors (Baileys' executeWMexQuery behaviour). dataPath may
// be "" to return the whole data object.
func extractMexData(reply wire.Node, dataPath string) (json.RawMessage, error) {
	resultNode, ok := childByTag(reply, "result")
	if !ok {
		return nil, errors.New("client: w:mex reply missing <result> node")
	}
	raw := nodeBytes(resultNode)
	if len(raw) == 0 {
		return nil, errors.New("client: w:mex reply <result> is empty")
	}
	var res mexResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("client: decode w:mex result: %w", err)
	}
	if len(res.Errors) > 0 {
		e := res.Errors[0]
		return nil, fmt.Errorf("client: w:mex error (%d): %s", e.Extensions.ErrorCode, e.Message)
	}
	if dataPath == "" {
		b, _ := json.Marshal(res.Data)
		return b, nil
	}
	v, ok := res.Data[dataPath]
	if !ok {
		return nil, fmt.Errorf("client: w:mex result missing data.%s", dataPath)
	}
	return v, nil
}

// rawNewsletter mirrors the JSON shape of a newsletter create/metadata payload
// (thread_metadata + viewer_metadata), as in Baileys' parse helpers.
type rawNewsletter struct {
	ID             string `json:"id"`
	ThreadMetadata struct {
		Name struct {
			Text string `json:"text"`
		} `json:"name"`
		Description struct {
			Text string `json:"text"`
		} `json:"description"`
		Invite           string `json:"invite"`
		SubscribersCount string `json:"subscribers_count"`
		Verification     string `json:"verification"`
		CreationTime     string `json:"creation_time"`
	} `json:"thread_metadata"`
	ViewerMetadata struct {
		Mute string `json:"mute"`
	} `json:"viewer_metadata"`
}

// parseNewsletter decodes a newsletter JSON payload (the value at the data path)
// into a NewsletterInfo. Mirrors Baileys' parseNewsletterCreateResponse /
// parseNewsletterMetadata, including the latter's "result" unwrapping.
func parseNewsletter(raw json.RawMessage) (*NewsletterInfo, error) {
	// parseNewsletterMetadata: payload may be wrapped as {"result": {...}}.
	var wrapper struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &wrapper); err == nil && len(wrapper.Result) > 0 {
		raw = wrapper.Result
	}
	var rn rawNewsletter
	if err := json.Unmarshal(raw, &rn); err != nil {
		return nil, fmt.Errorf("client: decode newsletter payload: %w", err)
	}
	if rn.ID == "" {
		return nil, errors.New("client: newsletter payload missing id")
	}
	info := &NewsletterInfo{
		JID:          rn.ID,
		Name:         rn.ThreadMetadata.Name.Text,
		Description:  rn.ThreadMetadata.Description.Text,
		Invite:       rn.ThreadMetadata.Invite,
		Verification: rn.ThreadMetadata.Verification,
		MuteState:    rn.ViewerMetadata.Mute,
	}
	if n, err := strconv.Atoi(rn.ThreadMetadata.SubscribersCount); err == nil {
		info.SubscriberCount = n
	}
	if t, err := strconv.ParseInt(rn.ThreadMetadata.CreationTime, 10, 64); err == nil {
		info.CreationTime = t
	}
	return info, nil
}

// --- public methods ---

// runMexQuery sends a w:mex query and returns the raw JSON at data[dataPath].
func (c *Client) runMexQuery(ctx context.Context, queryID, dataPath string, variables any) (json.RawMessage, error) {
	sess, ok := c.activeSession()
	if !ok {
		return nil, errors.New("client: newsletter op requires a live session")
	}
	req, err := buildMexQuery(c.nextIQID("wa-go-mex-"), queryID, variables)
	if err != nil {
		return nil, err
	}
	reply, err := c.sendIQ(ctx, sess, req)
	if err != nil {
		return nil, err
	}
	return extractMexData(reply, dataPath)
}

// NewsletterMetadata fetches a channel's metadata. keyType selects whether
// jidOrInvite is interpreted as a JID or an invite key.
func (c *Client) NewsletterMetadata(ctx context.Context, jidOrInvite string, keyType NewsletterKeyType) (*NewsletterInfo, error) {
	if jidOrInvite == "" {
		return nil, errors.New("client: NewsletterMetadata requires a jid or invite key")
	}
	if keyType == "" {
		keyType = NewsletterKeyJID
	}
	raw, err := c.runMexQuery(ctx, nlQueryMetadata, nlPathMetadata, newsletterMetadataVariables(jidOrInvite, keyType))
	if err != nil {
		return nil, err
	}
	return parseNewsletter(raw)
}

// NewsletterCreate creates a new channel with the given name and (optional)
// description and returns its metadata.
func (c *Client) NewsletterCreate(ctx context.Context, name, description string) (*NewsletterInfo, error) {
	if name == "" {
		return nil, errors.New("client: NewsletterCreate requires a name")
	}
	raw, err := c.runMexQuery(ctx, nlQueryCreate, nlPathCreate, newsletterCreateVariables(name, description))
	if err != nil {
		return nil, err
	}
	return parseNewsletter(raw)
}

// NewsletterFollow follows (subscribes to) a channel.
func (c *Client) NewsletterFollow(ctx context.Context, jid string) error {
	if jid == "" {
		return errors.New("client: NewsletterFollow requires a jid")
	}
	_, err := c.runMexQuery(ctx, nlQueryFollow, nlPathFollow, newsletterIDVariables(jid))
	return err
}

// NewsletterUnfollow unfollows (unsubscribes from) a channel.
func (c *Client) NewsletterUnfollow(ctx context.Context, jid string) error {
	if jid == "" {
		return errors.New("client: NewsletterUnfollow requires a jid")
	}
	_, err := c.runMexQuery(ctx, nlQueryUnfollow, nlPathUnfollow, newsletterIDVariables(jid))
	return err
}

// NewsletterMute mutes (mute=true) or unmutes (mute=false) a channel.
func (c *Client) NewsletterMute(ctx context.Context, jid string, mute bool) error {
	if jid == "" {
		return errors.New("client: NewsletterMute requires a jid")
	}
	queryID, dataPath := nlQueryMute, nlPathMute
	if !mute {
		queryID, dataPath = nlQueryUnmute, nlPathUnmute
	}
	_, err := c.runMexQuery(ctx, queryID, dataPath, newsletterIDVariables(jid))
	return err
}
