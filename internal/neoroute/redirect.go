package neoroute

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopool"
)

// Redirect sends requests for one model to another before resolution. From and
// To are what a caller asks for, stored as "neoproxy/<name>" (virtual model) or
// "neoproxy/<provider>/<model>" (direct); From may end in "*" to match every
// asked model with that prefix.
//
// Scope picks who it applies to: Keys (only those inference keys; Users then
// lists their owners), else Users (everyone of theirs, keys and sessions), else
// everyone. For one request the most specific matching redirect wins (key, then
// user, then everyone), then an exact From over a prefix, then the oldest.
// Redirects never chain: the target is resolved as asked, without a second look.
//
// A redirect never grants access by itself: the request is checked as if the
// caller had asked for To. BypassAccess, set by an administrator, is the one
// exception: the target's grant or provider-access check is skipped (account
// ownership and key account scope still apply).
type Redirect struct {
	ID           string        `json:"id"`
	From         string        `json:"from"`
	To           string        `json:"to"`
	Scope        RedirectScope `json:"scope"`
	Enabled      bool          `json:"enabled"`
	BypassAccess bool          `json:"bypassAccess"`
	Note         string        `json:"note"`
	CreatedAt    int64         `json:"createdAt"`
	UpdatedAt    int64         `json:"updatedAt"`
}

type RedirectScope struct {
	Users []string `json:"users,omitempty"`
	Keys  []string `json:"keys,omitempty"`
}

// RedirectRef records the redirect that applied to a decision. From is the
// model the caller asked for; Pattern is the rule's From when it is a prefix.
type RedirectRef struct {
	ID           string `json:"id"`
	From         string `json:"from"`
	To           string `json:"to"`
	Pattern      string `json:"pattern,omitempty"`
	Note         string `json:"note,omitempty"`
	BypassAccess bool   `json:"bypassAccess,omitempty"`
}

const (
	redirectNoteMax  = 280
	redirectScopeMax = 256
)

// canonicalAsked adds the served "neoproxy/" prefix when missing.
func canonicalAsked(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "neoproxy/") {
		return s
	}
	return "neoproxy/" + s
}

func (r Redirect) prefix() (string, bool) { return strings.CutSuffix(r.From, "*") }

func (r Redirect) matches(asked string) bool {
	if p, ok := r.prefix(); ok {
		return strings.HasPrefix(asked, p)
	}
	return asked == r.From
}

// specificity orders scopes: 2 key, 1 user, 0 everyone.
func (r Redirect) specificity() int {
	switch {
	case len(r.Scope.Keys) > 0:
		return 2
	case len(r.Scope.Users) > 0:
		return 1
	}
	return 0
}

func (r Redirect) appliesTo(p neo.Principal) bool {
	switch r.specificity() {
	case 2:
		return p.KeyID != "" && slices.Contains(r.Scope.Keys, p.KeyID)
	case 1:
		return slices.Contains(r.Scope.Users, p.UserID)
	}
	return true
}

// overlaps reports whether some caller could be in both scopes. Key scopes
// carry their owners in Users, so a key scope meets a user scope through them.
func (r Redirect) overlaps(o Redirect) bool {
	a, b := r.specificity(), o.specificity()
	if a == 0 || b == 0 {
		return true
	}
	if a == 2 && b == 2 {
		return slices.ContainsFunc(r.Scope.Keys, func(k string) bool { return slices.Contains(o.Scope.Keys, k) })
	}
	return slices.ContainsFunc(r.Scope.Users, func(u string) bool { return slices.Contains(o.Scope.Users, u) })
}

func (r Redirect) ref(asked string) *RedirectRef {
	ref := &RedirectRef{ID: r.ID, From: asked, To: r.To, Note: r.Note, BypassAccess: r.BypassAccess}
	if _, ok := r.prefix(); ok {
		ref.Pattern = r.From
	}
	return ref
}

// redirectFor picks the redirect for this caller and asked model, if any.
func redirectFor(redirects []Redirect, p neo.Principal, asked string) *Redirect {
	var best *Redirect
	for i := range redirects {
		r := &redirects[i]
		if !r.Enabled || !r.appliesTo(p) || !r.matches(asked) {
			continue
		}
		if best == nil || redirectBefore(*r, *best) {
			best = r
		}
	}
	if best == nil {
		return nil
	}
	out := *best
	return &out
}

func redirectBefore(a, b Redirect) bool {
	if c := cmp.Compare(b.specificity(), a.specificity()); c != 0 {
		return c < 0
	}
	_, ap := a.prefix()
	_, bp := b.prefix()
	if ap != bp {
		return !ap
	}
	if a.CreatedAt != b.CreatedAt {
		return a.CreatedAt < b.CreatedAt
	}
	return a.ID < b.ID
}

// target is the request as resolution sees it: asking for the redirect's To.
func (st routeState) target(req Request) Request {
	if st.redirect != nil {
		req.Asked = st.redirect.To
	}
	return req
}

// allotment is the principal's grant on vm; a bypassing redirect serves an
// ungranted target at full allotment.
func (st routeState) allotment(vm string) (float64, bool) {
	if allot, ok := grantFor(st.principal, vm); ok {
		return allot, true
	}
	if st.bypass {
		return 1, true
	}
	return 0, false
}

func (s *Store) loadRedirects(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT body FROM neoroute_redirects ORDER BY created_at,id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := make([]Redirect, 0)
	for rows.Next() {
		var raw []byte
		var r Redirect
		if err = rows.Scan(&raw); err != nil {
			return err
		}
		if err = json.Unmarshal(raw, &r); err != nil {
			return err
		}
		out = append(out, r)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	s.redirectMu.Lock()
	s.redirects = out
	s.redirectMu.Unlock()
	return nil
}

// Redirects lists every redirect, oldest first.
func (s *Store) Redirects() []Redirect {
	s.redirectMu.RLock()
	defer s.redirectMu.RUnlock()
	out := make([]Redirect, len(s.redirects))
	for i, r := range s.redirects {
		r.Scope = RedirectScope{Users: slices.Clone(r.Scope.Users), Keys: slices.Clone(r.Scope.Keys)}
		out[i] = r
	}
	return out
}

func (s *Store) redirectFor(p neo.Principal, asked string) *Redirect {
	s.redirectMu.RLock()
	defer s.redirectMu.RUnlock()
	return redirectFor(s.redirects, p, asked)
}

func requireAdminSession(ctx context.Context) (neo.Principal, error) {
	p, ok := neo.PrincipalFromContext(ctx)
	if !ok || !p.IsAdmin() {
		return p, fmt.Errorf("organization administrator session required")
	}
	return p, nil
}

// SaveRedirect validates and stores r; create assigns a new id.
func (s *Store) SaveRedirect(ctx context.Context, r Redirect, create bool) (Redirect, error) {
	if _, err := requireAdminSession(ctx); err != nil {
		return Redirect{}, err
	}
	snap, err := s.pools.Snapshot(ctx)
	if err != nil {
		return Redirect{}, err
	}
	vms, err := s.VirtualModels(ctx)
	if err != nil {
		return Redirect{}, err
	}
	users, err := s.identity.Users(ctx)
	if err != nil {
		return Redirect{}, err
	}
	s.redirectMu.Lock()
	defer s.redirectMu.Unlock()
	now := s.now().UnixMilli()
	if create {
		r.ID = "rd_" + uuid.NewString()
		r.CreatedAt = now
	} else {
		i := slices.IndexFunc(s.redirects, func(o Redirect) bool { return o.ID == r.ID })
		if i < 0 {
			return Redirect{}, sql.ErrNoRows
		}
		r.CreatedAt = s.redirects[i].CreatedAt
	}
	r.UpdatedAt = now
	if err = normalizeRedirect(&r, snap, vms, users); err != nil {
		return Redirect{}, err
	}
	if err = checkRedirectConflicts(r, s.redirects); err != nil {
		return Redirect{}, err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return Redirect{}, err
	}
	if create {
		_, err = s.db.ExecContext(ctx, `INSERT INTO neoroute_redirects(id,created_at,body) VALUES(?,?,?)`, r.ID, r.CreatedAt, raw)
	} else {
		_, err = s.db.ExecContext(ctx, `UPDATE neoroute_redirects SET body=? WHERE id=?`, raw, r.ID)
	}
	if err != nil {
		return Redirect{}, err
	}
	next := slices.DeleteFunc(slices.Clone(s.redirects), func(o Redirect) bool { return o.ID == r.ID })
	next = append(next, r)
	slices.SortFunc(next, func(a, b Redirect) int {
		return cmp.Or(cmp.Compare(a.CreatedAt, b.CreatedAt), cmp.Compare(a.ID, b.ID))
	})
	s.redirects = next
	s.notifyRedirects()
	return r, nil
}

func (s *Store) DeleteRedirect(ctx context.Context, id string) error {
	if _, err := requireAdminSession(ctx); err != nil {
		return err
	}
	s.redirectMu.Lock()
	defer s.redirectMu.Unlock()
	result, err := s.db.ExecContext(ctx, `DELETE FROM neoroute_redirects WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	s.redirects = slices.DeleteFunc(slices.Clone(s.redirects), func(o Redirect) bool { return o.ID == id })
	s.notifyRedirects()
	return nil
}

// notifyRedirects wakes queued requests: a redirect can change what they wait for.
func (s *Store) notifyRedirects() {
	s.mu.Lock()
	s.notifyLocked()
	s.mu.Unlock()
}

// normalizeRedirect canonicalizes r in place and checks it against the live
// catalogue and identities.
func normalizeRedirect(r *Redirect, snap neopool.Snapshot, vms []VirtualModel, users []neo.User) error {
	r.From, r.To, r.Note = canonicalAsked(r.From), canonicalAsked(r.To), strings.TrimSpace(r.Note)
	from, prefix := r.prefix()
	switch {
	case strings.TrimPrefix(from, "neoproxy/") == "":
		return fmt.Errorf("from is required (a served model, or a prefix ending in *)")
	case strings.ContainsAny(from, "* \t\r\n"):
		return fmt.Errorf("from may contain * only at the end and no spaces")
	case strings.ContainsAny(r.To, "* \t\r\n"):
		return fmt.Errorf("to must be one served model, without * or spaces")
	case len(r.Note) > redirectNoteMax:
		return fmt.Errorf("note is limited to %d characters", redirectNoteMax)
	}
	target := strings.TrimPrefix(r.To, "neoproxy/")
	if strings.Contains(target, "/") {
		if !slices.ContainsFunc(snap.Models, func(m neopool.Model) bool { return m.ID == target }) {
			return fmt.Errorf("%s is not a model of any configured provider", r.To)
		}
	} else if !slices.ContainsFunc(vms, func(v VirtualModel) bool { return v.Name == target }) {
		return fmt.Errorf("%s is not a virtual model", r.To)
	}
	if r.matches(r.To) {
		if prefix {
			return fmt.Errorf("%s would redirect to itself: it matches %s", r.To, r.From)
		}
		return fmt.Errorf("a model cannot redirect to itself")
	}
	keys := make(map[string]string)
	for _, u := range users {
		for _, k := range u.Keys {
			keys[k.ID] = u.ID
		}
	}
	r.Scope.Users = dedupe(r.Scope.Users)
	r.Scope.Keys = dedupe(r.Scope.Keys)
	if len(r.Scope.Users)+len(r.Scope.Keys) > redirectScopeMax {
		return fmt.Errorf("scope is limited to %d people and keys", redirectScopeMax)
	}
	if len(r.Scope.Keys) > 0 {
		owners := make([]string, 0, len(r.Scope.Keys))
		for _, k := range r.Scope.Keys {
			owner, ok := keys[k]
			if !ok {
				return fmt.Errorf("key %s does not exist or was revoked", k)
			}
			owners = append(owners, owner)
		}
		r.Scope.Users = dedupe(owners)
		return nil
	}
	for _, id := range r.Scope.Users {
		if !slices.ContainsFunc(users, func(u neo.User) bool { return u.ID == id }) {
			return fmt.Errorf("user %s does not exist", id)
		}
	}
	return nil
}

func dedupe(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	out = slices.Compact(out)
	if len(out) == 0 {
		return nil
	}
	return out
}

// checkRedirectConflicts keeps enabled redirects to one hop: no target is the
// source of another redirect that could apply to the same caller, and no two
// rules share a source and scope.
func checkRedirectConflicts(r Redirect, existing []Redirect) error {
	if !r.Enabled {
		return nil
	}
	for _, o := range existing {
		if o.ID == r.ID || !o.Enabled || !r.overlaps(o) {
			continue
		}
		switch {
		case o.matches(r.To):
			return fmt.Errorf("%s is itself redirected to %s by another rule; point this one at %s directly", r.To, o.To, o.To)
		case r.matches(o.To):
			return fmt.Errorf("another rule redirects %s to %s, which this rule would redirect again; redirects do not chain", o.From, o.To)
		case o.From == r.From && slices.Equal(o.Scope.Users, r.Scope.Users) && slices.Equal(o.Scope.Keys, r.Scope.Keys):
			return fmt.Errorf("%s is already redirected to %s for the same people; edit that rule instead", r.From, o.To)
		}
	}
	return nil
}

// redirectedModels adds the models a caller may ask for only through a
// redirect: listed sources whose target the caller can use (or bypasses).
func redirectedModels(listed []string, redirects []Redirect, p neo.Principal, snap neopool.Snapshot, vms []VirtualModel) []string {
	known := make([]string, 0, len(snap.Models)+len(vms))
	for _, vm := range vms {
		known = append(known, vm.Served)
	}
	for _, m := range snap.Models {
		if m.Exposed && !m.VirtualOnly && providerVisible(snap, m.ProviderID) {
			known = append(known, m.Served)
		}
	}
	usable := slices.Clone(listed)
	for _, r := range redirects {
		if !r.Enabled || !r.appliesTo(p) {
			continue
		}
		sources := []string{r.From}
		if _, prefix := r.prefix(); prefix {
			sources = sources[:0]
			for _, k := range known {
				if r.matches(k) {
					sources = append(sources, k)
				}
			}
		}
		for _, from := range sources {
			if slices.Contains(listed, from) {
				continue
			}
			if best := redirectFor(redirects, p, from); best == nil || best.ID != r.ID {
				continue
			}
			if r.BypassAccess || slices.Contains(usable, r.To) {
				listed = append(listed, from)
			}
		}
	}
	return listed
}
