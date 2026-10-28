package neo

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	sqlite3 "github.com/mattn/go-sqlite3"
)

// Store is shared by identity, pooled accounts and routing. Extension migrations
// must use their own version tables; do not modify PRAGMA user_version.
type Store struct {
	db             *sql.DB
	now            func() time.Time
	scopeMu        sync.RWMutex
	scopeValidator AccountScopeValidator
	changesMu      sync.Mutex
	changes        chan struct{}
}

// Open creates a private SQLite file. No URI or client-provided DSN is accepted.
// One connection and IMMEDIATE transactions serialize mutations deterministically.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("%w: empty database path", ErrInvalid)
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve database path: %w", err)
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	if fi, errStat := os.Lstat(path); errStat == nil && (!fi.Mode().IsRegular() || fi.Mode()&os.ModeSymlink != 0) {
		return nil, fmt.Errorf("database must be a regular file")
	} else if errStat != nil && !os.IsNotExist(errStat) {
		return nil, errStat
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("create database: %w", err)
	}
	if err = f.Chmod(0600); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	dsn := (&url.URL{Scheme: "file", Path: path}).String() + "?_foreign_keys=on&_journal_mode=WAL&_synchronous=FULL&_busy_timeout=5000&_txlock=immediate&_secure_delete=on"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db, now: time.Now, changes: make(chan struct{})}
	if err = s.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) DB() *sql.DB  { return s.db }
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) SetAccountScopeValidator(v AccountScopeValidator) {
	s.scopeMu.Lock()
	s.scopeValidator = v
	s.scopeMu.Unlock()
}
func (s *Store) validateScope(ctx context.Context, id string, ids []string) error {
	s.scopeMu.RLock()
	v := s.scopeValidator
	s.scopeMu.RUnlock()
	if v == nil || len(ids) == 0 {
		return ErrForbidden
	}
	return v(ctx, id, ids)
}

// Changes broadcasts committed identity mutations. Capture it before resolving
// eligibility, then re-read current authority after waking.
func (s *Store) Changes() <-chan struct{} {
	s.changesMu.Lock()
	defer s.changesMu.Unlock()
	if s.changes == nil {
		s.changes = make(chan struct{})
	}
	return s.changes
}

// NotifyChanges lets extensions signal an authority change after their commit.
func (s *Store) NotifyChanges() {
	s.changesMu.Lock()
	defer s.changesMu.Unlock()
	if s.changes != nil {
		close(s.changes)
	}
	s.changes = make(chan struct{})
}
func (s *Store) commitMutation(tx *sql.Tx) error {
	if err := tx.Commit(); err != nil {
		return err
	}
	s.NotifyChanges()
	return nil
}

func (s *Store) migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	var version int
	if err = tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > 1 {
		return fmt.Errorf("unsupported identity schema version %d", version)
	}
	if version == 0 {
		_, err = tx.ExecContext(ctx, `
CREATE TABLE neo_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE neo_users (
 id TEXT PRIMARY KEY, handle TEXT NOT NULL UNIQUE, name TEXT NOT NULL,
 role TEXT NOT NULL CHECK(role IN ('admin','owner','member','service')),
 state TEXT NOT NULL CHECK(state IN ('active','invited','suspended')),
 password_hash BLOB, provider_access TEXT NOT NULL DEFAULT '[]',
 rpm INTEGER, tokens_per_day INTEGER, created_at INTEGER NOT NULL
);
CREATE TABLE neo_grants (
 user_id TEXT NOT NULL REFERENCES neo_users(id) ON DELETE CASCADE,
 virtual_model TEXT NOT NULL, allotment REAL NOT NULL CHECK(allotment>=0 AND allotment<=1),
 PRIMARY KEY(user_id,virtual_model)
);
CREATE TABLE neo_sessions (
 token_hash TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES neo_users(id) ON DELETE CASCADE,
 expires_at INTEGER NOT NULL, created_at INTEGER NOT NULL
);
CREATE INDEX neo_sessions_user ON neo_sessions(user_id);
CREATE TABLE neo_keys (
 id TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES neo_users(id) ON DELETE CASCADE,
 token_hash TEXT NOT NULL UNIQUE, prefix TEXT NOT NULL, label TEXT NOT NULL,
 account_ids TEXT, created_at INTEGER NOT NULL, last_used_at INTEGER,
 expires_at INTEGER, revoked_at INTEGER
);
CREATE INDEX neo_keys_user ON neo_keys(user_id);
PRAGMA user_version=1;`)
		if err != nil {
			return fmt.Errorf("migrate identity schema: %w", err)
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("database foreign-key check failed")
	}
	return rows.Err()
}

func rollback(tx *sql.Tx) { _ = tx.Rollback() }
func randomToken(prefix string, n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b), nil
}
func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}
func jsonText(v any) string { b, _ := json.Marshal(v); return string(b) }
func databaseError(err error) error {
	var e sqlite3.Error
	if errors.As(err, &e) && e.Code == sqlite3.ErrConstraint {
		return ErrConflict
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
