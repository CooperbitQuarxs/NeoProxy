package neoroute

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopool"
)

func accountAllowed(p neo.Principal, a neopool.Account) bool {
	if a.OwnerID == "" {
		return false
	}
	if p.Role == "owner" && a.OwnerID != p.UserID {
		return false
	}
	return p.AllowsAccount(a.ID)
}
func accountSupports(a neopool.Account, model string) bool { return slices.Contains(a.ModelIDs, model) }
func providerVisible(snap neopool.Snapshot, id string) bool {
	for _, p := range snap.Providers {
		if p.ID == id {
			return p.Exposed
		}
	}
	return false
}
func exposedModel(snap neopool.Snapshot, id string) (neopool.Model, bool) {
	for _, m := range snap.Models {
		if m.ID == id {
			return m, m.Exposed && providerVisible(snap, m.ProviderID)
		}
	}
	return neopool.Model{}, false
}

// localWindow is the account's current configured token meter of that kind.
func localWindow(a neopool.Account, kind string, now int64) (neopool.Window, bool) {
	for _, w := range a.Windows {
		if w.Kind == kind && w.Known && w.Capacity > 0 && w.StartsAt <= now && w.ResetsAt > now {
			return w, true
		}
	}
	return neopool.Window{}, false
}

// windowFor returns the meter a stage of that window kind reads for the account:
// the provider-reported window when one is known and current, else the local
// configured meter. metered is false when the account has neither, i.e. it is
// unmetered and always eligible. provider tells which of the two was returned.
func windowFor(a neopool.Account, kind string, now int64) (w neopool.Window, provider, metered bool) {
	for _, pw := range a.ProviderWindows {
		if pw.Kind == kind && pw.Known && (pw.ResetsAt == 0 || pw.ResetsAt > now) {
			return pw, true, true
		}
	}
	w, metered = localWindow(a, kind, now)
	return w, false, metered
}
func accountByID(snap neopool.Snapshot, id string) (neopool.Account, bool) {
	for _, a := range snap.Accounts {
		if a.ID == id {
			return a, true
		}
	}
	return neopool.Account{}, false
}
func grantFor(p neo.Principal, vm string) (float64, bool) {
	for _, g := range p.Grants {
		if g.VirtualModel == vm && finiteFraction(g.Allotment) {
			return g.Allotment, true
		}
	}
	return 0, false
}

func (s *Store) refresh(ctx context.Context, p neo.Principal) (neo.Principal, error) {
	if p.KeyID != "" {
		next, err := s.identity.RefreshPrincipal(ctx, p)
		return next, authorityError(err)
	}
	next, err := s.identity.PrincipalForUser(ctx, p.UserID)
	if err != nil {
		return neo.Principal{}, authorityError(err)
	}
	next.Session = p.Session
	next.AccountIDs = p.AccountIDs
	return next, nil
}

func authorityError(err error) error {
	if err == nil {
		return nil
	}
	status, code, message := 0, "", ""
	switch {
	case errors.Is(err, neo.ErrSuspended):
		status, code, message = 403, "neoproxy_suspended", "User is suspended"
	case errors.Is(err, neo.ErrUnauthenticated), errors.Is(err, neo.ErrNotFound):
		status, code, message = 401, "neoproxy_unauthorized", "Inference key is no longer authorized"
	case errors.Is(err, neo.ErrForbidden), errors.Is(err, neopool.ErrForbidden):
		status, code, message = 403, "neoproxy_forbidden", "Account scope is no longer authorized"
	default:
		return err
	}
	return &Error{Status: status, Code: code, Decision: Decision{Outcome: "error", Reason: code, Message: message, Trail: []TrailStep{}}}
}

type routeState struct {
	principal neo.Principal
	handle    string
	snapshot  neopool.Snapshot
	usage     []neopool.Usage
	vm        *VirtualModel
	cursors   map[string]int64
	blocked   map[string]map[string]bool
	at        int64
	now       time.Time
	// Draw inputs: memory and rng for the stateful and random draws, history
	// (auto) and prices (cheapest) loaded only when a stage draws with them.
	memory  *drawMemory
	rng     drawRand
	history map[string]accountHistory
	prices  map[string]neopool.PlanPrice
	// redirect is the redirect applied to the asked model (resolution then
	// sees its To); bypass skips the target's grant/access check for it.
	redirect *Redirect
	bypass   bool
}

func (s *Store) state(ctx context.Context, asked string) (routeState, error) {
	p, ok := neo.PrincipalFromContext(ctx)
	if !ok {
		return routeState{}, &Error{Status: 401, Code: "neoproxy_unauthorized", Decision: Decision{Outcome: "error", Reason: "unauthorized", Trail: []TrailStep{}}}
	}
	refreshed, err := s.refresh(ctx, p)
	if err != nil {
		return routeState{}, err
	}
	p = refreshed
	snap, err := s.pools.Snapshot(ctx)
	if err != nil {
		return routeState{}, err
	}
	now := s.now()
	state := routeState{principal: p, snapshot: snap, at: now.UnixMilli(), now: now, cursors: make(map[string]int64), memory: s.memory, rng: s.rng}
	if r := s.redirectFor(p, asked); r != nil {
		state.redirect, state.bypass, asked = r, r.BypassAccess, r.To
	}
	selector := strings.TrimPrefix(asked, "neoproxy/")
	if strings.HasPrefix(asked, "neoproxy/") && !strings.Contains(selector, "/") {
		vm, errVM := s.VirtualModel(ctx, selector)
		if errVM != nil && !errors.Is(errVM, sql.ErrNoRows) {
			return state, errVM
		}
		if errVM == nil {
			state.vm = &vm
			for _, st := range vm.Stages {
				n, errCursor := s.cursor(ctx, vm.ID, st.ID)
				if errCursor != nil {
					return state, errCursor
				}
				state.cursors[st.ID] = n
			}
		}
	}
	if state.vm != nil {
		state.usage, err = s.pools.Usage(ctx, "")
		if err != nil {
			return state, err
		}
		if stagesUse(state.vm, "auto") {
			if state.history, err = s.drawHistory(ctx); err != nil {
				return state, err
			}
		}
		if stagesUse(state.vm, "cheapest") {
			if state.prices, err = s.planPrices(ctx); err != nil {
				return state, err
			}
		}
		// Conditions and scripts may read the handle; resolve it only for them.
		if state.vm.Script != "" || slices.ContainsFunc(state.vm.Stages, func(st Stage) bool { return st.Condition != nil }) {
			user, errUser := s.identity.User(ctx, p.UserID)
			if errUser != nil {
				return state, authorityError(errUser)
			}
			state.handle = user.Handle
		}
	}
	return state, nil
}

func directDecision(asked, model string, state routeState, vm string, trail []TrailStep, checkAccess bool) Decision {
	d := Decision{Outcome: "error", VirtualModel: vm, Trail: trail}
	m, ok := exposedModel(state.snapshot, model)
	if !ok {
		d.Reason = "unknown-model"
		return d
	}
	if m.VirtualOnly {
		d.Reason = "no-provider-access"
		d.Message = "This model is available only through a metered virtual-model stage"
		return d
	}
	if checkAccess && !state.bypass && !slices.Contains(state.principal.ProviderAccess, m.ProviderID) {
		d.Reason = "no-provider-access"
		d.Trail = append(d.Trail, TrailStep{Kind: "denied", ProviderID: m.ProviderID})
		return d
	}
	if checkAccess && !state.bypass && !state.principal.AllowsDirect(m.ProviderID, m.ID) {
		d.Reason = "no-model-access"
		d.Message = "This model is not in the direct models allowed for this person or key"
		d.Trail = append(d.Trail, TrailStep{Kind: "denied", ProviderID: m.ProviderID, Model: m.ID})
		return d
	}
	for _, a := range state.snapshot.Accounts {
		if a.ProviderID == m.ProviderID && a.State == "healthy" && accountAllowed(state.principal, a) && accountSupports(a, model) {
			d.Outcome = "ok"
			d.Model = model
			d.AccountID = a.ID
			d.Direct = checkAccess
			if checkAccess {
				d.Trail = append(d.Trail, TrailStep{Kind: "direct", Model: model, AccountID: a.ID})
			}
			return d
		}
	}
	d.Reason = "no-healthy-account"
	return d
}

// stageUsage is the account's used fraction of its meter w for stage st:
// the provider-reported value as is; for a local meter the account-wide tokens
// (every user, stage and direct call, reservations included) when Per is "all",
// or this user's own ledger usage of the stage when Per is "user".
func stageUsage(state routeState, vm string, st Stage, a neopool.Account, w neopool.Window, provider bool) float64 {
	if provider {
		return w.Used
	}
	if st.Per == "all" {
		return float64(w.UsedTokens) / float64(w.Capacity)
	}
	total := float64(0)
	for _, u := range state.usage {
		if u.VirtualModelID == vm && u.StageID == st.ID && u.AccountID == a.ID && u.Window == st.Window && u.StartsAt == w.StartsAt && u.ResetsAt == w.ResetsAt && u.UserID == state.principal.UserID {
			total += float64(u.Tokens+u.Reserved) / float64(w.Capacity)
		}
	}
	return total
}

// inStageScope reports whether the stage may consider account a at all: its
// model's provider, the principal's ownership/key scope and the stage's list.
func inStageScope(state routeState, st Stage, m neopool.Model, a neopool.Account) bool {
	return a.ProviderID == m.ProviderID && accountAllowed(state.principal, a) && (st.Accounts.All || slices.Contains(st.Accounts.IDs, a.ID))
}

// stageAccounts lists the accounts a stage considers in its order: the
// explicit list's order, or the snapshot's (account ID) order for "all".
func stageAccounts(snap neopool.Snapshot, st Stage) []neopool.Account {
	if st.Accounts.All {
		return snap.Accounts
	}
	out := make([]neopool.Account, 0, len(st.Accounts.IDs))
	for _, id := range st.Accounts.IDs {
		if a, ok := accountByID(snap, id); ok {
			out = append(out, a)
		}
	}
	return out
}
func evaluateStage(state routeState, vm VirtualModel, st Stage, index int, allot float64, facts *requestFacts) (TrailStep, []neopool.Account, int64) {
	trail := TrailStep{Kind: "stage", Index: index, StageID: st.ID, Model: st.Model, Window: st.Window, Cap: st.Cap * allot, Draw: canonicalDraw(st.Draw), Candidates: []Candidate{}}
	m, visible := exposedModel(state.snapshot, st.Model)
	if visible && !fitsContext(st, m, facts) {
		trail.Skipped = "context"
		return trail, nil, 0
	}
	var candidates []neopool.Account
	unhealthy := !visible
	var reset int64
	var sum float64
	for _, a := range stageAccounts(state.snapshot, st) {
		if !inStageScope(state, st, m, a) {
			continue
		}
		w, provider, metered := windowFor(a, st.Window, state.at)
		if a.CooldownUntil > state.at && (reset == 0 || a.CooldownUntil < reset) {
			reset = a.CooldownUntil
		}
		// An unmetered account has nothing to run out of: used stays 0, it never
		// enters the cap average and contributes no reset time.
		used := float64(0)
		if metered {
			used = stageUsage(state, vm.ID, st, a, w, provider)
			sum += used
			trail.Metered++
			if w.ResetsAt > 0 && (reset == 0 || w.ResetsAt < reset) {
				reset = w.ResetsAt
			}
		}
		exhausted := metered && !provider && w.UsedTokens >= w.Capacity
		if a.State != "healthy" || !visible || !accountSupports(a, st.Model) || exhausted {
			unhealthy = true
			continue
		}
		budget := st.accountLimit(a.ID) * allot
		if used < budget && !state.blocked[st.ID][a.ID] {
			candidates = append(candidates, a)
			trail.Candidates = append(trail.Candidates, Candidate{AccountID: a.ID, Window: st.Window, Metered: metered, Used: used, Budget: budget})
		}
	}
	if trail.Metered > 0 {
		trail.Total = sum / float64(trail.Metered)
	}
	var scope *conditionScope
	if st.Condition != nil {
		pool := &poolFacts{size: len(candidates)}
		if i := windowIndex(st.Window); i >= 0 {
			pool.known[i] = 1
			if trail.Metered > 0 {
				pool.average[i], pool.known[i] = trail.Total, 2
			}
		}
		scope = &conditionScope{facts: facts, state: &state, vm: vm.ID, stage: &st, model: m, pool: pool}
		if !st.Condition.usesAccount() && !st.Condition.eval(scope) {
			trail.Skipped = "condition"
			trail.Candidates = []Candidate{}
			return trail, nil, reset
		}
	}
	if trail.Total >= trail.Cap {
		trail.Skipped = "cap"
		trail.Candidates = []Candidate{}
		return trail, nil, reset
	}
	if scope != nil && st.Condition.usesAccount() && len(candidates) > 0 {
		// Both slices were freshly built above; compact them in place.
		kept := candidates[:0]
		listed := trail.Candidates[:0]
		var excluded []Candidate
		for i := range candidates {
			scope.account = &candidates[i]
			if st.Condition.eval(scope) {
				kept = append(kept, candidates[i])
				listed = append(listed, trail.Candidates[i])
				continue
			}
			candidate := trail.Candidates[i]
			candidate.Reason = "condition"
			excluded = append(excluded, candidate)
		}
		candidates, trail.Candidates, trail.excluded = kept, listed, excluded
		if len(candidates) == 0 {
			trail.Skipped = "condition"
			return trail, nil, reset
		}
	}
	if len(candidates) == 0 {
		trail.Skipped = "per-account"
		if unhealthy {
			trail.Skipped = "unhealthy"
		}
		return trail, nil, reset
	}
	return trail, candidates, reset
}

// explained lists the condition-excluded accounts after the stage's candidates
// unless the stage was skipped for a reason that hides every account.
func explained(tr TrailStep) TrailStep {
	switch tr.Skipped {
	case "cap", "lua-cap", "upstream-error":
	default:
		tr.Candidates = append(tr.Candidates, tr.excluded...)
	}
	tr.excluded = nil
	return tr
}

// decide is the canonical pure policy shared by simulation and execution. The
// excluded prefix represents real failed attempts, not fabricated health changes.
// A redirect resolves its target as if asked for, after a leading trail step.
func decide(req Request, state routeState, after int, proposal *ScriptProposal) Decision {
	if state.redirect == nil {
		return resolveAsked(req, state, after, proposal)
	}
	ref := state.redirect.ref(req.Asked)
	d := resolveAsked(state.target(req), state, after, proposal)
	d.Redirect = ref
	d.Trail = append([]TrailStep{{Kind: "redirect", Asked: req.Asked, Redirect: ref}}, d.Trail...)
	return d
}

func resolveAsked(req Request, state routeState, after int, proposal *ScriptProposal) Decision {
	trail := make([]TrailStep, 0)
	if !strings.HasPrefix(req.Asked, "neoproxy/") {
		return Decision{Outcome: "error", Reason: "unknown-model", Trail: []TrailStep{{Kind: "no-model", Asked: req.Asked}}}
	}
	selector := strings.TrimPrefix(req.Asked, "neoproxy/")
	if strings.Contains(selector, "/") {
		return directDecision(req.Asked, selector, state, "", trail, true)
	}
	if state.vm == nil {
		return Decision{Outcome: "error", Reason: "unknown-model", Trail: []TrailStep{{Kind: "no-model", Asked: req.Asked}}}
	}
	vm := *state.vm
	allot, granted := state.allotment(vm.ID)
	if !granted {
		return Decision{Outcome: "error", VirtualModel: vm.ID, Reason: "not-granted", Trail: []TrailStep{{Kind: "denied", VirtualModel: vm.ID}}}
	}
	var resetsAt int64
	facts := newRequestFacts(req, state)
	for index, st := range vm.Stages {
		tr, candidates, reset := evaluateStage(state, vm, st, index, allot, facts)
		tr, candidates = applyPolicy(tr, candidates, st, allot, proposal)
		if reset > 0 && (resetsAt == 0 || reset < resetsAt) {
			resetsAt = reset
		}
		if index <= after {
			tr.Skipped = "upstream-error"
			tr.Candidates = nil
			trail = append(trail, explained(tr))
			continue
		}
		if tr.Skipped != "" {
			trail = append(trail, explained(tr))
			continue
		}
		var chosen int
		if proposal != nil && proposal.StageID != "" {
			if proposal.StageID != st.ID {
				tr.Skipped = "script"
				trail = append(trail, explained(tr))
				continue
			}
			chosen = -1
			for i, a := range candidates {
				if a.ID == proposal.AccountID {
					chosen = i
					break
				}
			}
			if chosen < 0 {
				return Decision{Outcome: "error", VirtualModel: vm.ID, Reason: "invalid-script-proposal", Trail: trail}
			}
			tr.DrawNote = "chosen by the routing script"
		} else {
			chosen, tr.DrawNote = drawCandidate(state, vm.ID, st, candidates, tr.Candidates, facts)
		}
		tr.Chosen = candidates[chosen].ID
		trail = append(trail, explained(tr))
		return Decision{Outcome: "ok", VirtualModel: vm.ID, StageIndex: &index, StageID: st.ID, Model: st.Model, AccountID: candidates[chosen].ID, Effort: stageEffort(st, facts.effort), Trail: trail}
	}
	if proposal != nil && proposal.StageID != "" {
		return Decision{Outcome: "error", VirtualModel: vm.ID, Reason: "invalid-script-proposal", Trail: trail}
	}
	trail = append(trail, TrailStep{Kind: "terminal", Terminal: &vm.Terminal})
	switch vm.Terminal.Kind {
	case "passthrough":
		return directDecision(req.Asked, vm.Terminal.Model, state, vm.ID, trail, false)
	case "queue":
		return Decision{Outcome: "queued", VirtualModel: vm.ID, Reason: "queue", Message: vm.Terminal.Message, ResetsAt: resetsAt, Trail: trail}
	default:
		return Decision{Outcome: "limited", VirtualModel: vm.ID, Reason: "limit", Message: vm.Terminal.Message, ResetsAt: resetsAt, Trail: trail}
	}
}
func (s *Store) choose(ctx context.Context, req Request, state routeState, after int) (Decision, routeState, error) {
	var proposal *ScriptProposal
	if state.vm != nil && state.vm.Script != "" {
		allot, ok := state.allotment(state.vm.ID)
		if !ok {
			return decide(req, state, after, nil), state, nil
		}
		hook := s.scriptHook()
		if hook == nil {
			return Decision{}, state, scriptError(state.vm.ID, "script evaluator unavailable")
		}
		if len(req.Body) > ScriptMaxInput {
			return Decision{}, state, scriptError(state.vm.ID, "routing script input exceeds 256 KiB")
		}
		asked := state.target(req)
		facts := newRequestFacts(asked, state)
		input := ScriptInput{Source: state.vm.Script, Asked: asked.Asked, Protocol: req.Protocol, UserID: state.principal.UserID, VirtualModel: *state.vm, Stages: make([]ScriptStage, 0, len(state.vm.Stages)), Effort: facts.effort}
		input.User = ScriptUser{ID: state.principal.UserID, Handle: state.handle, Role: state.principal.Role}
		input.Time = ScriptTime{Weekday: strings.ToLower(facts.now.Weekday().String()), Hour: facts.now.Hour(), Date: facts.now.Format(time.DateOnly)}
		input.Request = append(json.RawMessage(nil), req.Body...)
		for i, st := range state.vm.Stages {
			tr, _, _ := evaluateStage(state, *state.vm, st, i, allot, facts)
			if i <= after {
				tr.Skipped = "upstream-error"
				tr.Candidates = nil
			}
			input.Stages = append(input.Stages, ScriptStage{StageID: st.ID, Model: st.Model, Candidates: tr.Candidates, Total: tr.Total, Cap: tr.Cap, Skipped: tr.Skipped, Lua: st.Lua, Allotment: allot, Window: st.Window})
		}
		for _, a := range state.snapshot.Accounts {
			if !accountAllowed(state.principal, a) {
				continue
			}
			account := ScriptAccount{AccountID: a.ID, ProviderID: a.ProviderID, State: a.State, SourceID: a.SourceID, Windows: make([]ScriptWindow, 0, len(a.Windows)), ProviderWindows: make([]ScriptWindow, 0, len(a.ProviderWindows))}
			for _, w := range a.Windows {
				account.Windows = append(account.Windows, scriptWindow(w, state.at))
			}
			for _, w := range a.ProviderWindows {
				account.ProviderWindows = append(account.ProviderWindows, scriptWindow(w, state.at))
			}
			input.Accounts = append(input.Accounts, account)
		}
		eligible := false
		for _, stage := range input.Stages {
			if stage.Skipped == "" && len(stage.Candidates) > 0 {
				eligible = true
				break
			}
		}
		if !eligible {
			return decide(req, state, after, nil), state, nil
		}
		p, err := hook.Evaluate(ctx, input)
		if err != nil {
			return Decision{}, state, scriptError(state.vm.ID, err.Error())
		}
		if err = validatePolicies(p, input); err != nil {
			return Decision{}, state, scriptError(state.vm.ID, err.Error())
		}
		proposal = &p
		// Evaluation is outside every database transaction. Reload authority and
		// inventory after it: a script cannot freeze a grant or account's health.
		fresh, err := s.state(ctx, req.Asked)
		if err != nil {
			return Decision{}, state, err
		}
		if fresh.vm == nil || !reflect.DeepEqual(fresh.vm, state.vm) {
			return Decision{}, fresh, scriptError(state.vm.ID, "virtual model changed during script evaluation")
		}
		fresh.blocked = state.blocked
		state = fresh
	}
	return decide(req, state, after, proposal), state, nil
}
func (s *Store) decision(ctx context.Context, req Request, state routeState, after int) (Decision, error) {
	d, _, err := s.choose(ctx, req, state, after)
	return d, err
}
func scriptError(vm, message string) *Error {
	if len(message) > 1024 {
		message = message[:1024]
	}
	return &Error{Status: 422, Code: "neoproxy_script_error", Decision: Decision{Outcome: "error", VirtualModel: vm, Reason: "script-error", Message: strings.ToValidUTF8(message, ""), Trail: []TrailStep{}}}
}
func decisionError(d Decision) error {
	switch d.Outcome {
	case "ok":
		return nil
	case "limited":
		return &Error{Status: 429, Code: "neoproxy_limit", Decision: d}
	case "queued":
		return &Error{Status: 429, Code: "neoproxy_limit", Decision: d}
	}
	status := 403
	if d.Reason == "unknown-model" {
		status = 404
	}
	if d.Reason == "no-healthy-account" {
		status = 503
	}
	if d.Reason == "invalid-script-proposal" {
		status = 422
	}
	return &Error{Status: status, Code: "neoproxy_" + d.Reason, Decision: d}
}
func (s *Store) Simulate(ctx context.Context, req Request) (Decision, error) {
	state, err := s.state(ctx, req.Asked)
	if err != nil {
		return Decision{}, err
	}
	limited, err := s.userLimited(ctx, state.principal)
	if err != nil {
		return Decision{}, err
	}
	if limited {
		return userLimitError().Decision, nil
	}
	return s.decision(ctx, req, state, -1)
}
func (s *Store) Models(ctx context.Context) ([]string, error) {
	state, err := s.state(ctx, "")
	if err != nil {
		return nil, err
	}
	vms, err := s.VirtualModels(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0)
	for _, vm := range vms {
		if _, ok := grantFor(state.principal, vm.ID); ok {
			out = append(out, vm.Served)
		}
	}
	for _, m := range state.snapshot.Models {
		if !m.VirtualOnly && m.Exposed && providerVisible(state.snapshot, m.ProviderID) && state.principal.AllowsDirect(m.ProviderID, m.ID) {
			for _, a := range state.snapshot.Accounts {
				if a.ProviderID == m.ProviderID && accountAllowed(state.principal, a) && accountSupports(a, m.ID) {
					out = append(out, m.Served)
					break
				}
			}
		}
	}
	s.redirectMu.RLock()
	defer s.redirectMu.RUnlock()
	return redirectedModels(out, s.redirects, state.principal, state.snapshot, vms), nil
}
