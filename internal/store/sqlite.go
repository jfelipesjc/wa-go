package store

import (
	"database/sql"
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

func (s *sqliteStore) LoadSenderKey(group string) ([]byte, bool, error) {
	return s.kvGet(nsSenderKey, group)
}

func (s *sqliteStore) StoreSenderKey(group string, record []byte) error {
	return s.kvPut(nsSenderKey, group, record)
}
