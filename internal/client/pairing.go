package client

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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

// registrationInput builds the waproto.RegInput from stored creds.
func registrationInput(c *store.Creds) waproto.RegInput {
	var idPub, skPub [32]byte
	var skSig [64]byte
	copy(idPub[:], c.IdentityKey.Pub)
	copy(skPub[:], c.SignedPreKey.KeyPair.Pub)
	copy(skSig[:], c.SignedPreKey.Signature)
	return waproto.RegInput{
		RegistrationID:  c.RegistrationID,
		IdentityPub:     idPub,
		SignedPreKeyID:  c.SignedPreKey.KeyID,
		SignedPreKeyPub: skPub,
		SignedPreKeySig: skSig,
		Version:         waVersion,
		Browser:         browser,
		CountryCode:     countryCode,
		SyncFull:        false,
	}
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
	conn, hsErr := c.handshake(ctx, creds, registrationPayloadBytes)
	if hsErr != nil {
		return false, hsErr
	}
	defer conn.Close()
	return c.pairingLoop(ctx, conn, creds)
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
		if err != nil {
			// Stream ended. If we already paired this is the expected restart;
			// the caller checks the store. Surface a disconnect either way.
			c.emit(DisconnectedEvent{Reason: "stream ended: " + err.Error()})
			return creds.Registered, nil
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

	// 6. keyIndex from ADVDeviceIdentity(account.details).
	var devIdentity waproto.ADVDeviceIdentity
	if err := proto.Unmarshal(account.GetDetails(), &devIdentity); err != nil {
		return wire.Node{}, out, fmt.Errorf("decode device identity: %w", err)
	}
	keyIndex := devIdentity.GetKeyIndex()

	// 7. Re-serialize with accountSignatureKey zeroed (encodeSignedDeviceIdentity
	//    with includeSignatureKey=false).
	reSigned := &waproto.ADVSignedDeviceIdentity{
		Details:         account.GetDetails(),
		AccountSignature: account.GetAccountSignature(),
		DeviceSignature: account.GetDeviceSignature(),
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
		Account:  accountEnc,
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

// registrationPayloadBytes marshals the registration ClientPayload for creds.
func registrationPayloadBytes(c *store.Creds) ([]byte, error) {
	pl, err := waproto.RegistrationPayload(registrationInput(c))
	if err != nil {
		return nil, err
	}
	return proto.Marshal(pl)
}

// loginPayloadBytes marshals the login/resume ClientPayload from registered creds.
func loginPayloadBytes(c *store.Creds) ([]byte, error) {
	user, device, err := parseJID(c.Me)
	if err != nil {
		return nil, err
	}
	pl := waproto.LoginPayload(waproto.LoginInput{
		Username:    user,
		Device:      device,
		Version:     waVersion,
		CountryCode: countryCode,
	})
	return proto.Marshal(pl)
}

// runLogin reconnects with the login payload and handles <success>/<failure>.
func (c *Client) runLogin(ctx context.Context, creds *store.Creds) error {
	conn, err := c.handshake(ctx, creds, loginPayloadBytes)
	if err != nil {
		return err
	}
	defer conn.Close()

	for {
		if ctx.Err() != nil {
			return nil
		}
		node, err := conn.ReadNode()
		if err != nil {
			c.emit(DisconnectedEvent{Reason: "stream ended: " + err.Error()})
			return nil
		}
		switch node.Tag {
		case "success":
			c.emit(LoggedInEvent{})
			return nil
		case "failure":
			reason := node.Attrs["reason"]
			if reason == "" {
				reason = node.Attrs["text"]
			}
			c.emit(DisconnectedEvent{Reason: "login failure: " + reason})
			return nil
		}
	}
}
