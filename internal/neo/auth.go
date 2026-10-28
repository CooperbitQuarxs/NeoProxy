package neo

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const SessionLifetime = 12 * time.Hour

var keyPattern = regexp.MustCompile(`^nk_[0-9a-f]{48}$`)

// Valid bcrypt work is also performed for an unknown handle.
const dummyPasswordHash = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"

func (s *Store) Bootstrap(ctx context.Context, handle, name, password string) (Session, string, error) {
	in := UserInput{Handle: handle, Name: name, Password: password, Role: RoleAdmin, State: "active"}
	if err := validateUser(in, true); err != nil {
		return Session{}, "", err
	}
	hash, err := passwordHash(password)
	if err != nil {
		return Session{}, "", err
	}
	id, err := randomToken("u_", 12)
	if err != nil {
		return Session{}, "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, "", err
	}
	defer rollback(tx)
	var exists int
	if err = tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM neo_meta WHERE key='bootstrap')+(SELECT COUNT(*) FROM neo_users WHERE role='admin')`).Scan(&exists); err != nil {
		return Session{}, "", err
	}
	if exists != 0 {
		return Session{}, "", ErrConflict
	}
	if err = insertUser(ctx, tx, id, in, hash, s.now().UnixMilli()); err != nil {
		return Session{}, "", err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO neo_meta(key,value) VALUES('bootstrap','complete')"); err != nil {
		return Session{}, "", err
	}
	session, token, err := s.createSession(ctx, tx, id, handle, RoleAdmin)
	if err != nil {
		return Session{}, "", err
	}
	if err = s.commitMutation(tx); err != nil {
		return Session{}, "", err
	}
	return session, token, nil
}
func (s *Store) createSession(ctx context.Context, tx *sql.Tx, id, handle, role string) (Session, string, error) {
	token, err := randomToken("", 32)
	if err != nil {
		return Session{}, "", err
	}
	at := s.now()
	expires := at.Add(SessionLifetime).UnixMilli()
	if _, err = tx.ExecContext(ctx, `INSERT INTO neo_sessions(token_hash,user_id,expires_at,created_at) VALUES(?,?,?,?)`, hashToken(token), id, expires, at.UnixMilli()); err != nil {
		return Session{}, "", err
	}
	// Remove expired sessions opportunistically; no background clock-dependent task.
	if _, err = tx.ExecContext(ctx, "DELETE FROM neo_sessions WHERE expires_at<=?", at.UnixMilli()); err != nil {
		return Session{}, "", err
	}
	if role == RoleMember {
		role = "user"
	}
	return Session{Role: role, UserID: id, Handle: handle, ExpiresAt: expires}, token, nil
}
func (s *Store) Login(ctx context.Context, handle, password string) (Session, string, error) {
	if len(handle) > 32 || len(password) > 72 {
		return Session{}, "", ErrUnauthenticated
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, "", err
	}
	defer rollback(tx)
	var id, role, state string
	var hash []byte
	err = tx.QueryRowContext(ctx, "SELECT id,role,state,password_hash FROM neo_users WHERE handle=?", handle).Scan(&id, &role, &state, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		_ = bcrypt.CompareHashAndPassword([]byte(dummyPasswordHash), []byte(password))
		return Session{}, "", ErrUnauthenticated
	}
	if err != nil {
		return Session{}, "", err
	}
	if len(hash) == 0 {
		_ = bcrypt.CompareHashAndPassword([]byte(dummyPasswordHash), []byte(password))
		return Session{}, "", ErrUnauthenticated
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(password)) != nil {
		return Session{}, "", ErrUnauthenticated
	}
	if state != "active" {
		return Session{}, "", ErrSuspended
	}
	if role == RoleService {
		return Session{}, "", ErrForbidden
	}
	session, token, err := s.createSession(ctx, tx, id, handle, role)
	if err != nil {
		return session, "", err
	}
	if err = tx.Commit(); err != nil {
		return Session{}, "", err
	}
	return session, token, nil
}
func (s *Store) Session(ctx context.Context, token string) (Session, Principal, error) {
	if len(token) != 64 {
		return Session{}, Principal{}, ErrUnauthenticated
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Session{}, Principal{}, err
	}
	defer rollback(tx)
	var id string
	var expires int64
	err = tx.QueryRowContext(ctx, "SELECT user_id,expires_at FROM neo_sessions WHERE token_hash=?", hashToken(token)).Scan(&id, &expires)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && expires <= s.now().UnixMilli()) {
		return Session{}, Principal{}, ErrUnauthenticated
	}
	if err != nil {
		return Session{}, Principal{}, err
	}
	u, err := readUserPolicy(ctx, tx, id)
	if err != nil {
		return Session{}, Principal{}, err
	}
	if u.State != "active" || u.Role == RoleService {
		return Session{}, Principal{}, ErrSuspended
	}
	p := principal(u)
	p.Session = true
	role := u.Role
	if role == RoleMember {
		role = "user"
	}
	if err = tx.Commit(); err != nil {
		return Session{}, Principal{}, err
	}
	return Session{Role: role, UserID: id, Handle: u.Handle, ExpiresAt: expires}, p, nil
}
func (s *Store) Logout(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM neo_sessions WHERE token_hash=?", hashToken(token))
	return err
}

func (s *Store) MintKey(ctx context.Context, actor Principal, userID, label string, accountIDs []string, expiresAt *int64) (Key, string, error) {
	if !actor.Session || !actor.CanAccessUser(userID) {
		return Key{}, "", ErrForbidden
	}
	if label == "" || len(label) > 200 || (accountIDs != nil && len(accountIDs) == 0) || (expiresAt != nil && *expiresAt <= s.now().UnixMilli()) {
		return Key{}, "", ErrInvalid
	}
	seen := make(map[string]bool, len(accountIDs))
	for _, id := range accountIDs {
		if id == "" || seen[id] {
			return Key{}, "", ErrInvalid
		}
		seen[id] = true
	}
	u, err := s.User(ctx, userID)
	if err != nil {
		return Key{}, "", err
	}
	if u.State == "suspended" {
		return Key{}, "", ErrSuspended
	}
	if u.Role == RoleOwner {
		if err = s.validateScope(ctx, userID, accountIDs); err != nil {
			return Key{}, "", err
		}
	}
	secret, err := randomToken("nk_", 24)
	if err != nil {
		return Key{}, "", err
	}
	id, err := randomToken("key_", 12)
	if err != nil {
		return Key{}, "", err
	}
	k := Key{ID: id, Prefix: secret[:7] + "…" + secret[len(secret)-4:], Label: label, AccountIDs: accountIDs, CreatedAt: s.now().UnixMilli(), ExpiresAt: expiresAt}
	var scope any
	if accountIDs != nil {
		scope = jsonText(accountIDs)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Key{}, "", err
	}
	defer rollback(tx)
	// Re-read state and policy under the same writer transaction as key creation.
	current, err := readUser(ctx, tx, userID)
	if err != nil {
		return Key{}, "", err
	}
	if current.Role != u.Role || current.State != u.State {
		return Key{}, "", ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO neo_keys(id,user_id,token_hash,prefix,label,account_ids,created_at,expires_at) VALUES(?,?,?,?,?,?,?,?)`, id, userID, hashToken(secret), k.Prefix, label, scope, k.CreatedAt, expiresAt); err != nil {
		return Key{}, "", databaseError(err)
	}
	if current.State == "invited" {
		if _, err = tx.ExecContext(ctx, "UPDATE neo_users SET state='active' WHERE id=?", userID); err != nil {
			return Key{}, "", err
		}
	}
	if err = s.commitMutation(tx); err != nil {
		return Key{}, "", err
	}
	return k, secret, nil
}
func (s *Store) RevokeKey(ctx context.Context, actor Principal, userID, keyID string) error {
	if !actor.Session || !actor.CanAccessUser(userID) {
		return ErrForbidden
	}
	result, err := s.db.ExecContext(ctx, "UPDATE neo_keys SET revoked_at=? WHERE id=? AND user_id=? AND revoked_at IS NULL", s.now().UnixMilli(), keyID, userID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	s.NotifyChanges()
	return nil
}
func (s *Store) AuthenticateKey(ctx context.Context, secret string) (Principal, error) {
	if !keyPattern.MatchString(secret) {
		return Principal{}, ErrUnauthenticated
	}
	var id, userID string
	err := s.db.QueryRowContext(ctx, "SELECT id,user_id FROM neo_keys WHERE token_hash=?", hashToken(secret)).Scan(&id, &userID)
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, ErrUnauthenticated
	}
	if err != nil {
		return Principal{}, err
	}
	p, err := s.RefreshPrincipal(ctx, Principal{UserID: userID, KeyID: id})
	if err != nil {
		return Principal{}, err
	}
	if _, err = s.db.ExecContext(ctx, "UPDATE neo_keys SET last_used_at=? WHERE id=?", s.now().UnixMilli(), id); err != nil {
		return Principal{}, err
	}
	return p, nil
}

// RefreshPrincipal rechecks current grants, limits, state, expiry and revocation.
// Queued inference requests call this before each reservation attempt.
func (s *Store) RefreshPrincipal(ctx context.Context, p Principal) (Principal, error) {
	if p.KeyID == "" {
		return Principal{}, fmt.Errorf("%w: inference key required", ErrUnauthenticated)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Principal{}, err
	}
	defer rollback(tx)
	var userID string
	var scope sql.NullString
	var expires, revoked sql.NullInt64
	err = tx.QueryRowContext(ctx, "SELECT user_id,account_ids,expires_at,revoked_at FROM neo_keys WHERE id=?", p.KeyID).Scan(&userID, &scope, &expires, &revoked)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (userID != p.UserID || revoked.Valid || (expires.Valid && expires.Int64 <= s.now().UnixMilli()))) {
		return Principal{}, ErrUnauthenticated
	}
	if err != nil {
		return Principal{}, err
	}
	u, err := readUserPolicy(ctx, tx, userID)
	if err != nil {
		return Principal{}, err
	}
	if u.State != "active" {
		return Principal{}, ErrSuspended
	}
	next := principal(u)
	next.KeyID = p.KeyID
	if scope.Valid {
		if err = json.Unmarshal([]byte(scope.String), &next.AccountIDs); err != nil {
			return Principal{}, err
		}
		if len(next.AccountIDs) == 0 {
			return Principal{}, ErrForbidden
		}
	}
	if err = tx.Commit(); err != nil {
		return Principal{}, err
	}
	if next.Role == RoleOwner {
		if err = s.validateScope(ctx, userID, next.AccountIDs); err != nil {
			return Principal{}, err
		}
	}
	return next, nil
}
