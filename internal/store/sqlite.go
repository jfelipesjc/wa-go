package store

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	_ "modernc.org/sqlite" // CGo-free SQLite driver, registered as "sqlite".
)

// signal_kv namespaces. Kept as constants so #3 and tests agree on the layout.
const (
	nsSignedPreKey = "signed_pre_key"
	nsPreKey       = "pre_key"
	nsSession      = "session"
	nsIdentity     = "identity"
	nsSenderKey    = "sender_key"
	nsAppStateKey  = "app_state_key"
	nsLIDMap       = "lid_map"
)

const schema = `
CREATE TABLE IF NOT EXISTS creds (
    id   INTEGER PRIMARY KEY CHECK (id = 1),
    json TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS signal_kv (
    namespace TEXT NOT NULL,
    key       TEXT NOT NULL,
    value     BLOB NOT NULL,
    PRIMARY KEY (namespace, key)
);
`

// sqliteStore is a Store backed by modernc.org/sqlite (no CGo).
type sqliteStore struct {
	db *sql.DB
}

// OpenSQLite opens (creating if needed) a SQLite database at path and ensures the
// schema exists. The returned Store must be Closed by the caller.
func OpenSQLite(path string) (Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("store: open %q: %w", path, err)
	}
	// modernc SQLite is fine with a small pool; serialize to avoid SQLITE_BUSY.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: create schema: %w", err)
	}
	return &sqliteStore{db: db}, nil
}

func (s *sqliteStore) Close() error { return s.db.Close() }

func (s *sqliteStore) LoadCreds() (*Creds, bool, error) {
	var raw string
	err := s.db.QueryRow(`SELECT json FROM creds WHERE id = 1`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("store: load creds: %w", err)
	}
	var c Creds
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return nil, false, fmt.Errorf("store: unmarshal creds: %w", err)
	}
	return &c, true, nil
}

func (s *sqliteStore) SaveCreds(c *Creds) error {
	if c == nil {
		return errors.New("store: SaveCreds(nil)")
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("store: marshal creds: %w", err)
	}
	_, err = s.db.Exec(
		`INSERT INTO creds (id, json) VALUES (1, ?)
         ON CONFLICT(id) DO UPDATE SET json = excluded.json`,
		string(raw),
	)
	if err != nil {
		return fmt.Errorf("store: save creds: %w", err)
	}
	return nil
}

// --- generic key/value (signal_kv) ---

func (s *sqliteStore) kvGet(ns, key string) ([]byte, bool, error) {
	var v []byte
	err := s.db.QueryRow(`SELECT value FROM signal_kv WHERE namespace = ? AND key = ?`, ns, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("store: kv get %s/%s: %w", ns, key, err)
	}
	return v, true, nil
}

func (s *sqliteStore) kvPut(ns, key string, value []byte) error {
	_, err := s.db.Exec(
		`INSERT INTO signal_kv (namespace, key, value) VALUES (?, ?, ?)
         ON CONFLICT(namespace, key) DO UPDATE SET value = excluded.value`,
		ns, key, value,
	)
	if err != nil {
		return fmt.Errorf("store: kv put %s/%s: %w", ns, key, err)
	}
	return nil
}

func (s *sqliteStore) kvDelete(ns, key string) error {
	_, err := s.db.Exec(`DELETE FROM signal_kv WHERE namespace = ? AND key = ?`, ns, key)
	if err != nil {
		return fmt.Errorf("store: kv delete %s/%s: %w", ns, key, err)
	}
	return nil
}

// KVGet / KVPut expose the generic store for tests and for #3 namespaces not yet
// enumerated. They are not part of the Store interface.
func (s *sqliteStore) KVGet(ns, key string) ([]byte, bool, error) { return s.kvGet(ns, key) }
func (s *sqliteStore) KVPut(ns, key string, value []byte) error   { return s.kvPut(ns, key, value) }

// --- SignalStore (blob stubs; crypto refinement in #3) ---

func idKey(id uint32) string { return fmt.Sprintf("%d", id) }

func (s *sqliteStore) GetSignedPreKey(id uint32) ([]byte, bool, error) {
	return s.kvGet(nsSignedPreKey, idKey(id))
}

func (s *sqliteStore) PutPreKeys(preKeys map[uint32][]byte) error {
	for id, blob := range preKeys {
		if err := s.kvPut(nsPreKey, idKey(id), blob); err != nil {
			return err
		}
	}
	return nil
}

func (s *sqliteStore) StorePreKeys(preKeys map[uint32]StoredKeyPair) error {
	for id, kp := range preKeys {
		blob, err := json.Marshal(kp)
		if err != nil {
			return fmt.Errorf("store: marshal pre-key %d: %w", id, err)
		}
		if err := s.kvPut(nsPreKey, idKey(id), blob); err != nil {
			return err
		}
	}
	return nil
}

func (s *sqliteStore) LoadPreKey(id uint32) (StoredKeyPair, bool, error) {
	return s.loadKeyPair(nsPreKey, id)
}

func (s *sqliteStore) RemovePreKey(id uint32) error {
	return s.kvDelete(nsPreKey, idKey(id))
}

func (s *sqliteStore) StoreSignedPreKey(id uint32, kp StoredKeyPair) error {
	blob, err := json.Marshal(kp)
	if err != nil {
		return fmt.Errorf("store: marshal signed pre-key %d: %w", id, err)
	}
	return s.kvPut(nsSignedPreKey, idKey(id), blob)
}

func (s *sqliteStore) LoadSignedPreKey(id uint32) (StoredKeyPair, bool, error) {
	return s.loadKeyPair(nsSignedPreKey, id)
}

// loadKeyPair fetches and JSON-decodes a StoredKeyPair from a namespace by id.
func (s *sqliteStore) loadKeyPair(ns string, id uint32) (StoredKeyPair, bool, error) {
	blob, ok, err := s.kvGet(ns, idKey(id))
	if err != nil || !ok {
		return StoredKeyPair{}, ok, err
	}
	var kp StoredKeyPair
	if err := json.Unmarshal(blob, &kp); err != nil {
		return StoredKeyPair{}, false, fmt.Errorf("store: unmarshal key pair %s/%d: %w", ns, id, err)
	}
	return kp, true, nil
}

func (s *sqliteStore) LoadSession(addr string) ([]byte, bool, error) {
	return s.kvGet(nsSession, addr)
}

func (s *sqliteStore) StoreSession(addr string, record []byte) error {
	return s.kvPut(nsSession, addr, record)
}

func (s *sqliteStore) LoadIdentity(addr string) ([]byte, bool, error) {
	return s.kvGet(nsIdentity, addr)
}

func (s *sqliteStore) SaveIdentity(addr string, key []byte) error {
	return s.kvPut(nsIdentity, addr, key)
}

// senderKeyName composes the (group, sender) pair into a single namespace key.
// The "::" separator cannot appear inside a JID, so the join is unambiguous.
func senderKeyName(group, sender string) string {
	return group + "::" + sender
}

func (s *sqliteStore) LoadSenderKey(group, sender string) ([]byte, bool, error) {
	return s.kvGet(nsSenderKey, senderKeyName(group, sender))
}

func (s *sqliteStore) StoreSenderKey(group, sender string, record []byte) error {
	return s.kvPut(nsSenderKey, senderKeyName(group, sender), record)
}

// appStateKeyName base64-encodes the binary keyId into an ASCII kv key.
func appStateKeyName(keyID []byte) string {
	return base64.StdEncoding.EncodeToString(keyID)
}

func (s *sqliteStore) StoreAppStateSyncKey(keyID, keyData []byte) error {
	return s.kvPut(nsAppStateKey, appStateKeyName(keyID), keyData)
}

func (s *sqliteStore) LoadAppStateSyncKey(keyID []byte) ([]byte, bool, error) {
	return s.kvGet(nsAppStateKey, appStateKeyName(keyID))
}

// LatestAppStateSyncKey returns the keyId + keyData of a stored app-state sync
// key (the highest key name), or ok=false if none is stored. It lets ensureKey
// recover the key across sessions when the in-memory lastKeyID was lost on
// relogin (the APP_STATE_SYNC_KEY_SHARE arrives once, after pairing).
func (s *sqliteStore) LatestAppStateSyncKey() (keyID, keyData []byte, ok bool, err error) {
	var name string
	var v []byte
	row := s.db.QueryRow(
		`SELECT key, value FROM signal_kv WHERE namespace = ? ORDER BY key DESC LIMIT 1`, nsAppStateKey)
	e := row.Scan(&name, &v)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil, false, nil
	}
	if e != nil {
		return nil, nil, false, fmt.Errorf("store: latest app-state key: %w", e)
	}
	id, derr := base64.StdEncoding.DecodeString(name)
	if derr != nil {
		return nil, nil, false, fmt.Errorf("store: decode app-state keyId %q: %w", name, derr)
	}
	return id, v, true, nil
}

// --- LID <-> PN mapping ---
//
// Mirrors Baileys' LIDMappingStore: each mapping is persisted in both directions
// under the lid_map namespace. The forward key is the bare PN user, the reverse
// key is "<lidUser>_reverse". Keys and values are the numeric user strings (no
// device, no server).

func lidReverseKey(lidUser string) string { return lidUser + "_reverse" }

func (s *sqliteStore) StoreLIDMapping(lidUser, pnUser string) error {
	if lidUser == "" || pnUser == "" {
		return errors.New("store: StoreLIDMapping requires non-empty users")
	}
	if err := s.kvPut(nsLIDMap, pnUser, []byte(lidUser)); err != nil {
		return err
	}
	return s.kvPut(nsLIDMap, lidReverseKey(lidUser), []byte(pnUser))
}

func (s *sqliteStore) LoadPNForLID(lidUser string) (string, bool, error) {
	v, ok, err := s.kvGet(nsLIDMap, lidReverseKey(lidUser))
	if err != nil || !ok {
		return "", ok, err
	}
	return string(v), true, nil
}

func (s *sqliteStore) LoadLIDForPN(pnUser string) (string, bool, error) {
	v, ok, err := s.kvGet(nsLIDMap, pnUser)
	if err != nil || !ok {
		return "", ok, err
	}
	return string(v), true, nil
}
