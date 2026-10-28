package neo

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

var handlePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)

func validateUser(in UserInput, creating bool) error {
	if !handlePattern.MatchString(in.Handle) || len(in.Name) > 200 {
		return ErrInvalid
	}
	switch in.Role {
	case RoleAdmin, RoleOwner, RoleMember, RoleService:
	default:
		return ErrInvalid
	}
	switch in.State {
	case "active", "invited", "suspended":
	default:
		return ErrInvalid
	}
	if (in.Limits.RPM != nil && *in.Limits.RPM <= 0) || (in.Limits.TokensPerDay != nil && *in.Limits.TokensPerDay <= 0) {
		return ErrInvalid
	}
	if in.Password != "" {
		if len(in.Password) < 12 || len(in.Password) > 72 {
			return fmt.Errorf("%w: password must be 12 to 72 bytes", ErrInvalid)
		}
	} else if creating && in.Role != RoleService && in.State == "active" {
		return fmt.Errorf("%w: active human user requires password", ErrInvalid)
	}
	seen := make(map[string]bool, len(in.Grants))
	for _, g := range in.Grants {
		if g.VirtualModel == "" || len(g.VirtualModel) > 128 || seen[g.VirtualModel] || math.IsNaN(g.Allotment) || math.IsInf(g.Allotment, 0) || g.Allotment < 0 || g.Allotment > 1 {
			return ErrInvalid
		}
		seen[g.VirtualModel] = true
	}
	seen = make(map[string]bool, len(in.ProviderAccess))
	for _, p := range in.ProviderAccess {
		if strings.TrimSpace(p) != p || p == "" || len(p) > 100 || seen[p] {
			return ErrInvalid
		}
		seen[p] = true
	}
	return nil
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readUserPolicy(ctx context.Context, q queryer, id string) (User, error) {
	var u User
	var access string
	err := q.QueryRowContext(ctx, `SELECT id,handle,name,role,state,provider_access,rpm,tokens_per_day,created_at FROM neo_users WHERE id=?`, id).Scan(&u.ID, &u.Handle, &u.Name, &u.Role, &u.State, &access, &u.Limits.RPM, &u.Limits.TokensPerDay, &u.CreatedAt)
	if err != nil {
		return u, databaseError(err)
	}
	if err = json.Unmarshal([]byte(access), &u.ProviderAccess); err != nil {
		return u, err
	}
	if u.ProviderAccess == nil {
		u.ProviderAccess = []string{}
	}
	u.Grants = []Grant{}
	u.Keys = []Key{}
	rows, err := q.QueryContext(ctx, "SELECT virtual_model,allotment FROM neo_grants WHERE user_id=? ORDER BY virtual_model", id)
	if err != nil {
		return u, err
	}
	for rows.Next() {
		var g Grant
		if err = rows.Scan(&g.VirtualModel, &g.Allotment); err != nil {
			_ = rows.Close()
			return u, err
		}
		u.Grants = append(u.Grants, g)
	}
	err = rows.Err()
	errClose := rows.Close()
	if err != nil {
		return u, err
	}
	if errClose != nil {
		return u, errClose
	}
	return u, nil
}
func readUser(ctx context.Context, q queryer, id string) (User, error) {
	u, err := readUserPolicy(ctx, q, id)
	if err != nil {
		return u, err
	}
	rows, err := q.QueryContext(ctx, `SELECT id,prefix,label,account_ids,created_at,last_used_at,expires_at FROM neo_keys WHERE user_id=? AND revoked_at IS NULL ORDER BY created_at,id`, id)
	if err != nil {
		return u, err
	}
	defer rows.Close()
	for rows.Next() {
		var k Key
		var scope sql.NullString
		if err = rows.Scan(&k.ID, &k.Prefix, &k.Label, &scope, &k.CreatedAt, &k.LastUsedAt, &k.ExpiresAt); err != nil {
			return u, err
		}
		if scope.Valid {
			if err = json.Unmarshal([]byte(scope.String), &k.AccountIDs); err != nil {
				return u, err
			}
		}
		u.Keys = append(u.Keys, k)
	}
	return u, rows.Err()
}

func (s *Store) User(ctx context.Context, id string) (User, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return User{}, err
	}
	defer rollback(tx)
	u, err := readUser(ctx, tx, id)
	if err != nil {
		return u, err
	}
	return u, tx.Commit()
}
func (s *Store) Users(ctx context.Context) ([]User, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	rows, err := tx.QueryContext(ctx, "SELECT id FROM neo_users ORDER BY created_at,id")
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	users := make([]User, 0, len(ids))
	for _, id := range ids {
		u, errRead := readUser(ctx, tx, id)
		if errRead != nil {
			return nil, errRead
		}
		users = append(users, u)
	}
	return users, tx.Commit()
}
func (s *Store) Grants(ctx context.Context, id string) ([]Grant, error) {
	u, err := s.User(ctx, id)
	return u.Grants, err
}

func setGrants(ctx context.Context, tx *sql.Tx, id string, grants []Grant) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM neo_grants WHERE user_id=?", id); err != nil {
		return err
	}
	for _, g := range grants {
		if _, err := tx.ExecContext(ctx, "INSERT INTO neo_grants(user_id,virtual_model,allotment) VALUES(?,?,?)", id, g.VirtualModel, g.Allotment); err != nil {
			return err
		}
	}
	return nil
}
func insertUser(ctx context.Context, tx *sql.Tx, id string, in UserInput, hash []byte, at int64) error {
	access := in.ProviderAccess
	if access == nil {
		access = []string{}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO neo_users(id,handle,name,role,state,password_hash,provider_access,rpm,tokens_per_day,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, id, in.Handle, in.Name, in.Role, in.State, hash, jsonText(access), in.Limits.RPM, in.Limits.TokensPerDay, at); err != nil {
		return databaseError(err)
	}
	return setGrants(ctx, tx, id, in.Grants)
}
func passwordHash(password string) ([]byte, error) {
	if password == "" {
		return nil, nil
	}
	return bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
}

func (s *Store) CreateUser(ctx context.Context, in UserInput) (User, error) {
	if err := validateUser(in, true); err != nil {
		return User{}, err
	}
	hash, err := passwordHash(in.Password)
	if err != nil {
		return User{}, err
	}
	id, err := randomToken("u_", 12)
	if err != nil {
		return User{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer rollback(tx)
	if err = insertUser(ctx, tx, id, in, hash, s.now().UnixMilli()); err != nil {
		return User{}, err
	}
	u, err := readUser(ctx, tx, id)
	if err != nil {
		return u, err
	}
	return u, s.commitMutation(tx)
}

func protectLastAdmin(ctx context.Context, tx *sql.Tx, u User, role, state string) error {
	if u.Role != RoleAdmin || u.State != "active" || (role == RoleAdmin && state == "active") {
		return nil
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM neo_users WHERE role='admin' AND state='active'").Scan(&count); err != nil {
		return err
	}
	if count <= 1 {
		return fmt.Errorf("%w: cannot remove the last active administrator", ErrForbidden)
	}
	return nil
}
func invalidateAccess(ctx context.Context, tx *sql.Tx, id string, at int64, revokeKeys bool) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM neo_sessions WHERE user_id=?", id); err != nil {
		return err
	}
	if revokeKeys {
		_, err := tx.ExecContext(ctx, "UPDATE neo_keys SET revoked_at=? WHERE user_id=? AND revoked_at IS NULL", at, id)
		return err
	}
	return nil
}
func (s *Store) UpdateUser(ctx context.Context, id string, in UserInput) (User, error) {
	if err := validateUser(in, false); err != nil {
		return User{}, err
	}
	hash, err := passwordHash(in.Password)
	if err != nil {
		return User{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer rollback(tx)
	old, err := readUser(ctx, tx, id)
	if err != nil {
		return User{}, err
	}
	if err = protectLastAdmin(ctx, tx, old, in.Role, in.State); err != nil {
		return User{}, err
	}
	access := in.ProviderAccess
	if access == nil {
		access = []string{}
	}
	_, err = tx.ExecContext(ctx, `UPDATE neo_users SET handle=?,name=?,role=?,state=?,provider_access=?,rpm=?,tokens_per_day=? WHERE id=?`, in.Handle, in.Name, in.Role, in.State, jsonText(access), in.Limits.RPM, in.Limits.TokensPerDay, id)
	if err != nil {
		return User{}, databaseError(err)
	}
	if hash != nil {
		if _, err = tx.ExecContext(ctx, "UPDATE neo_users SET password_hash=? WHERE id=?", hash, id); err != nil {
			return User{}, err
		}
	}
	if hash != nil || old.State != in.State || old.Role != in.Role {
		if err = invalidateAccess(ctx, tx, id, s.now().UnixMilli(), hash != nil); err != nil {
			return User{}, err
		}
	}
	if err = setGrants(ctx, tx, id, in.Grants); err != nil {
		return User{}, err
	}
	u, err := readUser(ctx, tx, id)
	if err != nil {
		return u, err
	}
	return u, s.commitMutation(tx)
}
func (s *Store) DeleteUser(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	u, err := readUser(ctx, tx, id)
	if err != nil {
		return err
	}
	if err = protectLastAdmin(ctx, tx, u, "", ""); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM neo_users WHERE id=?", id); err != nil {
		return databaseError(err)
	}
	return s.commitMutation(tx)
}
func (s *Store) ChangePassword(ctx context.Context, id, current, password string, admin bool) error {
	if len(password) < 12 || len(password) > 72 {
		return ErrInvalid
	}
	hash, err := passwordHash(password)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	var old []byte
	if err = tx.QueryRowContext(ctx, "SELECT password_hash FROM neo_users WHERE id=?", id).Scan(&old); err != nil {
		return databaseError(err)
	}
	if !admin && bcrypt.CompareHashAndPassword(old, []byte(current)) != nil {
		return ErrUnauthenticated
	}
	if _, err = tx.ExecContext(ctx, "UPDATE neo_users SET password_hash=? WHERE id=?", hash, id); err != nil {
		return err
	}
	if err = invalidateAccess(ctx, tx, id, s.now().UnixMilli(), true); err != nil {
		return err
	}
	return s.commitMutation(tx)
}
func principal(u User) Principal {
	return Principal{UserID: u.ID, Role: u.Role, ProviderAccess: u.ProviderAccess, Grants: u.Grants, Limits: u.Limits}
}
func (s *Store) PrincipalForUser(ctx context.Context, id string) (Principal, error) {
	u, err := s.User(ctx, id)
	if err != nil {
		return Principal{}, err
	}
	if u.State != "active" {
		return Principal{}, ErrSuspended
	}
	return principal(u), nil
}
