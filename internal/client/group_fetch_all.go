package client

import (
	"context"
	"errors"

	"github.com/jfelipesjc/wa-go/internal/wire"
)

// FetchAllGroups returns metadata for every group the account participates in,
// mirroring Baileys' groupFetchAllParticipating:
//
//	<iq to=@g.us xmlns=w:g2 type=get>
//	  <participating><participants/><description/></participating>
//	</iq>
//
// The reply carries <groups><group>…</group>…</groups>; each <group> is parsed
// with the same extractor as GroupMetadata.
func (c *Client) FetchAllGroups(ctx context.Context) ([]*GroupInfo, error) {
	sess, ok := c.activeSession()
	if !ok {
		return nil, errors.New("client: not logged in (no active session)")
	}
	req := buildGroupQuery(c.nextIQID("wa-go-group-"), "@g.us", "get", []wire.Node{
		{
			Tag:   "participating",
			Attrs: map[string]string{},
			Content: []wire.Node{
				{Tag: "participants", Attrs: map[string]string{}},
				{Tag: "description", Attrs: map[string]string{}},
			},
		},
	})
	reply, err := c.sendIQ(ctx, sess, req)
	if err != nil {
		return nil, err
	}
	return parseAllGroups(reply)
}

// parseAllGroups extracts every <group> under <groups> in a w:g2 participating
// reply, reusing parseGroupMetadata per group.
func parseAllGroups(reply wire.Node) ([]*GroupInfo, error) {
	groupsNode, ok := childByTag(reply, "groups")
	if !ok {
		return nil, nil // no groups → empty (not an error)
	}
	var out []*GroupInfo
	for _, g := range childrenByTag(groupsNode, "group") {
		// parseGroupMetadata expects a node whose child is the <group>; wrap it.
		gi, err := parseGroupMetadata(wire.Node{Content: []wire.Node{g}})
		if err != nil {
			continue // skip a malformed group rather than failing the whole list
		}
		out = append(out, gi)
	}
	return out, nil
}
