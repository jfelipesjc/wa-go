package client

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/felipeleal/wa-go/internal/control"
	"github.com/felipeleal/wa-go/internal/keys"
	"github.com/felipeleal/wa-go/internal/store"
	"github.com/felipeleal/wa-go/internal/waproto"
	"github.com/felipeleal/wa-go/internal/wire"
	"google.golang.org/protobuf/proto"
)

// parseJID splits a WhatsApp JID into its numeric user and device parts.
// Formats handled: "user@server", "user.device@server", "user:device@server".
func parseJID(jid string) (user uint64, device uint32, err error) {
	at := strings.IndexByte(jid, '@')
	if at < 0 {
		return 0, 0, fmt.Errorf("client: malformed JID %q", jid)
	}
	local := jid[:at]
	// device delimiter can be ':' (agent) or '.' (device); take the numeric user
	// up to the first non-digit run.
	userStr := local
	if i := strings.IndexAny(local, ":."); i >= 0 {
		userStr = local[:i]
		rest := local[i+1:]
		// rest may itself contain a further delimiter (user:agent.device); take
		// the trailing numeric device.
		if j := strings.IndexAny(rest, ":."); j >= 0 {
			rest = rest[j+1:]
		}
		if d, e := strconv.ParseUint(rest, 10, 32); e == nil {
			device = uint32(d)
		}
	}
	u, e := strconv.ParseUint(userStr, 10, 64)
	if e != nil {
		return 0, 0, fmt.Errorf("client: JID user not numeric in %q: %w", jid, e)
	}
	return u, device, nil
}

// WA signature prefixes, mirroring Baileys' Defaults/index.js.
var (
	advAccountSigPrefix = []byte{6, 0} // WA_ADV_ACCOUNT_SIG_PREFIX
	advDeviceSigPrefix  = []byte{6, 1} // WA_ADV_DEVICE_SIG_PREFIX
)

const sWhatsAppNet = "@s.whatsapp.net"

// credsFromIdentity converts a freshly generated keys.Identity into a
// persistable store.Creds (pre-pairing fields only).
func credsFromIdentity(id keys.Identity) *store.Creds {
	cp := func(kp keys.KeyPair) store.CredKeyPair {
		return store.CredKeyPair{Priv: append([]byte(nil), kp.Priv[:]...), Pub: append([]byte(nil), kp.Pub[:]...)}
	}
	return &store.Creds{
		NoiseKey:       cp(id.NoiseKey),
		IdentityKey:    cp(id.IdentityKey),
		RegistrationID: id.RegistrationID,
		AdvSecret:      append([]byte(nil), id.AdvSecret[:]...),
		SignedPreKey: store.CredSignedPreKey{
			KeyID:     id.SignedPreKey.KeyID,
			KeyPair:   cp(id.SignedPreKey.KeyPair),
			Signature: append([]byte(nil), id.SignedPreKey.Signature[:]...),
		},
		Registered: false,
	}
}

// registrationInput builds the waproto.RegInput from stored creds, applying the
// given device profile's fingerprint (browser/version/locale/UA fields). The
// key material comes from creds; the fingerprint comes from the profile.
func registrationInput(c *store.Creds, profile control.DeviceProfile) waproto.RegInput {
	var idPub, skPub [32]byte
	var skSig [64]byte
	copy(idPub[:], c.IdentityKey.Pub)
	copy(skPub[:], c.SignedPreKey.KeyPair.Pub)
	copy(skSig[:], c.SignedPreKey.Signature)
	base := waproto.RegInput{
		RegistrationID:  c.RegistrationID,
		IdentityPub:     idPub,
		SignedPreKeyID:  c.SignedPreKey.KeyID,
		SignedPreKeyPub: skPub,
		SignedPreKeySig: skSig,
		SyncFull:        false,
	}
	return profile.RegInput(base)
}

// buildQRString builds the pairing QR URL exactly as Baileys'
// buildPairingQRData (companion-reg-client-utils.js):
//
//	"https://wa.me/settings/linked_devices#" +
//	  [ref, noiseKeyB64, identityKeyB64, advB64, platformId].join(",")
//
// noiseKeyB64/identityKeyB64 are the std-base64 of the respective public keys;
// advB64 is the std-base64 of the advSecretKey; platformId is the companion web
// client type ("1" = CHROME for the Ubuntu/Chrome browser tuple).
func buildQRString(ref string, noisePub, identityPub, advSecret []byte, platformID string) string {
	b64 := base64.StdEncoding.EncodeToString
	joined := ref + "," + b64(noisePub) + "," + b64(identityPub) + "," + b64(advSecret) + "," + platformID
	return "https://wa.me/settings/linked_devices#" + joined
}

// platformID returns the companion web client type id for our browser tuple,
// matching getCompanionPlatformId. For {Ubuntu, Chrome, ...} this is "1".
func platformID() string { return "1" }

// --- node helpers (Baileys' getBinaryNodeChild/Children) ---

func children(n wire.Node) []wire.Node {
	if c, ok := n.Content.([]wire.Node); ok {
		return c
	}
	return nil
}

func childByTag(n wire.Node, tag string) (wire.Node, bool) {
	for _, ch := range children(n) {
		if ch.Tag == tag {
			return ch, true
		}
	}
	return wire.Node{}, false
}

func childrenByTag(n wire.Node, tag string) []wire.Node {
	var out []wire.Node
	for _, ch := range children(n) {
		if ch.Tag == tag {
			out = append(out, ch)
		}
	}
	return out
}

// nodeBytes returns the leaf byte content of a node, or nil.
func nodeBytes(n wire.Node) []byte {
	switch c := n.Content.(type) {
	case []byte:
		return c
	case string:
		return []byte(c)
	}
	return nil
}

// runPairing runs the QR pairing flow: handshake with the registration payload,
// then loop reading nodes, handling pair-device (QR) and pair-success. It
// returns paired=true once pair-success has been processed and persisted (the
// server then ends the stream). paired=false means the context ended or the
// stream closed without success.
func (c *Client) runPairing(ctx context.Context, creds *store.Creds) (bool, error) {
	conn, hsErr := c.handshake(ctx, creds, c.registrationPayloadBytes)
	if hsErr != nil {
		return false, hsErr
	}
	defer conn.Close()
	return c.pairingLoop(ctx, conn, creds)
}

// debugPairing, when true, dumps each received node and the disconnect reason to
// debugOut. Temporary diagnostic for live pairing; gated off by default.
var (
	debugPairing           = false
	debugOut     io.Writer = io.Discard
)

// EnableDebug turns on verbose pairing diagnostics (each received node + the
// disconnect reason) written to w. Diagnostic use only.
func EnableDebug(w io.Writer) {
	debugPairing = true
	debugOut = w
}

// qrRotateInterval is how long each pairing ref's QR is displayed before
// rotating to the next ref the server provided (mirrors Baileys' ~20s cycle).
var qrRotateInterval = 20 * time.Second

// keepAliveInterval is how often we ping the server during pairing so it does
// not close the idle connection while the user is scanning. WhatsApp drops the
// stream within ~20s without a keep-alive ping.
var keepAliveInterval = 20 * time.Second

// pairingLoop reads nodes from conn and drives the pairing state machine. It is
// separated from runPairing so tests can drive it over a fake nodeConn without a
// real handshake.
//
// All writes to conn go through sendMu: the Noise transport cipher uses a
// sequential nonce counter, so concurrent SendNode calls (read-loop replies vs.
// the keep-alive goroutine) would corrupt the stream.
func (c *Client) pairingLoop(ctx context.Context, conn nodeConn, creds *store.Creds) (bool, error) {
	id := identityFromCreds(creds)

	var sendMu sync.Mutex
	send := func(n wire.Node) error {
		// Control Layer (C): outgoing frame hooks run pre-encrypt; a hook may
		// inspect or mutate the node in place before it hits the wire.
		c.runOutgoingHooks(&n)
		sendMu.Lock()
		defer sendMu.Unlock()
		return conn.SendNode(n)
	}

	loopCtx, cancel := context.WithCancel(ctx)

	// All background goroutines (keep-alive, QR rotation) exit when loopCtx is
	// cancelled. We wait for them before returning so none can emit on the
	// events channel after Connect closes it (emit on a closed channel panics).
	var wg sync.WaitGroup
	defer func() {
		cancel()
		wg.Wait()
	}()

	// Keep-alive: ping the server periodically so it keeps the stream open
	// while the QR is displayed and scanned.
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(keepAliveInterval)
		defer t.Stop()
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-t.C:
				_ = send(pingNode())
			}
		}
	}()

	// rotateStop, when closed, stops the active QR-rotation goroutine. A new
	// pair-device iq closes the previous one and installs a fresh channel. Using
	// a channel (not a context.CancelFunc) keeps go vet's lostcancel analyzer
	// happy across the reassign-in-loop; loopCtx still bounds the goroutine's life.
	var rotateStop chan struct{}
	stopRotation := func() {
		if rotateStop != nil {
			close(rotateStop)
			rotateStop = nil
		}
	}

	for {
		if ctx.Err() != nil {
			return false, nil
		}
		node, err := conn.ReadNode()
		if err == nil {
			// Control Layer (C): incoming frame hooks run post-decode.
			c.runIncomingHooks(node)
		}
		if err != nil {
			if debugPairing {
				fmt.Fprintf(debugOut, "[wa-go] stream ended: %v\n", err)
			}
			// Stream ended. If we already paired this is the expected restart;
			// the caller checks the store. Surface a disconnect either way.
			c.emit(DisconnectedEvent{Reason: "stream ended: " + err.Error()})
			return creds.Registered, nil
		}
		if debugPairing {
			fmt.Fprintf(debugOut, "[wa-go] node: <%s type=%q id=%q> children=%d\n",
				node.Tag, node.Attrs["type"], node.Attrs["id"], len(children(node)))
		}

		switch {
		case isIQSet(node, "pair-device"):
			// Reply to the pair-device iq, then (re)start QR rotation over the
			// refs it carries. A fresh pair-device iq replaces the previous refs.
			refs, err := c.replyPairDevice(send, node)
			if err != nil {
				return false, err
			}
			stopRotation()
			stop := make(chan struct{})
			rotateStop = stop
			wg.Add(1)
			go func() {
				defer wg.Done()
				c.rotateQR(loopCtx, stop, refs, creds)
			}()
		case isPairSuccess(node):
			stopRotation()
			reply, updated, err := handlePairSuccess(node, id, creds.AdvSecret)
			if err != nil {
				c.emit(DisconnectedEvent{Reason: "pair-success: " + err.Error()})
				return false, fmt.Errorf("client: pair-success: %w", err)
			}
			// Persist the post-pairing creds.
			creds.Me = updated.Me
			creds.Account = updated.Account
			creds.Platform = updated.Platform
			creds.PushName = updated.PushName
			creds.Registered = true
			if err := c.store.SaveCreds(creds); err != nil {
				return false, fmt.Errorf("client: save creds after pairing: %w", err)
			}
			if err := send(reply); err != nil {
				return false, fmt.Errorf("client: send pair-device-sign: %w", err)
			}
			c.emit(PairSuccessEvent{JID: creds.Me})
		}
	}
}

// replyPairDevice sends the empty <iq result> ack for a pair-device iq and
// returns the list of ref strings it carries (in order). QR emission is handled
// separately by rotateQR so refs can be displayed one at a time.
func (c *Client) replyPairDevice(send func(wire.Node) error, node wire.Node) ([]string, error) {
	id := node.Attrs["id"]
	if err := send(iqResult(id, nil)); err != nil {
		return nil, fmt.Errorf("client: reply pair-device iq: %w", err)
	}
	pd, ok := childByTag(node, "pair-device")
	if !ok {
		return nil, errors.New("client: pair-device node missing pair-device child")
	}
	refNodes := childrenByTag(pd, "ref")
	if len(refNodes) == 0 {
		return nil, errors.New("client: pair-device has no ref")
	}
	refs := make([]string, len(refNodes))
	for i, rn := range refNodes {
		refs[i] = string(nodeBytes(rn))
	}
	return refs, nil
}

// rotateQR emits the QR for each ref in turn, advancing every qrRotateInterval,
// until the refs are exhausted, ctx is cancelled (stream end), or stop is closed
// (a newer pair-device or pair-success). The first QR is emitted immediately so
// the user has something to scan at once.
func (c *Client) rotateQR(ctx context.Context, stop <-chan struct{}, refs []string, creds *store.Creds) {
	for _, ref := range refs {
		qr := buildQRString(ref, creds.NoiseKey.Pub, creds.IdentityKey.Pub, creds.AdvSecret, platformID())
		c.emit(QREvent{Code: qr})
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-time.After(qrRotateInterval):
		}
	}
}

// iqIDCounter backs unique iq ids for client-originated stanzas (keep-alive).
var iqIDCounter atomic.Uint64

// pingNode builds the keep-alive ping: <iq to=s.whatsapp.net type=get xmlns=w:p><ping/></iq>.
func pingNode() wire.Node {
	id := strconv.FormatUint(iqIDCounter.Add(1), 10)
	return wire.Node{
		Tag: "iq",
		Attrs: map[string]string{
			"to":    sWhatsAppNet,
			"type":  "get",
			"xmlns": "w:p",
			"id":    "wa-go-ka-" + id,
		},
		Content: []wire.Node{{Tag: "ping", Attrs: map[string]string{}}},
	}
}

// pairSuccessCreds carries the post-pairing fields handlePairSuccess extracts.
type pairSuccessCreds struct {
	Me       string
	Account  []byte // re-serialized ADVSignedDeviceIdentity (signature key zeroed)
	Platform string
	PushName string
}

// handlePairSuccess implements Baileys' configureSuccessfulPairing faithfully.
//
// It verifies the device-identity HMAC against advSecret, verifies the account
// signature, computes our device signature, re-serializes the
// ADVSignedDeviceIdentity (with deviceSignature set and accountSignatureKey
// zeroed), and builds the pair-device-sign reply. It returns the reply node and
// the post-pairing creds fields.
func handlePairSuccess(node wire.Node, id keys.Identity, advSecret []byte) (wire.Node, pairSuccessCreds, error) {
	var out pairSuccessCreds

	msgID, _ := node.Attrs["id"]
	ps, ok := childByTag(node, "pair-success")
	if !ok {
		return wire.Node{}, out, errors.New("missing pair-success node")
	}
	devIDNode, ok := childByTag(ps, "device-identity")
	if !ok {
		return wire.Node{}, out, errors.New("missing device-identity node")
	}
	deviceNode, ok := childByTag(ps, "device")
	if !ok {
		return wire.Node{}, out, errors.New("missing device node")
	}
	platformNode, _ := childByTag(ps, "platform")
	bizNode, _ := childByTag(ps, "biz")

	jid := deviceNode.Attrs["jid"]
	bizName := bizNode.Attrs["name"]
	platform := platformNode.Attrs["name"]

	// 1. ADVSignedDeviceIdentityHMAC.
	var hmacMsg waproto.ADVSignedDeviceIdentityHMAC
	if err := proto.Unmarshal(nodeBytes(devIDNode), &hmacMsg); err != nil {
		return wire.Node{}, out, fmt.Errorf("decode device-identity hmac: %w", err)
	}
	// 2. Verify HMAC-SHA256(details, advSecret) == hmac.
	mac := hmac.New(sha256.New, advSecret)
	mac.Write(hmacMsg.GetDetails())
	if !hmac.Equal(mac.Sum(nil), hmacMsg.GetHmac()) {
		return wire.Node{}, out, errors.New("invalid account signature (HMAC mismatch)")
	}

	// 3. ADVSignedDeviceIdentity from details.
	var account waproto.ADVSignedDeviceIdentity
	if err := proto.Unmarshal(hmacMsg.GetDetails(), &account); err != nil {
		return wire.Node{}, out, fmt.Errorf("decode signed device identity: %w", err)
	}

	// 4. Verify accountSignature over [6,0] || account.details || ourIdentityPub.
	accountMsg := concat(advAccountSigPrefix, account.GetDetails(), id.IdentityKey.Pub[:])
	var accSigKey [32]byte
	if len(account.GetAccountSignatureKey()) != 32 {
		return wire.Node{}, out, errors.New("account signature key not 32 bytes")
	}
	copy(accSigKey[:], account.GetAccountSignatureKey())
	var accSig [64]byte
	if len(account.GetAccountSignature()) != 64 {
		return wire.Node{}, out, errors.New("account signature not 64 bytes")
	}
	copy(accSig[:], account.GetAccountSignature())
	if !keys.Verify(accSigKey, accountMsg, accSig) {
		return wire.Node{}, out, errors.New("failed to verify account signature")
	}

	// 5. Compute deviceSignature over [6,1] || details || ourIdentityPub || accountSignatureKey.
	deviceMsg := concat(advDeviceSigPrefix, account.GetDetails(), id.IdentityKey.Pub[:], account.GetAccountSignatureKey())
	devSig, err := keys.Sign(id.IdentityKey.Priv, deviceMsg)
	if err != nil {
		return wire.Node{}, out, fmt.Errorf("compute device signature: %w", err)
	}
	account.DeviceSignature = devSig[:]

	// Persist the FULL signed device identity (WITH accountSignatureKey) — this is
	// what gets attached as <device-identity> to outgoing messages
	// (encodeSignedDeviceIdentity(account, true)). Only the pair reply below zeroes
	// the key. Storing the zeroed version makes our outgoing device-identity
	// incomplete and recipients drop our messages.
	accountFull, err := proto.Marshal(&account)
	if err != nil {
		return wire.Node{}, out, fmt.Errorf("encode full signed device identity: %w", err)
	}

	// 6. keyIndex from ADVDeviceIdentity(account.details).
	var devIdentity waproto.ADVDeviceIdentity
	if err := proto.Unmarshal(account.GetDetails(), &devIdentity); err != nil {
		return wire.Node{}, out, fmt.Errorf("decode device identity: %w", err)
	}
	keyIndex := devIdentity.GetKeyIndex()

	// 7. Re-serialize with accountSignatureKey zeroed (encodeSignedDeviceIdentity
	//    with includeSignatureKey=false).
	reSigned := &waproto.ADVSignedDeviceIdentity{
		Details:          account.GetDetails(),
		AccountSignature: account.GetAccountSignature(),
		DeviceSignature:  account.GetDeviceSignature(),
		// AccountSignatureKey intentionally nil.
	}
	accountEnc, err := proto.Marshal(reSigned)
	if err != nil {
		return wire.Node{}, out, fmt.Errorf("re-encode signed device identity: %w", err)
	}

	// 8. Build the pair-device-sign reply.
	reply := wire.Node{
		Tag:   "iq",
		Attrs: map[string]string{"to": sWhatsAppNet, "type": "result", "id": msgID},
		Content: []wire.Node{
			{
				Tag:   "pair-device-sign",
				Attrs: map[string]string{},
				Content: []wire.Node{
					{
						Tag:     "device-identity",
						Attrs:   map[string]string{"key-index": fmt.Sprintf("%d", keyIndex)},
						Content: accountEnc,
					},
				},
			},
		},
	}

	out = pairSuccessCreds{
		Me:       jid,
		Account:  accountFull, // full identity (with accountSignatureKey) for outgoing device-identity
		Platform: platform,
		PushName: bizName,
	}
	return reply, out, nil
}

// concat joins byte slices into a fresh slice.
func concat(parts ...[]byte) []byte {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// --- node classification ---

func isIQSet(n wire.Node, childTag string) bool {
	if n.Tag != "iq" || n.Attrs["type"] != "set" {
		return false
	}
	_, ok := childByTag(n, childTag)
	return ok
}

// isPairSuccess matches Baileys' "CB:iq,,pair-success" (any iq type, with a
// pair-success child).
func isPairSuccess(n wire.Node) bool {
	if n.Tag != "iq" {
		return false
	}
	_, ok := childByTag(n, "pair-success")
	return ok
}

// iqResult builds an empty <iq type=result id=...> reply to @s.whatsapp.net.
func iqResult(id string, content any) wire.Node {
	return wire.Node{
		Tag:     "iq",
		Attrs:   map[string]string{"to": sWhatsAppNet, "type": "result", "id": id},
		Content: content,
	}
}

// identityFromCreds reconstructs the in-memory keys.Identity needed for the
// pair-success crypto (only the identity key pair and adv secret are used).
func identityFromCreds(c *store.Creds) keys.Identity {
	var id keys.Identity
	copy(id.IdentityKey.Priv[:], c.IdentityKey.Priv)
	copy(id.IdentityKey.Pub[:], c.IdentityKey.Pub)
	copy(id.NoiseKey.Priv[:], c.NoiseKey.Priv)
	copy(id.NoiseKey.Pub[:], c.NoiseKey.Pub)
	copy(id.AdvSecret[:], c.AdvSecret)
	id.RegistrationID = c.RegistrationID
	return id
}

// --- handshake + login ---

// nodeConn is the subset of *wire.Conn the pairing/login flow uses, so tests can
// stub it.
type nodeConn interface {
	SendNode(wire.Node) error
	ReadNode() (wire.Node, error)
	Close() error
}

// registrationPayloadBytes marshals the registration ClientPayload for creds,
// using the client's device profile fingerprint.
func (c *Client) registrationPayloadBytes(creds *store.Creds) ([]byte, error) {
	pl, err := waproto.RegistrationPayload(registrationInput(creds, c.profile))
	if err != nil {
		return nil, err
	}
	return proto.Marshal(pl)
}

// loginPayloadBytes marshals the login/resume ClientPayload from registered
// creds, using the client's device profile fingerprint.
func (c *Client) loginPayloadBytes(creds *store.Creds) ([]byte, error) {
	user, device, err := parseJID(creds.Me)
	if err != nil {
		return nil, err
	}
	base := waproto.LoginInput{
		Username: user,
		Device:   device,
	}
	pl := waproto.LoginPayload(c.profile.LoginInput(base))
	return proto.Marshal(pl)
}

// runLogin reconnects with the login payload and handles <success>/<failure>.
func (c *Client) runLogin(ctx context.Context, creds *store.Creds) error {
	conn, err := c.handshake(ctx, creds, c.loginPayloadBytes)
	if err != nil {
		return err
	}
	defer conn.Close()
	return c.loginLoop(ctx, conn, creds)
}

// loginLoop drives the post-login state machine: on <success> it uploads
// pre-keys and goes active, then handles incoming <message> stanzas (decrypt +
// emit + receipt/ack). It is separated from runLogin so tests can drive it over
// a fake nodeConn without a real handshake.
//
// As in pairingLoop, all writes go through sendMu because the Noise transport's
// nonce counter is not concurrency-safe (the keep-alive goroutine and the read
// loop both send).
func (c *Client) loginLoop(ctx context.Context, conn nodeConn, creds *store.Creds) error {
	var sendMu sync.Mutex
	send := func(n wire.Node) error {
		// Control Layer (C): outgoing frame hooks run pre-encrypt.
		c.runOutgoingHooks(&n)
		sendMu.Lock()
		defer sendMu.Unlock()
		return conn.SendNode(n)
	}

	// Publish the live session so SendText can use this connection, and tear it
	// down (plus any waiting iq requests) when the loop exits.
	c.setActive(&session{send: send, creds: creds})
	defer c.clearActive()

	loopCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer func() {
		cancel()
		wg.Wait()
	}()

	// Keep-alive so the server keeps the stream open while we wait for messages.
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(keepAliveInterval)
		defer t.Stop()
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-t.C:
				_ = send(pingNode())
			}
		}
	}()

	for {
		if ctx.Err() != nil {
			return nil
		}
		node, err := conn.ReadNode()
		if err == nil {
			// Control Layer (C): incoming frame hooks run post-decode.
			c.runIncomingHooks(node)
		}
		if err != nil {
			c.emit(DisconnectedEvent{Reason: "stream ended: " + err.Error()})
			return nil
		}
		if debugPairing {
			fmt.Fprintf(debugOut, "[wa-go] node: <%s type=%q id=%q from=%q> children=%d\n",
				node.Tag, node.Attrs["type"], node.Attrs["id"], node.Attrs["from"], len(children(node)))
		}
		// Route replies to outstanding iq requests (usync, prekey-bundle fetch)
		// before the default handling: an <iq type=result|error> whose id matches a
		// pending request is delivered to the waiting goroutine and consumed here.
		if node.Tag == "iq" {
			if t := node.Attrs["type"]; t == "result" || t == "error" {
				if c.deliverIQ(node) {
					continue
				}
			}
		}
		switch node.Tag {
		case "success":
			c.emit(LoggedInEvent{})
			// Post-login: upload pre-keys, then announce active presence so the
			// server delivers queued/incoming messages. Mirrors socket.js's
			// CB:success handler (uploadPreKeys + sendPassiveIq('active')).
			if err := c.uploadPreKeys(send, creds); err != nil {
				c.emit(DisconnectedEvent{Reason: "upload pre-keys: " + err.Error()})
				return err
			}
			if err := send(passiveIqNode("active")); err != nil {
				return err
			}
			// Announce availability so the server fans incoming messages to this
			// companion device (without it, only the link-time history syncs).
			if err := send(presenceAvailableNode()); err != nil {
				return err
			}
		case "failure":
			reason := node.Attrs["reason"]
			if reason == "" {
				reason = node.Attrs["text"]
			}
			c.emit(DisconnectedEvent{Reason: "login failure: " + reason})
			return nil
		case "message":
			if err := c.handleMessage(send, node, creds); err != nil && debugPairing {
				fmt.Fprintf(debugOut, "[wa-go] handleMessage: %v\n", err)
			}
		case "notification":
			// Emit granular typed events (w:gp2/encrypt/picture/app-state/...) for
			// the API layer, then ack: the server expects acks and may withhold
			// delivery otherwise.
			c.handleNotification(node)
			_ = send(stanzaAckNode(node, creds.Me))
		case "receipt":
			c.emit(ReceiptEvent{
				From:        node.Attrs["from"],
				Participant: node.Attrs["participant"],
				ID:          node.Attrs["id"],
				Type:        node.Attrs["type"],
			})
			// Emit the richer ReceiptUpdateEvent for delivery/read/played receipts
			// (retry receipts are excluded; the resend path below handles those).
			if !isRetryReceipt(node) {
				c.handleReceiptUpdate(node)
			}
			// A <receipt type=retry> means a device could not decrypt a message WE
			// sent: re-encrypt and resend it (Baileys' sendMessagesAgain). The
			// resend may issue blocking iq fetches (prekey bundle), so run it off
			// the read loop. assertSessions/sendIQ need the live session; capture it.
			if isRetryReceipt(node) {
				if sess, ok := c.activeSession(); ok {
					rn := node
					go func() {
						if err := c.handleRetryReceipt(loopCtx, sess, rn); err != nil && debugPairing {
							fmt.Fprintf(debugOut, "[wa-go] handle retry receipt: %v\n", err)
						}
					}()
				}
			}
			_ = send(stanzaAckNode(node, creds.Me))
		case "call":
			// Surface the call to the app and ack it (class="call"). We do NOT
			// auto-reject — the app decides via RejectCall.
			if info, perr := parseCallNode(node); perr == nil {
				c.emit(CallEvent{Info: *info})
			}
			_ = send(callAckNode(node, creds.Me))
		case "presence", "chatstate":
			// Forwarded presence / typing state for a subscribed contact.
			if ev, ok := parsePresenceNode(node); ok {
				c.emit(ev)
			}
		case "iq":
			// Server pings / queries: ack pings so the stream stays healthy.
			if node.Attrs["type"] == "get" {
				if _, ok := childByTag(node, "ping"); ok {
					_ = send(iqResult(node.Attrs["id"], nil))
				}
			}
		}
	}
}

// passiveIqNode builds the <iq xmlns=passive type=set> with a single child tag
// (e.g. "active"), mirroring Baileys' sendPassiveIq.
func passiveIqNode(tag string) wire.Node {
	id := "wa-go-passive-" + strconv.FormatUint(iqIDCounter.Add(1), 10)
	return wire.Node{
		Tag: "iq",
		Attrs: map[string]string{
			"to":    sWhatsAppNet,
			"xmlns": "passive",
			"type":  "set",
			"id":    id,
		},
		Content: []wire.Node{{Tag: tag, Attrs: map[string]string{}}},
	}
}
