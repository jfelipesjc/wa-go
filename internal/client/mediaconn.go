package client

// mediaconn.go fetches the media connection descriptor WhatsApp requires before
// uploading/downloading media: an <iq type=set xmlns=w:m> carrying a single
// <media_conn> child, whose result holds the auth token, ttl, and the list of
// media hosts. It mirrors Baileys' refreshMediaConn
// (harness/node_modules/@whiskeysockets/baileys/lib/Socket/messages-send.js).
//
// The result is cached on the Client and only re-fetched when missing or past
// its ttl, exactly like Baileys keys its cache on fetchDate + ttl.

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/felipeleal/wa-go/internal/wire"
)

// MediaConn is the parsed <media_conn> descriptor: the auth token appended to
// upload URLs, the ordered media host list, and the ttl (seconds) after which it
// must be refreshed.
type MediaConn struct {
	Auth  string
	Hosts []string
	TTL   int

	// fetchedAt records when this descriptor was obtained, for ttl expiry.
	fetchedAt time.Time
}

// expired reports whether the descriptor is past its ttl relative to now. A
// zero-value MediaConn (never fetched) is always expired.
func (m *MediaConn) expired(now time.Time) bool {
	if m == nil || m.fetchedAt.IsZero() {
		return true
	}
	return now.Sub(m.fetchedAt) > time.Duration(m.TTL)*time.Second
}

// mediaConnCache holds the cached descriptor and its guard. It lives on the
// Client via the embedded field below; defined here so client.go is untouched.
type mediaConnCache struct {
	mu   sync.Mutex
	conn *MediaConn
}

// mediaConnState lazily associates a cache with each Client without editing the
// Client struct. Keyed by *Client; entries are cheap and never removed (one per
// live client, of which there are few).
var (
	mediaConnStatesMu sync.Mutex
	mediaConnStates   = map[*Client]*mediaConnCache{}
)

func (c *Client) mediaConnCacheState() *mediaConnCache {
	mediaConnStatesMu.Lock()
	defer mediaConnStatesMu.Unlock()
	st, ok := mediaConnStates[c]
	if !ok {
		st = &mediaConnCache{}
		mediaConnStates[c] = st
	}
	return st
}

// mediaConnQueryNode builds the media_conn request iq, byte-for-byte as Baileys'
// refreshMediaConn:
//
//	<iq to=@s.whatsapp.net type=set xmlns=w:m id=...>
//	  <media_conn/>
//	</iq>
func mediaConnQueryNode(id string) wire.Node {
	return wire.Node{
		Tag: "iq",
		Attrs: map[string]string{
			"to":    sWhatsAppNet,
			"type":  "set",
			"xmlns": "w:m",
			"id":    id,
		},
		Content: []wire.Node{
			{Tag: "media_conn", Attrs: map[string]string{}},
		},
	}
}

// parseMediaConn extracts the MediaConn from an <iq result> reply, mirroring
// Baileys: read <media_conn auth=.. ttl=..> and each <host hostname=..>. It is a
// pure function (no network) so it is unit-testable against a synthetic reply.
func parseMediaConn(reply wire.Node) (*MediaConn, error) {
	mc, ok := childByTag(reply, "media_conn")
	if !ok {
		return nil, errors.New("client: media_conn result missing <media_conn>")
	}
	conn := &MediaConn{
		Auth: mc.Attrs["auth"],
		TTL:  atoiDefault(mc.Attrs["ttl"], 0),
	}
	for _, h := range childrenByTag(mc, "host") {
		if hn := h.Attrs["hostname"]; hn != "" {
			conn.Hosts = append(conn.Hosts, hn)
		}
	}
	if conn.Auth == "" {
		return nil, errors.New("client: media_conn result missing auth")
	}
	if len(conn.Hosts) == 0 {
		return nil, errors.New("client: media_conn result has no hosts")
	}
	return conn, nil
}

// mediaConn returns a non-expired MediaConn, fetching a fresh one via an
// <iq xmlns=w:m><media_conn> query when the cache is empty or past its ttl. It
// requires a live session.
func (c *Client) mediaConn(ctx context.Context) (*MediaConn, error) {
	st := c.mediaConnCacheState()
	st.mu.Lock()
	defer st.mu.Unlock()

	if !st.conn.expired(time.Now()) {
		return st.conn, nil
	}

	sess, ok := c.activeSession()
	if !ok {
		return nil, errors.New("client: media_conn requires a live session")
	}

	id := c.nextIQID("wa-go-mediaconn-")
	reply, err := c.sendIQ(ctx, sess, mediaConnQueryNode(id))
	if err != nil {
		return nil, err
	}
	conn, err := parseMediaConn(reply)
	if err != nil {
		return nil, err
	}
	conn.fetchedAt = time.Now()
	st.conn = conn
	return conn, nil
}

// atoiDefault parses a base-10 int, returning def on any error or empty string.
func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n := 0
	neg := false
	for i, r := range s {
		if i == 0 && r == '-' {
			neg = true
			continue
		}
		if r < '0' || r > '9' {
			return def
		}
		n = n*10 + int(r-'0')
	}
	if neg {
		n = -n
	}
	return n
}
