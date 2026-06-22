// Package client: lid.go implements the LID <-> PN distinction and mapping that
// WhatsApp rolled out with its privacy "LID" identifier (`<num>@lid`) living
// alongside the classic phone-number JID (`<num>@s.whatsapp.net`).
//
// Background (mirroring Baileys' WABinary/jid-utils.js + Signal/lid-mapping.js):
//
//   - A JID's *server* decides its kind: "s.whatsapp.net" is a phone-number (PN)
//     JID, "lid" is a privacy LID. Baileys: isPnUser = endsWith('@s.whatsapp.net'),
//     isLidUser = endsWith('@lid'). (There are also hosted variants "hosted" /
//     "hosted.lid" we do not handle yet.)
//   - A LID and a PN that refer to the same account share NOTHING numerically:
//     the LID user is an opaque number unrelated to the phone number. The only way
//     to learn the pairing is to be told (USync, or a message stanza that carries
//     both), hence a persistent mapping store.
//   - The mapping is kept on the bare *user* number (no device, no server), exactly
//     like Baileys' LIDMappingStore which decodes both sides and stores
//     pnUser -> lidUser and lidUser_reverse -> pnUser. Device separation is
//     re-attached at addressing time, not stored.
//
// addressingMode (documented, surfaced by newer message stanzas): a <message> now
// carries `addressing_mode=lid|pn`, and when LID-addressed also `participant_pn` /
// `sender_pn` / `peer_recipient_pn` (and the inverse `*_lid` attrs when PN
// addressed). receive.go uses these to learn LID<->PN pairs for free. See
// registerAddressingMapping.
package client

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/felipeleal/wa-go/internal/store"
	"github.com/felipeleal/wa-go/internal/wire"
)

const (
	// lidServer is the server part of a LID JID ("<user>@lid").
	lidServer = "lid"
	// pnServer is the server part of a phone-number JID.
	pnServer = "s.whatsapp.net"
)

// jidServer returns the server part of a JID ("s.whatsapp.net" for a bare/local
// number with no '@'). This is the same logic as usync.go's serverOfJID; it is
// re-exposed here under the lid.go vocabulary so the LID helpers read cleanly.
func jidServer(jid string) string {
	if i := strings.IndexByte(jid, '@'); i >= 0 {
		return jid[i+1:]
	}
	return pnServer
}

// isLID reports whether a JID is a privacy LID ("<user>@lid"), mirroring Baileys'
// isLidUser.
func isLID(jid string) bool { return jidServer(jid) == lidServer }

// isPN reports whether a JID is a phone-number JID ("<user>@s.whatsapp.net"),
// mirroring Baileys' isPnUser.
func isPN(jid string) bool { return jidServer(jid) == pnServer }

// jidUser returns the bare numeric user string of a JID (no device/agent, no
// server), e.g. "551199990000" for "551199990000:3@s.whatsapp.net". It reuses
// parseJID so the parsing rules stay in one place.
func jidUser(jid string) (string, error) {
	user, _, err := parseJID(jid)
	if err != nil {
		return "", err
	}
	return strconv.FormatUint(user, 10), nil
}

// normalizeJID strips the device/agent from a JID, keeping "<user>@<server>",
// mirroring Baileys' jidNormalizedUser (which also maps the legacy "c.us" server
// to "s.whatsapp.net"). It is handy for deduplicating addresses that differ only
// by device. A JID without '@' is returned unchanged.
func normalizeJID(jid string) string {
	at := strings.IndexByte(jid, '@')
	if at < 0 {
		return jid
	}
	server := jid[at+1:]
	if server == "c.us" {
		server = pnServer
	}
	user, _, err := parseJID(jid)
	if err != nil {
		return jid
	}
	return fmt.Sprintf("%d@%s", user, server)
}

// LIDStore maps LID users to PN users (and back), backed by a SignalStore for
// persistence and a small in-memory cache. It mirrors the role of Baileys'
// LIDMappingStore: store the bare numeric users, re-attach device data when an
// addressable JID is needed. Methods take and return full JIDs for convenience;
// internally only the numeric user is keyed.
type LIDStore struct {
	st store.SignalStore

	mu      sync.RWMutex
	lidToPN map[string]string // lidUser -> pnUser
	pnToLID map[string]string // pnUser  -> lidUser
}

// newLIDStore builds a LIDStore over the given persistence backend (may be nil
// for a memory-only store, e.g. in tests).
func newLIDStore(st store.SignalStore) *LIDStore {
	return &LIDStore{
		st:      st,
		lidToPN: map[string]string{},
		pnToLID: map[string]string{},
	}
}

// MapLIDToPN records a LID<->PN pairing. lid must be a "@lid" JID and pn a
// "@s.whatsapp.net" JID (device data is ignored; only the user is kept), matching
// Baileys' validation in storeLIDPNMappings. The mapping is cached and persisted.
func (l *LIDStore) MapLIDToPN(lid, pn string) error {
	if !isLID(lid) || !isPN(pn) {
		return fmt.Errorf("client: invalid LID-PN mapping: %q, %q", lid, pn)
	}
	lidUser, err := jidUser(lid)
	if err != nil {
		return err
	}
	pnUser, err := jidUser(pn)
	if err != nil {
		return err
	}

	l.mu.Lock()
	l.lidToPN[lidUser] = pnUser
	l.pnToLID[pnUser] = lidUser
	l.mu.Unlock()

	if l.st != nil {
		return l.st.StoreLIDMapping(lidUser, pnUser)
	}
	return nil
}

// PNForLID returns the PN JID (device 0) for a LID JID, preserving the LID's
// device index, mirroring Baileys' getPNForLID device re-attachment. It checks
// the in-memory cache, then the store.
func (l *LIDStore) PNForLID(lid string) (string, bool) {
	lidUser, err := jidUser(lid)
	if err != nil {
		return "", false
	}
	pnUser, ok := l.lookup(lidUser, true)
	if !ok {
		return "", false
	}
	_, device, _ := parseJID(lid)
	return deviceJIDString(mustParseUser(pnUser), device, pnServer), true
}

// LIDForPN returns the LID JID for a PN JID, preserving the PN's device index,
// mirroring Baileys' getLIDForPN device re-attachment. It checks the in-memory
// cache, then the store.
func (l *LIDStore) LIDForPN(pn string) (string, bool) {
	pnUser, err := jidUser(pn)
	if err != nil {
		return "", false
	}
	lidUser, ok := l.lookup(pnUser, false)
	if !ok {
		return "", false
	}
	_, device, _ := parseJID(pn)
	return deviceJIDString(mustParseUser(lidUser), device, lidServer), true
}

// lookup resolves a user mapping, consulting the cache then the store. When
// reverse is true it resolves lidUser->pnUser, otherwise pnUser->lidUser. A hit
// from the store is promoted into the cache.
func (l *LIDStore) lookup(user string, reverse bool) (string, bool) {
	l.mu.RLock()
	var got string
	var ok bool
	if reverse {
		got, ok = l.lidToPN[user]
	} else {
		got, ok = l.pnToLID[user]
	}
	l.mu.RUnlock()
	if ok {
		return got, true
	}
	if l.st == nil {
		return "", false
	}

	var err error
	if reverse {
		got, ok, err = l.st.LoadPNForLID(user)
	} else {
		got, ok, err = l.st.LoadLIDForPN(user)
	}
	if err != nil || !ok {
		return "", false
	}
	l.mu.Lock()
	if reverse {
		l.lidToPN[user] = got
		l.pnToLID[got] = user
	} else {
		l.pnToLID[user] = got
		l.lidToPN[got] = user
	}
	l.mu.Unlock()
	return got, true
}

// mustParseUser parses a bare numeric user string; on the impossible parse error
// it returns 0 (the user always originates from a previously parsed JID).
func mustParseUser(user string) uint64 {
	u, _ := strconv.ParseUint(user, 10, 64)
	return u
}

// --- per-Client LIDStore (package-level map, mirroring chatmod/mediaconn) ---
//
// The Client struct lives in client.go which this front must not edit, so the
// LIDStore is associated with each *Client via a package-level map keyed by the
// client pointer, exactly like appStateStores (chatmod.go) and mediaConnStates
// (mediaconn.go). lidStore lazily creates one bound to the client's store.

var (
	lidStoresMu sync.Mutex
	lidStores   = map[*Client]*LIDStore{}
)

// lidStore returns this client's LIDStore, lazily creating one bound to the
// client's persistence store.
func (c *Client) lidStore() *LIDStore {
	lidStoresMu.Lock()
	defer lidStoresMu.Unlock()
	ls, ok := lidStores[c]
	if !ok {
		ls = newLIDStore(c.store)
		lidStores[c] = ls
	}
	return ls
}

// usyncLIDQueryNode builds a USync query that asks for the LID of each phone JID,
// mirroring Baileys' getUSyncDevices which chains withDeviceProtocol().
// withLIDProtocol(). We only need the LID protocol here:
//
//	<iq to=@s.whatsapp.net type=get xmlns=usync id=...>
//	  <usync context=message mode=query sid=... last=true index=0>
//	    <query><lid/></query>
//	    <list><user jid=<phone>/></list>
//	  </usync>
//	</iq>
//
// The reply carries, per <user>, the resolved LID either as a <lid val=...> child
// (USyncLIDProtocol.parser reads node.attrs.val) or as a jid=/lid= attribute on
// the <user> node itself (the device call path reads a.lid). parseUSyncLID
// accepts both shapes.
func usyncLIDQueryNode(id, sid string, phoneJIDs []string) wire.Node {
	users := make([]wire.Node, len(phoneJIDs))
	for i, jid := range phoneJIDs {
		users[i] = wire.Node{Tag: "user", Attrs: map[string]string{"jid": jid}}
	}
	return wire.Node{
		Tag: "iq",
		Attrs: map[string]string{
			"to":    sWhatsAppNet,
			"type":  "get",
			"xmlns": "usync",
			"id":    id,
		},
		Content: []wire.Node{
			{
				Tag: "usync",
				Attrs: map[string]string{
					"context": "message",
					"mode":    "query",
					"sid":     sid,
					"last":    "true",
					"index":   "0",
				},
				Content: []wire.Node{
					{
						Tag:     "query",
						Attrs:   map[string]string{},
						Content: []wire.Node{{Tag: "lid", Attrs: map[string]string{}}},
					},
					{Tag: "list", Attrs: map[string]string{}, Content: users},
				},
			},
		},
	}
}

// lidUserResult pairs a queried phone JID with the LID the server resolved for it.
type lidUserResult struct {
	PhoneJID string
	LID      string
}

// parseUSyncLID extracts (phone, lid) pairs from a USync LID reply. It mirrors
// both the USyncLIDProtocol parser (the LID arrives as a <lid val=...> child) and
// the device-call path (the LID arrives as a `lid` attribute on <user>). The
// phone JID is the <user jid=...> attribute.
func parseUSyncLID(reply wire.Node) ([]lidUserResult, error) {
	usync, ok := childByTag(reply, "usync")
	if !ok {
		return nil, errors.New("client: usync lid reply missing <usync>")
	}
	list, ok := childByTag(usync, "list")
	if !ok {
		return nil, errors.New("client: usync lid reply missing <list>")
	}
	var out []lidUserResult
	for _, user := range childrenByTag(list, "user") {
		phone := user.Attrs["jid"]
		if phone == "" {
			continue
		}
		lid := user.Attrs["lid"]
		if lid == "" {
			if ln, ok := childByTag(user, "lid"); ok {
				// USyncLIDProtocol.parser reads node.attrs.val; fall back to the
				// node's leaf text for robustness.
				if v := ln.Attrs["val"]; v != "" {
					lid = v
				} else if v := strings.TrimSpace(string(nodeBytes(ln))); v != "" {
					lid = v
				}
			}
		}
		if lid == "" {
			continue
		}
		out = append(out, lidUserResult{PhoneJID: phone, LID: lid})
	}
	return out, nil
}

// LIDForPN resolves the LID JID for a phone JID. It first consults the local
// mapping (cache + store); on a miss it runs a USync LID query, persists every
// returned pairing, and returns the LID for phoneJID. Returns an error when no
// mapping can be established.
func (c *Client) LIDForPN(ctx context.Context, phoneJID string) (string, error) {
	if !isPN(phoneJID) {
		return "", fmt.Errorf("client: LIDForPN requires a phone JID, got %q", phoneJID)
	}
	ls := c.lidStore()
	if lid, ok := ls.LIDForPN(phoneJID); ok {
		return lid, nil
	}

	sess, ok := c.activeSession()
	if !ok {
		return "", errors.New("client: LIDForPN requires a live session")
	}
	id := c.nextIQID("wa-go-usync-")
	sid := c.nextIQID("")
	reply, err := c.sendIQ(ctx, sess, usyncLIDQueryNode(id, sid, []string{phoneJID}))
	if err != nil {
		return "", err
	}
	pairs, err := parseUSyncLID(reply)
	if err != nil {
		return "", err
	}
	for _, p := range pairs {
		// Normalise the LID to a plain "@lid" user JID before mapping; the
		// returned val may already be a full JID or a bare number.
		lidJID := p.LID
		if !strings.ContainsRune(lidJID, '@') {
			lidJID = lidJID + "@" + lidServer
		}
		if err := ls.MapLIDToPN(lidJID, p.PhoneJID); err != nil && debugPairing {
			fmt.Fprintf(debugOut, "[wa-go] map lid %s<->%s: %v\n", lidJID, p.PhoneJID, err)
		}
	}
	if lid, ok := ls.LIDForPN(phoneJID); ok {
		return lid, nil
	}
	return "", fmt.Errorf("client: no LID resolved for %q", phoneJID)
}

// PNForLID resolves the phone JID for a LID. WhatsApp's USync exposes a LID
// protocol (PN -> LID) but no documented reverse PN-by-LID query; the reverse
// mapping is learned passively (from message stanzas carrying participant_pn /
// sender_pn, or from a prior LIDForPN call) and read here from the local store.
// Returns ok=false when no mapping has been observed yet.
func (c *Client) PNForLID(lid string) (string, bool) {
	return c.lidStore().PNForLID(lid)
}

// registerAddressingMapping inspects a stanza's addressing attributes and records
// any LID<->PN pairing it reveals, mirroring the storeLIDPNMappings calls Baileys
// makes from messages-recv. A LID-addressed stanza (sender is "@lid", or
// addressing_mode=lid) carries the PN counterpart in participant_pn / sender_pn /
// peer_recipient_pn; a PN-addressed stanza carries the LID in the inverse
// participant_lid / sender_lid / peer_recipient_lid attributes.
func (c *Client) registerAddressingMapping(attrs map[string]string) {
	sender := attrs["participant"]
	if sender == "" {
		sender = attrs["from"]
	}
	mode := attrs["addressing_mode"]
	if mode == "" {
		if isLID(sender) {
			mode = "lid"
		} else {
			mode = "pn"
		}
	}

	var lidJID, pnJID string
	if mode == "lid" {
		lidJID = sender
		pnJID = firstNonEmpty(attrs["participant_pn"], attrs["sender_pn"], attrs["peer_recipient_pn"])
	} else {
		pnJID = sender
		lidJID = firstNonEmpty(attrs["participant_lid"], attrs["sender_lid"], attrs["peer_recipient_lid"])
	}
	if !isLID(lidJID) || !isPN(pnJID) {
		return
	}
	if err := c.lidStore().MapLIDToPN(lidJID, pnJID); err != nil && debugPairing {
		fmt.Fprintf(debugOut, "[wa-go] register addressing map %s<->%s: %v\n", lidJID, pnJID, err)
	}
}

// firstNonEmpty returns the first non-empty string of its arguments, or "".
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
