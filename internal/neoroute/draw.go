package neoroute

import (
	"container/list"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopool"
	"github.com/tidwall/gjson"
)

// DrawMode describes one way a stage picks an account among its candidates.
// Needs names the inputs the mode reads beyond the candidate list: "meter"
// (used fractions, budgets, reset times), "weights" (Stage.Weights), "price"
// (the account's plan price) and "history" (the last 24h of request records).
type DrawMode struct {
	ID        string   `json:"id"`
	Label     string   `json:"label"`
	Short     string   `json:"short"`
	Doc       string   `json:"doc"`
	OmniRoute string   `json:"omniroute,omitempty"`
	Aliases   []string `json:"aliases,omitempty"`
	Needs     []string `json:"needs"`
}

// DrawOmission is an OmniRoute strategy NeoProxy deliberately does not offer.
type DrawOmission struct {
	OmniRoute string `json:"omniroute"`
	Reason    string `json:"reason"`
}

// DrawModes is the one table behind validation and GET /virtual-models/draws.
// Aliases are the ids earlier configurations saved; they resolve to the mode.
var DrawModes = []DrawMode{
	{ID: "priority", Label: "Priority", Short: "First account in stage order", Aliases: []string{"fill"}, OmniRoute: "priority, fill-first",
		Doc: "Takes the first candidate in the stage's account order (the explicit list's order, or account ID order for all accounts). An account stays a candidate until its threshold, so this drains the first account before touching the next: OmniRoute's fill-first and priority are the same ordering here.", Needs: []string{}},
	{ID: "round-robin", Label: "Round robin", Short: "Take turns in stage order", Aliases: []string{"alternate"}, OmniRoute: "round-robin",
		Doc: "Rotates through the candidates with a durable per-stage cursor that advances once per reserved request.", Needs: []string{}},
	{ID: "least-used", Label: "Least used", Short: "Lowest used fraction", OmniRoute: "least-used",
		Doc: "Takes the candidate with the lowest used fraction of its meter (unmetered counts as 0); ties go to stage order.", Needs: []string{"meter"}},
	{ID: "headroom", Label: "Most headroom", Short: "Most quota left before its threshold", OmniRoute: "headroom",
		Doc: "Takes the candidate with the most remaining quota: its threshold minus its used fraction. Unmetered accounts have unlimited headroom; ties go to stage order.", Needs: []string{"meter"}},
	{ID: "random", Label: "Random", Short: "Uniform, never the same account twice in a row", OmniRoute: "random",
		Doc: "Picks a candidate uniformly at random, excluding the account this stage drew last when there is more than one candidate.", Needs: []string{}},
	{ID: "strict-random", Label: "Strict random", Short: "Uniform, repeats allowed", OmniRoute: "strict-random",
		Doc: "Picks a candidate uniformly at random; the same account may be drawn any number of times in a row.", Needs: []string{}},
	{ID: "weighted", Label: "Weighted", Short: "Random by the stage's account weights", OmniRoute: "weighted",
		Doc: "Picks at random with odds proportional to the stage's weights (weights keyed by account ID; a missing account weighs 1; all zero means uniform).", Needs: []string{"weights"}},
	{ID: "quota-weighted", Label: "Quota weighted", Short: "Random by remaining quota",
		Doc: "Picks at random with odds proportional to remaining headroom (threshold minus used, never below 0). Unmetered accounts weigh 1, a full meter's worth; all zero means uniform.", Needs: []string{"meter"}},
	{ID: "p2c", Label: "Power of two choices", Short: "Draw two, keep the less used", OmniRoute: "p2c",
		Doc: "Draws two distinct candidates uniformly at random and takes the one with the lower used fraction (the first drawn on a tie).", Needs: []string{"meter"}},
	{ID: "reset-soonest", Label: "Resets soonest", Short: "Spend quota that is about to reset", OmniRoute: "reset-aware",
		Doc: "Takes the metered candidate whose window resets soonest, so quota that would be forfeited at the reset gets used first. Accounts with no reset time (unmetered, or a provider window without one) come last; ties go to stage order.", Needs: []string{"meter"}},
	{ID: "reset-latest", Label: "Resets latest", Short: "Keep soon-resetting accounts free", OmniRoute: "reset-window",
		Doc: "Takes the metered candidate whose window resets latest, keeping headroom on accounts that reset soon. Accounts with no reset time come last; ties go to stage order.", Needs: []string{"meter"}},
	{ID: "sticky", Label: "Sticky", Short: "Stay on the last account that worked", OmniRoute: "lkgp",
		Doc: "Takes the account that last served this stage successfully while it remains a candidate. Without one it falls back to most headroom; after an upstream failure it moves on to a different account. The memory is per stage and in-process.", Needs: []string{"meter"}},
	{ID: "cache-affinity", Label: "Cache affinity", Short: "Same prompt prefix, same account", OmniRoute: "cache-optimized",
		Doc: "Hashes the first 2 KiB of the request's system text and opening user message; requests with the same hash go to the same account while it remains a candidate, which keeps provider prompt caches warm. New prefixes fall back to most headroom. Up to 4096 prefixes are remembered per stage (least recently used dropped first), in-process.", Needs: []string{"meter"}},
	{ID: "cheapest", Label: "Cheapest", Short: "Lowest plan price first", OmniRoute: "cost-optimized",
		Doc: "Ranks candidates by their plan's monthly price, ascending. Pay-as-you-go keys nobody priced rank as the most expensive; ties go to most headroom.", Needs: []string{"price", "meter"}},
	{ID: "fit-context", Label: "Fits the context", Short: "Skip the stage when the prompt is too long", OmniRoute: "context-optimized",
		Doc: "Skips the stage (\"context\") when the request's estimated tokens exceed the stage model's context window, then draws by most headroom. The same skip is available with any draw through the stage's fitContext switch. OmniRoute's context-relay is covered by ordering stages by context size, smallest first, with fit-context on.", Needs: []string{"meter"}},
	{ID: "auto", Label: "Auto", Short: "Best headroom, success rate and latency", OmniRoute: "auto",
		Doc: "Scores each candidate 0.5×headroom + 0.3×success rate + 0.2×(1 − normalized p50 latency) and takes the highest (ties to stage order). Success rate and latency come from the last 24h of request records on that account (success 1.0 and latency 0.5 without data); latency is min-max normalized across the candidates that have it.", Needs: []string{"meter", "history"}},
}

// DrawOmissions are OmniRoute strategies outside a stage's draw on purpose.
var DrawOmissions = []DrawOmission{
	{OmniRoute: "fusion", Reason: "Fusion sends one request to several models and merges their answers: a multi-call answer strategy, not a choice of account."},
	{OmniRoute: "pipeline", Reason: "Pipeline chains several model calls, each feeding the next: a multi-call answer strategy, not a choice of account."},
}

// canonicalDraw resolves a saved draw id, including legacy aliases, to its
// DrawModes id; "" when the id is unknown.
func canonicalDraw(id string) string {
	for _, mode := range DrawModes {
		if mode.ID == id || slices.Contains(mode.Aliases, id) {
			return mode.ID
		}
	}
	return ""
}

func drawCatalogue(c *gin.Context) {
	if _, ok := sessionPrincipal(c); !ok {
		return
	}
	c.JSON(200, gin.H{"draws": DrawModes, "outOfScope": DrawOmissions})
}

// Auto draw score weights; they sum to 1.
const (
	autoHeadroomWeight = 0.5
	autoSuccessWeight  = 0.3
	autoLatencyWeight  = 0.2
	// autoNoLatency is the latency term of an account without latency data or
	// when every candidate's p50 is the same.
	autoNoLatency = 0.5
	// autoNoSuccess is the success rate of an account without request records.
	autoNoSuccess = 1.0
)

const (
	affinityPrefixBytes = 2 << 10
	affinityEntries     = 4096
	drawHistoryWindow   = 24 * time.Hour
	drawHistoryTTL      = 30 * time.Second
)

// drawRand is the randomness the random draws use; tests inject a seeded one.
type drawRand interface {
	IntN(int) int
	Float64() float64
}

type globalRand struct{}

func (globalRand) IntN(n int) int   { return rand.IntN(n) }
func (globalRand) Float64() float64 { return rand.Float64() }

// accountHistory is one account's last 24h of routed requests.
type accountHistory struct {
	ok, failed int
	p50        float64
	latency    bool
}

// drawMemory is the in-process state of the stateful draws, keyed by stage.
// Simulation only reads it; execution writes it.
type drawMemory struct {
	mu     sync.Mutex
	stages map[string]*stageMemory
}

type stageMemory struct {
	last     string // last reserved account (random)
	sticky   string // last account that served successfully (sticky)
	failed   string // last account that failed upstream (sticky)
	affinity *affinityCache
}

type affinityCache struct {
	order *list.List // front = most recent; values are affinityEntry
	index map[uint64]*list.Element
}

type affinityEntry struct {
	hash    uint64
	account string
}

func newDrawMemory() *drawMemory { return &drawMemory{stages: make(map[string]*stageMemory)} }

func memoryKey(vm, stage string) string { return vm + "\x00" + stage }

// stage returns the stage's memory, creating it; the caller holds m.mu.
func (m *drawMemory) stage(key string) *stageMemory {
	sm := m.stages[key]
	if sm == nil {
		sm = &stageMemory{}
		m.stages[key] = sm
	}
	return sm
}

// view returns a copy of the stage's scalar memory.
func (m *drawMemory) view(key string) stageMemory {
	if m == nil {
		return stageMemory{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if sm := m.stages[key]; sm != nil {
		return stageMemory{last: sm.last, sticky: sm.sticky, failed: sm.failed}
	}
	return stageMemory{}
}

func (m *drawMemory) bound(key string, hash uint64) (string, bool) {
	if m == nil {
		return "", false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	sm := m.stages[key]
	if sm == nil || sm.affinity == nil {
		return "", false
	}
	if e, ok := sm.affinity.index[hash]; ok {
		return e.Value.(affinityEntry).account, true
	}
	return "", false
}

func (m *drawMemory) bind(key string, hash uint64, account string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sm := m.stage(key)
	if sm.affinity == nil {
		sm.affinity = &affinityCache{order: list.New(), index: make(map[uint64]*list.Element)}
	}
	c := sm.affinity
	if e, ok := c.index[hash]; ok {
		e.Value = affinityEntry{hash, account}
		c.order.MoveToFront(e)
		return
	}
	c.index[hash] = c.order.PushFront(affinityEntry{hash, account})
	if c.order.Len() > affinityEntries {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.index, oldest.Value.(affinityEntry).hash)
	}
}

func (m *drawMemory) update(key string, change func(*stageMemory)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	change(m.stage(key))
}

// forget drops every stage memory of a saved or deleted virtual model.
func (m *drawMemory) forget(vm string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key := range m.stages {
		if strings.HasPrefix(key, vm+"\x00") {
			delete(m.stages, key)
		}
	}
}

// drawEvent is what execution learned about a stage's pick.
type drawEvent int

const (
	drawReserved drawEvent = iota
	drawServed
	drawFailed
)

// rememberDraw feeds execution outcomes back into the stateful draws.
func (s *Store) rememberDraw(state routeState, req Request, d Decision, event drawEvent) {
	if d.StageIndex == nil || state.vm == nil || *d.StageIndex >= len(state.vm.Stages) {
		return
	}
	st := state.vm.Stages[*d.StageIndex]
	key := memoryKey(state.vm.ID, st.ID)
	switch mode := canonicalDraw(st.Draw); {
	case event == drawReserved && mode == "random":
		s.memory.update(key, func(sm *stageMemory) { sm.last = d.AccountID })
	case event == drawReserved && mode == "cache-affinity":
		if hash, ok := promptHash(req.Body); ok {
			s.memory.bind(key, hash, d.AccountID)
		}
	case event == drawServed && mode == "sticky":
		s.memory.update(key, func(sm *stageMemory) { sm.sticky, sm.failed = d.AccountID, "" })
	case event == drawFailed && mode == "sticky":
		s.memory.update(key, func(sm *stageMemory) {
			if sm.sticky == d.AccountID {
				sm.sticky = ""
			}
			sm.failed = d.AccountID
		})
	}
}

// stagesUse reports whether any stage of vm draws with one of the modes.
func stagesUse(vm *VirtualModel, modes ...string) bool {
	return vm != nil && slices.ContainsFunc(vm.Stages, func(st Stage) bool { return slices.Contains(modes, canonicalDraw(st.Draw)) })
}

// planPricer is the optional pool capability the cheapest draw reads.
type planPricer interface {
	PlanPrices(context.Context) (map[string]neopool.PlanPrice, error)
}

func (s *Store) planPrices(ctx context.Context) (map[string]neopool.PlanPrice, error) {
	if pricer, ok := s.pools.(planPricer); ok {
		return pricer.PlanPrices(ctx)
	}
	return nil, nil
}

// drawHistory summarizes the last 24h of request records per account: a final
// "ok" counts as a success with its latency, an upstream failure (a failed
// stage attempt in the trail, or a final upstream error) as a failure. The
// result is cached briefly because every auto decision reads it.
func (s *Store) drawHistory(ctx context.Context) (map[string]accountHistory, error) {
	now := s.now()
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	if s.history != nil && !now.Before(s.historyAt) && now.Sub(s.historyAt) < drawHistoryTTL {
		return s.history, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT body FROM neoroute_requests WHERE at>?`, now.Add(-drawHistoryWindow).UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type record struct {
		Outcome   string `json:"outcome"`
		LatencyMs int64  `json:"latencyMs"`
		Decision  struct {
			AccountID string `json:"accountId"`
			Reason    string `json:"reason"`
			Trail     []struct {
				Chosen  string `json:"chosen"`
				Skipped string `json:"skipped"`
			} `json:"trail"`
		} `json:"decision"`
	}
	counts := make(map[string]*accountHistory)
	latencies := make(map[string][]int64)
	entry := func(id string) *accountHistory {
		h := counts[id]
		if h == nil {
			h = &accountHistory{}
			counts[id] = h
		}
		return h
	}
	for rows.Next() {
		var raw []byte
		var r record
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if json.Unmarshal(raw, &r) != nil {
			continue
		}
		for _, step := range r.Decision.Trail {
			if step.Skipped == "upstream-error" && step.Chosen != "" {
				entry(step.Chosen).failed++
			}
		}
		switch id := r.Decision.AccountID; {
		case id == "":
		case r.Outcome == "ok":
			entry(id).ok++
			latencies[id] = append(latencies[id], r.LatencyMs)
		case r.Outcome == "error" && r.Decision.Reason == "upstream-error":
			entry(id).failed++
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	out := make(map[string]accountHistory, len(counts))
	for id, h := range counts {
		if l := latencies[id]; len(l) > 0 {
			slices.Sort(l)
			h.p50, h.latency = float64(l[(len(l)-1)/2]), true
		}
		out[id] = *h
	}
	s.history, s.historyAt = out, now
	return out, nil
}

// promptHash hashes the first 2 KiB of the request's system text followed by
// its opening user message: the prefix a provider prompt cache keys on. The
// last user message changes every turn, so it is not part of the key.
func promptHash(body []byte) (uint64, bool) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return 0, false
	}
	root := gjson.ParseBytes(body)
	var b strings.Builder
	add := func(text string) {
		if text == "" || b.Len() >= affinityPrefixBytes {
			return
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(text)
	}
	add(textOf(root.Get("system")))
	add(root.Get("instructions").String())
	messages := root.Get("messages").Array()
	if input := root.Get("input"); input.IsArray() {
		messages = input.Array()
	}
	for _, m := range messages {
		if role := m.Get("role").String(); role == "system" || role == "developer" {
			add(textOf(m.Get("content")))
		}
	}
	if input := root.Get("input"); input.Type == gjson.String {
		add(input.String())
	} else {
		for _, m := range messages {
			if m.Get("role").String() == "user" {
				if text := textOf(m.Get("content")); text != "" {
					add(text)
					break
				}
			}
		}
	}
	if b.Len() == 0 {
		return 0, false
	}
	prefix := b.String()
	if len(prefix) > affinityPrefixBytes {
		prefix = prefix[:affinityPrefixBytes]
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(prefix))
	return h.Sum64(), true
}

// textOf is a content value's text: a string, or its text parts joined.
func textOf(content gjson.Result) string {
	if content.Type == gjson.String {
		return content.String()
	}
	var parts []string
	for _, part := range content.Array() {
		switch part.Get("type").String() {
		case "text", "input_text":
			if text := part.Get("text"); text.Type == gjson.String && text.String() != "" {
				parts = append(parts, text.String())
			}
		}
	}
	return strings.Join(parts, "\n")
}

// headroom is the candidate's remaining quota before its threshold; an
// unmetered account has unlimited headroom.
func headroom(c Candidate) float64 {
	if !c.Metered {
		return math.Inf(1)
	}
	return max(0, c.Budget-c.Used)
}

func pct(f float64) string { return fmt.Sprintf("%.0f%%", f*100) }

func headroomText(c Candidate) string {
	if !c.Metered {
		return "unmetered"
	}
	return pct(headroom(c)) + " headroom"
}

// mostHeadroom is the index of the candidate with the most headroom, ties to
// stage order, skipping avoid when another candidate remains.
func mostHeadroom(listed []Candidate, avoid string) int {
	best := -1
	for i, c := range listed {
		if c.AccountID == avoid && len(listed) > 1 {
			continue
		}
		if best < 0 || headroom(c) > headroom(listed[best]) {
			best = i
		}
	}
	return best
}

// weightedPick draws an index with odds proportional to weights; all zero
// (or none positive) draws uniformly.
func weightedPick(rng drawRand, weights []float64) int {
	total := 0.0
	for _, w := range weights {
		total += w
	}
	if !(total > 0) {
		return rng.IntN(len(weights))
	}
	x := rng.Float64() * total
	for i, w := range weights {
		if x < w {
			return i
		}
		x -= w
	}
	for i := len(weights) - 1; i >= 0; i-- {
		if weights[i] > 0 {
			return i
		}
	}
	return len(weights) - 1
}

func oddsNote(listed []Candidate, weights []float64, chosen int) string {
	total := 0.0
	for _, w := range weights {
		total += w
	}
	if !(total > 0) {
		return fmt.Sprintf("all weights 0: uniform over %d", len(listed))
	}
	return fmt.Sprintf("drew %s at %s odds of %d", listed[chosen].AccountID, pct(weights[chosen]/total), len(listed))
}

// resetsAt is the account's window reset for the stage, 0 when it has none.
func resetsAt(a neopool.Account, st Stage, at int64) int64 {
	if w, _, metered := windowFor(a, st.Window, at); metered {
		return w.ResetsAt
	}
	return 0
}

func untilText(reset, at int64) string {
	return (time.Duration(reset-at) * time.Millisecond).Round(time.Minute).String()
}

// drawCandidate picks one of the stage's candidates (aligned with listed) and
// says why. It reads the draw memory but never writes it.
func drawCandidate(state routeState, vm string, st Stage, candidates []neopool.Account, listed []Candidate, facts *requestFacts) (int, string) {
	n := len(candidates)
	rng := state.rng
	if rng == nil {
		rng = globalRand{}
	}
	key := memoryKey(vm, st.ID)
	switch canonicalDraw(st.Draw) {
	case "round-robin":
		i := int(state.cursors[st.ID] % int64(n))
		return i, fmt.Sprintf("turn %d of %d", i+1, n)
	case "least-used":
		chosen := 0
		for i := range listed {
			if listed[i].Used < listed[chosen].Used {
				chosen = i
			}
		}
		return chosen, "lowest used: " + pct(listed[chosen].Used)
	case "headroom", "fit-context":
		chosen := mostHeadroom(listed, "")
		return chosen, "most headroom: " + headroomText(listed[chosen])
	case "random":
		last := state.memory.view(key).last
		previous := slices.IndexFunc(listed, func(c Candidate) bool { return c.AccountID == last })
		if n == 1 || previous < 0 {
			return rng.IntN(n), fmt.Sprintf("uniform over %d", n)
		}
		i := rng.IntN(n - 1)
		if i >= previous {
			i++
		}
		return i, fmt.Sprintf("uniform over %d, not %s again", n-1, last)
	case "strict-random":
		return rng.IntN(n), fmt.Sprintf("uniform over %d", n)
	case "weighted":
		weights := make([]float64, n)
		for i, c := range listed {
			weights[i] = 1
			if w, ok := st.Weights[c.AccountID]; ok {
				weights[i] = w
			}
		}
		chosen := weightedPick(rng, weights)
		return chosen, oddsNote(listed, weights, chosen)
	case "quota-weighted":
		weights := make([]float64, n)
		for i, c := range listed {
			weights[i] = 1
			if c.Metered {
				weights[i] = headroom(c)
			}
		}
		chosen := weightedPick(rng, weights)
		return chosen, oddsNote(listed, weights, chosen)
	case "p2c":
		if n == 1 {
			return 0, "only candidate"
		}
		a := rng.IntN(n)
		b := rng.IntN(n - 1)
		if b >= a {
			b++
		}
		chosen := a
		if listed[b].Used < listed[a].Used {
			chosen = b
		}
		return chosen, fmt.Sprintf("2 drawn: %s %s vs %s %s", listed[a].AccountID, pct(listed[a].Used), listed[b].AccountID, pct(listed[b].Used))
	case "reset-soonest", "reset-latest":
		soonest := canonicalDraw(st.Draw) == "reset-soonest"
		chosen, best := 0, int64(0)
		for i, a := range candidates {
			r := resetsAt(a, st, state.at)
			if r > 0 && (best == 0 || (soonest && r < best) || (!soonest && r > best)) {
				chosen, best = i, r
			}
		}
		if best == 0 {
			return 0, "no reset times: stage order"
		}
		return chosen, "resets in " + untilText(best, state.at)
	case "sticky":
		memory := state.memory.view(key)
		if i := slices.IndexFunc(listed, func(c Candidate) bool { return c.AccountID == memory.sticky }); memory.sticky != "" && i >= 0 {
			return i, "sticky to " + memory.sticky
		}
		chosen := mostHeadroom(listed, memory.failed)
		if memory.failed != "" && listed[chosen].AccountID != memory.failed {
			return chosen, memory.failed + " failed last; most headroom: " + listed[chosen].AccountID
		}
		return chosen, "no sticky account; most headroom: " + headroomText(listed[chosen])
	case "cache-affinity":
		hash, ok := promptHash(facts.req.Body)
		if !ok {
			chosen := mostHeadroom(listed, "")
			return chosen, "no prompt text; most headroom: " + headroomText(listed[chosen])
		}
		if account, found := state.memory.bound(key, hash); found {
			if i := slices.IndexFunc(listed, func(c Candidate) bool { return c.AccountID == account }); i >= 0 {
				return i, fmt.Sprintf("prompt %016x warm on %s", hash, account)
			}
		}
		chosen := mostHeadroom(listed, "")
		return chosen, fmt.Sprintf("prompt %016x new here; most headroom: %s", hash, headroomText(listed[chosen]))
	case "cheapest":
		rank := func(i int) (int, float64) {
			p, ok := state.prices[listed[i].AccountID]
			if !ok || !p.Priced {
				return 1, 0
			}
			return 0, p.MonthlyUSD
		}
		chosen := 0
		for i := 1; i < n; i++ {
			ci, cp := rank(i)
			bi, bp := rank(chosen)
			if ci < bi || (ci == bi && (cp < bp || (cp == bp && headroom(listed[i]) > headroom(listed[chosen])))) {
				chosen = i
			}
		}
		if unpriced, price := rank(chosen); unpriced == 0 {
			return chosen, fmt.Sprintf("cheapest plan: $%.0f/month", price)
		}
		return chosen, "no priced plan; most headroom: " + headroomText(listed[chosen])
	case "auto":
		return autoDraw(state, listed)
	}
	return 0, "first in stage order"
}

// autoDraw scores each candidate (see the auto DrawModes entry).
func autoDraw(state routeState, listed []Candidate) (int, string) {
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, c := range listed {
		if h, ok := state.history[c.AccountID]; ok && h.latency {
			lo, hi = min(lo, h.p50), max(hi, h.p50)
		}
	}
	chosen, best := 0, math.Inf(-1)
	var note string
	for i, c := range listed {
		room := 1.0
		if c.Metered {
			room = min(1, headroom(c))
		}
		success, latency := autoNoSuccess, autoNoLatency
		h, ok := state.history[c.AccountID]
		if ok && h.ok+h.failed > 0 {
			success = float64(h.ok) / float64(h.ok+h.failed)
		}
		if ok && h.latency && hi > lo {
			latency = 1 - (h.p50-lo)/(hi-lo)
		}
		score := autoHeadroomWeight*room + autoSuccessWeight*success + autoLatencyWeight*latency
		if score > best {
			chosen, best = i, score
			note = fmt.Sprintf("score %.2f: headroom %s, success %s, latency %.2f", score, pct(room), pct(success), latency)
		}
	}
	return chosen, note
}

// tokens is the request's estimated input tokens (body bytes / 4, rounded up).
func (f *requestFacts) tokens() int64 { return int64((len(f.req.Body) + 3) / 4) }

// fitsContext reports whether the request fits the stage's effective context
// window. window is the stage's imposed context parameter (always enforced);
// without one the model's own window applies, and only when the stage asks for
// it with FitContext or the fit-context draw. An unknown window (0) always fits.
func fitsContext(st Stage, m neopool.Model, facts *requestFacts, window int) bool {
	if window > 0 {
		return facts == nil || facts.tokens() <= int64(window)
	}
	if !st.FitContext && canonicalDraw(st.Draw) != "fit-context" {
		return true
	}
	return m.Context <= 0 || facts == nil || facts.tokens() <= int64(m.Context)
}
