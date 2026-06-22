package client

import (
	"fmt"
	"strconv"

	"github.com/felipeleal/wa-go/internal/keys"
	"github.com/felipeleal/wa-go/internal/store"
	"github.com/felipeleal/wa-go/internal/wire"
)

// initialPreKeyCount is how many one-time pre-keys we generate and upload after a
// fresh login, mirroring Baileys' INITIAL_PREKEY_COUNT (the WhatsApp server keeps
// a stock of these to hand out to peers that want to start a session with us).
const initialPreKeyCount = 30

// keyBundleType is Baileys' KEY_BUNDLE_TYPE: the single byte 0x05 (libsignal
// curve type). It is sent as the <type> child of the prekey upload iq.
var keyBundleType = []byte{5}

// encodeBigEndianN renders v as a big-endian byte slice of length n, matching
// Baileys' encodeBigEndian(value, size) used for the <registration> (4 bytes)
// and the prekey/signed-prekey <id> (3 bytes) fields.
func encodeBigEndianN(v uint32, n int) []byte {
	b := make([]byte, n)
	r := v
	for i := n - 1; i >= 0; i-- {
		b[i] = byte(r & 0xff)
		r >>= 8
	}
	return b
}

// xmppPreKey builds a <key> node for a one-time pre-key, matching Baileys'
// xmppPreKey: <key><id>{id 3B BE}</id><value>{pub 32B raw}</value></key>. The
// public key is the RAW 32-byte Curve25519 key (no 0x05 prefix).
func xmppPreKey(pk keys.PreKey) wire.Node {
	return wire.Node{
		Tag:   "key",
		Attrs: map[string]string{},
		Content: []wire.Node{
			{Tag: "id", Attrs: map[string]string{}, Content: encodeBigEndianN(pk.KeyID, 3)},
			{Tag: "value", Attrs: map[string]string{}, Content: append([]byte(nil), pk.KeyPair.Pub[:]...)},
		},
	}
}

// xmppSignedPreKey builds the <skey> node, matching Baileys' xmppSignedPreKey:
// <skey><id>{id 3B BE}</id><value>{pub 32B raw}</value><signature>{64B}</signature></skey>.
func xmppSignedPreKey(c *store.Creds) wire.Node {
	return wire.Node{
		Tag:   "skey",
		Attrs: map[string]string{},
		Content: []wire.Node{
			{Tag: "id", Attrs: map[string]string{}, Content: encodeBigEndianN(c.SignedPreKey.KeyID, 3)},
			{Tag: "value", Attrs: map[string]string{}, Content: append([]byte(nil), c.SignedPreKey.KeyPair.Pub...)},
			{Tag: "signature", Attrs: map[string]string{}, Content: append([]byte(nil), c.SignedPreKey.Signature...)},
		},
	}
}

// buildPreKeyUploadNode assembles the prekey upload iq exactly as Baileys'
// getNextPreKeysNode:
//
//	<iq to=@s.whatsapp.net type=set xmlns=encrypt id=...>
//	  <registration>{registrationId 4B BE}</registration>
//	  <type>{0x05}</type>
//	  <identity>{identityPub 32B RAW, no 0x05}</identity>
//	  <list>
//	    <key><id>{3B BE}</id><value>{prekeyPub 32B}</value></key> ...
//	  </list>
//	  <skey><id>{3B BE}</id><value>{signedPub 32B}</value><signature>{64B}</signature></skey>
//	</iq>
//
// preKeys carries the one-time pre-keys whose public halves go in <list>; the
// signed pre-key comes from creds. id is the stanza id attribute.
func buildPreKeyUploadNode(id string, c *store.Creds, preKeys []keys.PreKey) wire.Node {
	list := make([]wire.Node, len(preKeys))
	for i, pk := range preKeys {
		list[i] = xmppPreKey(pk)
	}
	return wire.Node{
		Tag: "iq",
		Attrs: map[string]string{
			"to":    sWhatsAppNet,
			"type":  "set",
			"xmlns": "encrypt",
			"id":    id,
		},
		Content: []wire.Node{
			{Tag: "registration", Attrs: map[string]string{}, Content: encodeBigEndianN(c.RegistrationID, 4)},
			{Tag: "type", Attrs: map[string]string{}, Content: append([]byte(nil), keyBundleType...)},
			{Tag: "identity", Attrs: map[string]string{}, Content: append([]byte(nil), c.IdentityKey.Pub...)},
			{Tag: "list", Attrs: map[string]string{}, Content: list},
			xmppSignedPreKey(c),
		},
	}
}

// generateAndStorePreKeys creates count one-time pre-keys starting at id 1,
// persists their pairs (and the device's signed pre-key pair) in the store so
// the receive path can load them when a peer's pkmsg references an id, and
// returns the generated pre-keys for upload.
func (c *Client) generateAndStorePreKeys(creds *store.Creds, count int) ([]keys.PreKey, error) {
	preKeys, err := keys.GenPreKeys(1, count)
	if err != nil {
		return nil, fmt.Errorf("client: generate pre-keys: %w", err)
	}

	batch := make(map[uint32]store.StoredKeyPair, len(preKeys))
	for _, pk := range preKeys {
		batch[pk.KeyID] = store.StoredKeyPair{
			Priv: append([]byte(nil), pk.KeyPair.Priv[:]...),
			Pub:  append([]byte(nil), pk.KeyPair.Pub[:]...),
		}
	}
	if err := c.store.StorePreKeys(batch); err != nil {
		return nil, fmt.Errorf("client: store pre-keys: %w", err)
	}

	// Persist the signed pre-key pair so the responder path can load it by id.
	if err := c.store.StoreSignedPreKey(creds.SignedPreKey.KeyID, store.StoredKeyPair{
		Priv: append([]byte(nil), creds.SignedPreKey.KeyPair.Priv...),
		Pub:  append([]byte(nil), creds.SignedPreKey.KeyPair.Pub...),
	}); err != nil {
		return nil, fmt.Errorf("client: store signed pre-key: %w", err)
	}
	return preKeys, nil
}

// uploadPreKeys generates+stores a fresh batch of pre-keys and sends the upload
// iq, mirroring Baileys' uploadPreKeys after <success>.
func (c *Client) uploadPreKeys(send func(wire.Node) error, creds *store.Creds) error {
	preKeys, err := c.generateAndStorePreKeys(creds, initialPreKeyCount)
	if err != nil {
		return err
	}
	id := "wa-go-pk-" + strconv.FormatUint(iqIDCounter.Add(1), 10)
	return send(buildPreKeyUploadNode(id, creds, preKeys))
}
