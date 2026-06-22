// Package store persists a WhatsApp device's credentials and the signal-protocol
// state. The Creds struct mirrors the subset of Baileys' AuthenticationCreds that
// the pairing/auth flow (#2) and the signal layer (#3) need.
//
// Serialization: Creds is stored as a single JSON blob. Byte fields are encoded
// as base64 (via the keyBytes type) so the JSON is compact and ASCII-safe.
package store

import (
	"encoding/base64"
	"encoding/json"
)

// keyBytes is a byte slice that JSON-marshals as a base64 string, keeping the
// creds JSON ASCII-safe and compact. nil marshals as null.
type keyBytes []byte

func (b keyBytes) MarshalJSON() ([]byte, error) {
	if b == nil {
		return []byte("null"), nil
	}
	return json.Marshal(base64.StdEncoding.EncodeToString(b))
}

func (b *keyBytes) UnmarshalJSON(data []byte) error {
	var s *string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	if s == nil {
		*b = nil
		return nil
	}
	dec, err := base64.StdEncoding.DecodeString(*s)
	if err != nil {
		return err
	}
	*b = dec
	return nil
}

// CredKeyPair is the serializable form of a Curve25519 key pair.
type CredKeyPair struct {
	Priv keyBytes `json:"priv"`
	Pub  keyBytes `json:"pub"`
}

// CredSignedPreKey is the serializable form of a signed pre-key.
type CredSignedPreKey struct {
	KeyID     uint32      `json:"keyId"`
	KeyPair   CredKeyPair `json:"keyPair"`
	Signature keyBytes    `json:"signature"`
}

// Creds is the full, serializable credential set for one device.
//
// The first group is generated at identity creation (#2 keys). The second group
// ("post-pairing") is filled in after a successful pair-success exchange.
type Creds struct {
	// Identity (generated up front).
	NoiseKey       CredKeyPair      `json:"noiseKey"`
	IdentityKey    CredKeyPair      `json:"identityKey"`
	RegistrationID uint32           `json:"registrationId"`
	AdvSecret      keyBytes         `json:"advSecretKey"`
	SignedPreKey   CredSignedPreKey `json:"signedPreKey"`

	// Post-pairing (filled after a successful pair).
	Me         string   `json:"me,omitempty"`       // JID, e.g. "5511...@s.whatsapp.net"
	Account    keyBytes `json:"account,omitempty"`  // marshaled ADVSignedDeviceIdentity
	Platform   string   `json:"platform,omitempty"` // server-reported platform
	PushName   string   `json:"pushName,omitempty"` // display name
	Registered bool     `json:"registered"`         // true once paired
}

// SignalStore declares the signal-protocol persistence the #3 (encryption) layer
// will use. The current implementations are generic blob round-trips keyed by
// (namespace, key) over the signal_kv table; the cryptographic refinement (real
// session record encoding, identity trust, etc.) lands in #3. Returning
// (nil, false, nil) means "not found".
type SignalStore interface {
	// GetSignedPreKey returns the stored signed pre-key blob by id.
	GetSignedPreKey(id uint32) ([]byte, bool, error)
	// PutPreKeys stores one-time pre-key blobs keyed by id.
	PutPreKeys(preKeys map[uint32][]byte) error
	// LoadSession / StoreSession persist a session record blob for an address
	// (typically "<user>.<deviceID>").
	LoadSession(addr string) ([]byte, bool, error)
	StoreSession(addr string, record []byte) error
	// LoadIdentity / SaveIdentity persist a peer's identity key blob.
	LoadIdentity(addr string) ([]byte, bool, error)
	SaveIdentity(addr string, key []byte) error
	// LoadSenderKey / StoreSenderKey persist a group sender-key blob.
	LoadSenderKey(group string) ([]byte, bool, error)
	StoreSenderKey(group string, record []byte) error
}

// Store is the full persistence surface: device credentials plus the signal
// store, plus lifecycle (Close).
type Store interface {
	// LoadCreds returns the stored creds. The bool is false (with nil error and
	// nil creds) when no creds have been saved yet.
	LoadCreds() (*Creds, bool, error)
	// SaveCreds persists the creds (singleton; overwrites any existing).
	SaveCreds(*Creds) error

	SignalStore

	// Close releases the underlying database handle.
	Close() error
}
