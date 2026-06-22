package client

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/felipeleal/wa-go/internal/keys"
	"github.com/felipeleal/wa-go/internal/signal"
	"github.com/felipeleal/wa-go/internal/store"
	"github.com/felipeleal/wa-go/internal/wire"
)

// iqTagCounter backs unique stanza ids for client-originated request iqs (usync,
// prekey-bundle fetch). Distinct from iqIDCounter only for readability.
func (c *Client) nextIQID(prefix string) string {
	return prefix + strconv.FormatUint(iqIDCounter.Add(1), 10)
}

// sendIQ sends a request iq through the live session's send function, then waits
// for the matching <iq type=result|error> routed back by the read loop. It
// returns the reply node. The id attribute of req must be set and unique.
func (c *Client) sendIQ(ctx context.Context, sess *session, req wire.Node) (wire.Node, error) {
	id := req.Attrs["id"]
	if id == "" {
		return wire.Node{}, errors.New("client: sendIQ requires an id attribute")
	}
	ch, cancel := c.registerIQ(id)
	defer cancel()

	if err := sess.send(req); err != nil {
		return wire.Node{}, fmt.Errorf("client: send iq %s: %w", id, err)
	}

	select {
	case <-ctx.Done():
		return wire.Node{}, ctx.Err()
	case reply, ok := <-ch:
		if !ok {
			return wire.Node{}, errors.New("client: connection closed before iq reply")
		}
		if reply.Attrs["type"] == "error" {
			return reply, fmt.Errorf("client: iq %s returned error", id)
		}
		return reply, nil
	}
}

// deviceJID identifies one device of a user as returned by a usync device-list
// query: the numeric user, the device index (0 = the phone) and the rendered JID.
type deviceJID struct {
	User   uint64
	Device uint32
	JID    string
}

// usyncDeviceQueryNode builds the usync device-list query iq, byte-for-byte as
// Baileys' getUSyncDevices / executeUSyncQuery with a single device protocol:
//
//	<iq to=@s.whatsapp.net type=get xmlns=usync id=...>
//	  <usync context=message mode=query sid=... last=true index=0>
//	    <query><devices version=2/></query>
//	    <list><user jid=.../></list>
//	  </usync>
//	</iq>
func usyncDeviceQueryNode(id, sid string, jids []string) wire.Node {
	users := make([]wire.Node, len(jids))
	for i, jid := range jids {
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
						Tag:   "query",
						Attrs: map[string]string{},
						Content: []wire.Node{
							{Tag: "devices", Attrs: map[string]string{"version": "2"}},
						},
					},
					{
						Tag:     "list",
						Attrs:   map[string]string{},
						Content: users,
					},
				},
			},
		},
	}
}

// parseUSyncDevices extracts the device list from a usync result, mirroring
// USyncDeviceProtocol.parser + extractDeviceJids: each <list><user jid=...> holds
// a <devices><device-list><device id=.. key-index=..>. Device 0 (the phone) is
// always included; non-zero devices require a key-index (per Baileys).
func parseUSyncDevices(reply wire.Node) ([]deviceJID, error) {
	usync, ok := childByTag(reply, "usync")
	if !ok {
		return nil, errors.New("client: usync result missing <usync>")
	}
	list, ok := childByTag(usync, "list")
	if !ok {
		return nil, errors.New("client: usync result missing <list>")
	}
	var out []deviceJID
	for _, user := range childrenByTag(list, "user") {
		jid := user.Attrs["jid"]
		uNum, _, err := parseJID(jid)
		if err != nil {
			continue
		}
		server := serverOfJID(jid)
		devicesNode, ok := childByTag(user, "devices")
		if !ok {
			continue
		}
		dl, ok := childByTag(devicesNode, "device-list")
		if !ok {
			continue
		}
		for _, dev := range childrenByTag(dl, "device") {
			d64, err := strconv.ParseUint(dev.Attrs["id"], 10, 32)
			if err != nil {
				continue
			}
			device := uint32(d64)
			// Baileys: keep device 0, and non-zero only if a key-index is present.
			if device != 0 && dev.Attrs["key-index"] == "" {
				continue
			}
			out = append(out, deviceJID{
				User:   uNum,
				Device: device,
				JID:    deviceJIDString(uNum, device, server),
			})
		}
	}
	return out, nil
}

// serverOfJID returns the server part of a JID ("s.whatsapp.net" by default).
func serverOfJID(jid string) string {
	for i := 0; i < len(jid); i++ {
		if jid[i] == '@' {
			return jid[i+1:]
		}
	}
	return "s.whatsapp.net"
}

// deviceJIDString renders a device JID, mirroring jidEncode: "<user>@server" for
// device 0, "<user>:<device>@server" otherwise.
func deviceJIDString(user uint64, device uint32, server string) string {
	if device == 0 {
		return fmt.Sprintf("%d@%s", user, server)
	}
	return fmt.Sprintf("%d:%d@%s", user, device, server)
}

// fetchDevices runs a usync device-list query for the given JIDs and returns the
// flattened device list. It uses the live session and the read-loop's iq router.
func (c *Client) fetchDevices(ctx context.Context, sess *session, jids ...string) ([]deviceJID, error) {
	id := c.nextIQID("wa-go-usync-")
	sid := c.nextIQID("")
	req := usyncDeviceQueryNode(id, sid, jids)
	reply, err := c.sendIQ(ctx, sess, req)
	if err != nil {
		return nil, err
	}
	return parseUSyncDevices(reply)
}

// preKeyBundle is the parsed prekey bundle for one device, as returned by an
// <iq xmlns=encrypt><key> fetch.
type preKeyBundle struct {
	JID             string
	RegistrationID  uint32
	IdentityKey     [32]byte // raw 32-byte (0x05 stripped)
	SignedPreKeyID  uint32
	SignedPreKeyPub [32]byte
	SignedPreKeySig [64]byte
	PreKeyID        uint32
	PreKeyPub       [32]byte
	HasPreKey       bool
}

// preKeyFetchNode builds the prekey-bundle fetch iq, mirroring Baileys'
// assertSessions:
//
//	<iq to=@s.whatsapp.net type=get xmlns=encrypt id=...>
//	  <key><user jid=.../> ...</key>
//	</iq>
func preKeyFetchNode(id string, jids []string) wire.Node {
	users := make([]wire.Node, len(jids))
	for i, jid := range jids {
		users[i] = wire.Node{Tag: "user", Attrs: map[string]string{"jid": jid}}
	}
	return wire.Node{
		Tag: "iq",
		Attrs: map[string]string{
			"to":    sWhatsAppNet,
			"type":  "get",
			"xmlns": "encrypt",
			"id":    id,
		},
		Content: []wire.Node{
			{Tag: "key", Attrs: map[string]string{}, Content: users},
		},
	}
}

// childUintBE reads a big-endian unsigned int from a numeric leaf child (the
// <id> / <registration> nodes carry big-endian bytes, mirroring Baileys'
// getBinaryNodeChildUInt). Returns 0 if absent.
func childUintBE(n wire.Node, tag string) uint32 {
	ch, ok := childByTag(n, tag)
	if !ok {
		return 0
	}
	b := nodeBytes(ch)
	var v uint32
	for _, x := range b {
		v = v<<8 | uint32(x)
	}
	return v
}

// stripKeyType removes a leading 0x05 type byte from a 33-byte signal public key,
// returning the raw 32-byte key. A 32-byte input is returned unchanged.
func stripKeyType(b []byte) ([32]byte, error) {
	var out [32]byte
	switch len(b) {
	case 33:
		if b[0] != 0x05 {
			return out, fmt.Errorf("client: unexpected key type byte 0x%02x", b[0])
		}
		copy(out[:], b[1:])
	case 32:
		copy(out[:], b)
	default:
		return out, fmt.Errorf("client: bad public key length %d", len(b))
	}
	return out, nil
}

// parsePreKeyBundles parses the <list><user> nodes of an <iq xmlns=encrypt>
// result into per-device bundles, mirroring parseAndInjectE2ESessions: each
// <user jid> has <registration>, <identity>, <skey>{id,value,signature} and an
// optional <key>{id,value} one-time prekey.
func parsePreKeyBundles(reply wire.Node) ([]preKeyBundle, error) {
	list, ok := childByTag(reply, "list")
	if !ok {
		return nil, errors.New("client: prekey result missing <list>")
	}
	var out []preKeyBundle
	for _, user := range childrenByTag(list, "user") {
		b := preKeyBundle{JID: user.Attrs["jid"]}

		idNode, ok := childByTag(user, "identity")
		if !ok {
			return nil, fmt.Errorf("client: prekey bundle %s missing identity", b.JID)
		}
		idPub, err := stripKeyType(nodeBytes(idNode))
		if err != nil {
			return nil, err
		}
		b.IdentityKey = idPub
		b.RegistrationID = childUintBE(user, "registration")

		skey, ok := childByTag(user, "skey")
		if !ok {
			return nil, fmt.Errorf("client: prekey bundle %s missing skey", b.JID)
		}
		b.SignedPreKeyID = childUintBE(skey, "id")
		valNode, ok := childByTag(skey, "value")
		if !ok {
			return nil, fmt.Errorf("client: prekey bundle %s skey missing value", b.JID)
		}
		spkPub, err := stripKeyType(nodeBytes(valNode))
		if err != nil {
			return nil, err
		}
		b.SignedPreKeyPub = spkPub
		sigNode, ok := childByTag(skey, "signature")
		if !ok || len(nodeBytes(sigNode)) != 64 {
			return nil, fmt.Errorf("client: prekey bundle %s bad signature", b.JID)
		}
		copy(b.SignedPreKeySig[:], nodeBytes(sigNode))

		if key, ok := childByTag(user, "key"); ok {
			b.PreKeyID = childUintBE(key, "id")
			kvNode, ok := childByTag(key, "value")
			if ok {
				pkPub, err := stripKeyType(nodeBytes(kvNode))
				if err != nil {
					return nil, err
				}
				b.PreKeyPub = pkPub
				b.HasPreKey = true
			}
		}
		out = append(out, b)
	}
	return out, nil
}

// fetchPreKeyBundles fetches prekey bundles for the given device JIDs, verifies
// each signed prekey signature against the device identity, runs X3DH as the
// initiator and persists the resulting session under the device's signal address.
func (c *Client) fetchPreKeyBundles(ctx context.Context, sess *session, jids []string) error {
	if len(jids) == 0 {
		return nil
	}
	id := c.nextIQID("wa-go-pkfetch-")
	reply, err := c.sendIQ(ctx, sess, preKeyFetchNode(id, jids))
	if err != nil {
		return err
	}
	bundles, err := parsePreKeyBundles(reply)
	if err != nil {
		return err
	}
	for _, b := range bundles {
		if err := c.initiateSessionFromBundle(sess.creds, b); err != nil {
			return fmt.Errorf("client: init session for %s: %w", b.JID, err)
		}
	}
	return nil
}

// initiateSessionFromBundle verifies the bundle's signed prekey signature, runs
// the X3DH initiator handshake (fresh random base + sending ratchet keys), and
// persists the new session keyed by the device's signal address.
func (c *Client) initiateSessionFromBundle(creds *store.Creds, b preKeyBundle) error {
	// Verify SPK signature over the 0x05-prefixed signed prekey public key,
	// signed by the device identity key (Baileys' Curve.verify).
	signedMsg := signalPubKey33(b.SignedPreKeyPub)
	if !keys.Verify(b.IdentityKey, signedMsg, b.SignedPreKeySig) {
		return errors.New("client: signed pre-key signature verification failed")
	}

	localIdentity := keyPairFromCreds(creds.IdentityKey)
	base, err := keys.GenKeyPair()
	if err != nil {
		return err
	}
	ratchet, err := keys.GenKeyPair()
	if err != nil {
		return err
	}

	p := signal.InitiatorParams{
		LocalIdentity:   localIdentity,
		LocalBaseKey:    base,
		RemoteIdentity:  signal33(b.IdentityKey),
		RemoteSignedPre: signal33(b.SignedPreKeyPub),
		HasPreKey:       b.HasPreKey,
	}
	if b.HasPreKey {
		p.RemotePreKey = signal33(b.PreKeyPub)
	}

	st, err := signal.InitiateSession(p, ratchet)
	if err != nil {
		return err
	}
	// Mark the pending prekey so the first Encrypt wraps a PreKeyWhisperMessage.
	st.LocalRegID = creds.RegistrationID
	st.PendingActive = true
	st.PendingSignedPreKeyID = b.SignedPreKeyID
	if b.HasPreKey {
		st.HasPendingPreKey = true
		st.PendingPreKeyID = b.PreKeyID
	}

	addr, err := signalAddress(b.JID)
	if err != nil {
		return err
	}
	if err := c.persistSession(addr, st); err != nil {
		return err
	}
	return c.store.SaveIdentity(addr, append([]byte(nil), st.RemoteIdentityPub[:]...))
}

// signalPubKey33 returns the 0x05-prefixed 33-byte form of a raw public key (for
// signature verification input), as a byte slice.
func signalPubKey33(pub [32]byte) []byte {
	out := make([]byte, 33)
	out[0] = 0x05
	copy(out[1:], pub[:])
	return out
}

// signal33 returns the 0x05-prefixed fixed-size form used by the signal package's
// [signalKeyLen]byte fields.
func signal33(pub [32]byte) [33]byte {
	var out [33]byte
	out[0] = 0x05
	copy(out[1:], pub[:])
	return out
}
