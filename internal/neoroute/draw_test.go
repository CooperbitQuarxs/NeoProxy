package neoroute

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/rand/v2"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopool"
)

// scriptedRand replays fixed draws so a random mode's choice is exact.
type scriptedRand struct {
	ints   []int
	floats []float64
}

func (r *scriptedRand) IntN(n int) int {
	v := r.ints[0]
	r.ints = r.ints[1:]
	return v % n
}
func (r *scriptedRand) Float64() float64 {
	v := r.floats[0]
	r.floats = r.floats[1:]
	return v
}

const drawAt = int64(1_000_000_000)
const hour = int64(3_600_000)

// drawState has four candidates in stage order y, w, x, z (snapshot order is
// w, x, y, z): y 30% used resetting in 10h, w 10% in 5h, x 50% in 1h, and z
// unmetered. Every threshold is 90%.
func drawState(draw string) routeState {
	state := policyState()
	state.at = drawAt
	state.memory = newDrawMemory()
	window := func(used float64, resetIn int64) []neopool.Window {
		return []neopool.Window{{Kind: "day", Known: true, Source: "provider-reported", Unit: "percent", Capacity: 100, Used: used, ResetsAt: drawAt + resetIn}}
	}
	account := func(id string, windows []neopool.Window) neopool.Account {
		return neopool.Account{ID: id, EngineAuthID: "engine-" + id, ProviderID: "primary", OwnerID: "owner", State: "healthy", ModelIDs: []string{"primary/model"}, ProviderWindows: windows}
	}
	state.snapshot.Accounts = []neopool.Account{account("w", window(.1, 5*hour)), account("x", window(.5, hour)), account("y", window(.3, 10*hour)), account("z", nil)}
	st := &state.vm.Stages[0]
	st.Draw, st.PerAccount, st.Cap = draw, .9, 1
	st.Accounts = AccountScope{IDs: []string{"y", "w", "x", "z"}}
	return state
}

func drawStage(t *testing.T, d Decision) TrailStep {
	t.Helper()
	for _, step := range d.Trail {
		if step.Kind == "stage" && step.Chosen != "" {
			return step
		}
	}
	t.Fatalf("no chosen stage: %+v", d)
	return TrailStep{}
}

func TestDrawModesOnFixedCandidates(t *testing.T) {
	without := func(id string) func(*routeState) {
		return func(s *routeState) {
			s.vm.Stages[0].Accounts.IDs = slices.DeleteFunc(slices.Clone(s.vm.Stages[0].Accounts.IDs), func(v string) bool { return v == id })
		}
	}
	tests := []struct {
		name, draw, want, mode string
		change                 func(*routeState)
		note                   string
	}{
		{"priority takes stage order", "priority", "y", "priority", nil, ""},
		{"fill is priority", "fill", "y", "priority", nil, ""},
		{"round-robin", "round-robin", "w", "round-robin", func(s *routeState) { s.cursors["s0"] = 1 }, "turn 2 of 4"},
		{"alternate is round-robin", "alternate", "x", "round-robin", func(s *routeState) { s.cursors["s0"] = 6 }, ""},
		{"least-used", "least-used", "z", "least-used", nil, ""},
		{"least-used metered", "least-used", "w", "least-used", without("z"), ""},
		{"headroom unmetered is unlimited", "headroom", "z", "headroom", nil, "unmetered"},
		{"headroom metered", "headroom", "w", "headroom", without("z"), "80% headroom"},
		{"random skips the last pick", "random", "w", "random", func(s *routeState) {
			s.rng = &scriptedRand{ints: []int{0}}
			s.memory.update(memoryKey(s.vm.ID, "s0"), func(m *stageMemory) { m.last = "y" })
		}, "not y again"},
		{"strict-random may repeat", "strict-random", "y", "strict-random", func(s *routeState) {
			s.rng = &scriptedRand{ints: []int{0}}
			s.memory.update(memoryKey(s.vm.ID, "s0"), func(m *stageMemory) { m.last = "y" })
		}, ""},
		{"weighted missing weighs 1", "weighted", "z", "weighted", func(s *routeState) {
			s.vm.Stages[0].Weights = map[string]float64{"y": 0, "w": 0, "x": 0}
			s.rng = &scriptedRand{floats: []float64{.3}}
		}, "100% odds"},
		{"weighted by odds", "weighted", "x", "weighted", func(s *routeState) {
			s.vm.Stages[0].Weights = map[string]float64{"y": 1, "w": 1, "x": 2, "z": 0}
			s.rng = &scriptedRand{floats: []float64{.6}}
		}, "50% odds"},
		{"weighted all zero is uniform", "weighted", "x", "weighted", func(s *routeState) {
			s.vm.Stages[0].Weights = map[string]float64{"y": 0, "w": 0, "x": 0, "z": 0}
			s.rng = &scriptedRand{ints: []int{2}}
		}, "uniform"},
		{"quota-weighted low", "quota-weighted", "y", "quota-weighted", func(s *routeState) { s.rng = &scriptedRand{floats: []float64{.01}} }, ""},
		{"quota-weighted high", "quota-weighted", "z", "quota-weighted", func(s *routeState) { s.rng = &scriptedRand{floats: []float64{.99}} }, ""},
		{"p2c keeps the less used", "p2c", "y", "p2c", func(s *routeState) { s.rng = &scriptedRand{ints: []int{0, 1}} }, "2 drawn: y 30% vs x 50%"},
		{"p2c second wins", "p2c", "w", "p2c", func(s *routeState) { s.rng = &scriptedRand{ints: []int{2, 1}} }, "2 drawn: x 50% vs w 10%"},
		{"reset-soonest", "reset-soonest", "x", "reset-soonest", nil, "resets in 1h0m0s"},
		{"reset-latest", "reset-latest", "y", "reset-latest", nil, "resets in 10h0m0s"},
		{"reset with only unmetered", "reset-soonest", "z", "reset-soonest", func(s *routeState) { s.vm.Stages[0].Accounts.IDs = []string{"z"} }, "no reset times"},
		{"sticky", "sticky", "x", "sticky", func(s *routeState) {
			s.memory.update(memoryKey(s.vm.ID, "s0"), func(m *stageMemory) { m.sticky = "x" })
		}, "sticky to x"},
		{"sticky without memory is headroom", "sticky", "w", "sticky", without("z"), "no sticky account"},
		{"cheapest", "cheapest", "w", "cheapest", func(s *routeState) {
			s.prices = map[string]neopool.PlanPrice{"y": {Kind: "subscription", MonthlyUSD: 200, Priced: true}, "w": {Kind: "subscription", MonthlyUSD: 20, Priced: true}, "x": {Kind: "apiKey", MonthlyUSD: 20}}
		}, "$20/month"},
		{"cheapest tie goes to headroom", "cheapest", "z", "cheapest", func(s *routeState) {
			s.prices = map[string]neopool.PlanPrice{"y": {MonthlyUSD: 20, Priced: true}, "w": {MonthlyUSD: 20, Priced: true}, "z": {MonthlyUSD: 20, Priced: true}}
		}, ""},
		{"cheapest unpriced keys last", "cheapest", "y", "cheapest", func(s *routeState) {
			s.prices = map[string]neopool.PlanPrice{"y": {MonthlyUSD: 500, Priced: true}, "w": {Kind: "apiKey", MonthlyUSD: 20}}
		}, ""},
		{"fit-context draws by headroom", "fit-context", "w", "fit-context", without("z"), ""},
		{"auto without history", "auto", "z", "auto", nil, "score 0.90"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := drawState(test.draw)
			if test.change != nil {
				test.change(&state)
			}
			d := decide(Request{Asked: "neoproxy/route"}, state, -1, nil)
			if d.Outcome != "ok" || d.AccountID != test.want {
				t.Fatalf("decision=%+v", d)
			}
			step := drawStage(t, d)
			if step.Draw != test.mode || !strings.Contains(step.DrawNote, test.note) {
				t.Fatalf("draw=%q note=%q", step.Draw, step.DrawNote)
			}
			raw, _ := json.Marshal(step)
			if !strings.Contains(string(raw), `"draw":"`+test.mode+`"`) {
				t.Fatalf("trail JSON lacks draw: %s", raw)
			}
		})
	}
}

func TestRandomDrawsWithSeededRand(t *testing.T) {
	state := drawState("random")
	state.rng = rand.New(rand.NewPCG(1, 2))
	key := memoryKey(state.vm.ID, "s0")
	seen := map[string]int{}
	last := ""
	for range 200 {
		d := decide(Request{Asked: "neoproxy/route"}, state, -1, nil)
		if d.AccountID == last {
			t.Fatalf("random repeated %s", last)
		}
		seen[d.AccountID]++
		last = d.AccountID
		state.memory.update(key, func(m *stageMemory) { m.last = last })
	}
	if len(seen) != 4 {
		t.Fatalf("random never drew some candidates: %v", seen)
	}
	state = drawState("strict-random")
	state.rng = rand.New(rand.NewPCG(1, 2))
	repeats, last := 0, ""
	for range 200 {
		d := decide(Request{Asked: "neoproxy/route"}, state, -1, nil)
		if d.AccountID == last {
			repeats++
		}
		last = d.AccountID
	}
	if repeats == 0 {
		t.Fatal("strict-random never repeated in 200 draws")
	}
	// The same seed replays the same sequence.
	pick := func() []string {
		s := drawState("strict-random")
		s.rng = rand.New(rand.NewPCG(7, 7))
		var out []string
		for range 10 {
			out = append(out, decide(Request{Asked: "neoproxy/route"}, s, -1, nil).AccountID)
		}
		return out
	}
	if a, b := pick(), pick(); !slices.Equal(a, b) {
		t.Fatalf("seeded draws differ: %v %v", a, b)
	}
}

func TestStickyFallbackAfterFailure(t *testing.T) {
	route := &Store{memory: newDrawMemory()}
	state := drawState("sticky")
	state.memory = route.memory
	index := 0
	served := func(account string, event drawEvent) {
		route.rememberDraw(state, Request{}, Decision{StageIndex: &index, AccountID: account}, event)
	}
	served("x", drawServed)
	if d := decide(Request{Asked: "neoproxy/route"}, state, -1, nil); d.AccountID != "x" {
		t.Fatalf("sticky lost x: %+v", d)
	}
	served("x", drawFailed)
	d := decide(Request{Asked: "neoproxy/route"}, state, -1, nil)
	if d.AccountID != "z" || !strings.Contains(drawStage(t, d).DrawNote, "x failed last") {
		t.Fatalf("after failure: %+v", d)
	}
	// Moving on never returns to the failed account, even when it has the most headroom.
	state.vm.Stages[0].Accounts.IDs = []string{"x", "y"}
	served("y", drawServed)
	served("y", drawFailed)
	if d = decide(Request{Asked: "neoproxy/route"}, state, -1, nil); d.AccountID != "x" {
		t.Fatalf("failed account drawn again: %+v", d)
	}
	// A sticky account that is no longer a candidate falls back too.
	served("w", drawServed)
	if d = decide(Request{Asked: "neoproxy/route"}, state, -1, nil); d.AccountID != "y" {
		t.Fatalf("missing sticky account: %+v", d)
	}
}

func TestStickyThroughRun(t *testing.T) {
	f := newRouteFixture(t, "sticky")
	var tried []string
	failing := func(ctx context.Context, selection Selection) (*Execution, error) {
		tried = append(tried, selection.Decision.AccountID)
		return nil, errors.New("upstream down")
	}
	if _, err := f.route.Run(f.ctx, boundedRequest(false), failing); err == nil {
		t.Fatal("failing upstream succeeded")
	}
	var served []string
	ok := func(ctx context.Context, selection Selection) (*Execution, error) {
		served = append(served, selection.Decision.AccountID)
		return successful(ctx, selection)
	}
	for range 3 {
		if _, err := f.route.Run(f.ctx, boundedRequest(false), ok); err != nil {
			t.Fatal(err)
		}
	}
	if len(tried) != 1 || served[0] == tried[0] || served[1] != served[0] || served[2] != served[0] {
		t.Fatalf("tried=%v served=%v", tried, served)
	}
}

func TestCacheAffinity(t *testing.T) {
	body := func(system, first, last string) []byte {
		raw, _ := json.Marshal(map[string]any{"system": system, "messages": []map[string]string{{"role": "user", "content": first}, {"role": "assistant", "content": "ok"}, {"role": "user", "content": last}}})
		return raw
	}
	one, _ := promptHash(body("You are terse.", "Fix the bug", "turn 1"))
	two, _ := promptHash(body("You are terse.", "Fix the bug", "turn 2"))
	other, _ := promptHash(body("You are verbose.", "Fix the bug", "turn 1"))
	long := strings.Repeat("s", 3000)
	if one != two || one == other {
		t.Fatal("prompt hash must key on the system text and opening message, not the last turn")
	}
	if a, _ := promptHash(body(long, "x", "")); a != func() uint64 { h, _ := promptHash(body(long, "y", "")); return h }() {
		t.Fatal("only the first 2 KiB may count")
	}
	if _, ok := promptHash([]byte(`{"messages":[]}`)); ok {
		t.Fatal("no text must not hash")
	}
	route := &Store{memory: newDrawMemory()}
	state := drawState("cache-affinity")
	state.memory = route.memory
	req := Request{Asked: "neoproxy/route", Body: body("You are terse.", "Fix the bug", "turn 1")}
	d := decide(req, state, -1, nil)
	if d.AccountID != "z" || !strings.Contains(drawStage(t, d).DrawNote, "new here") {
		t.Fatalf("first draw: %+v", d)
	}
	index := 0
	route.rememberDraw(state, req, Decision{StageIndex: &index, AccountID: "x"}, drawReserved)
	later := Request{Asked: "neoproxy/route", Body: body("You are terse.", "Fix the bug", "turn 9")}
	for range 3 {
		if d = decide(later, state, -1, nil); d.AccountID != "x" || !strings.Contains(drawStage(t, d).DrawNote, "warm on x") {
			t.Fatalf("affinity not stable: %+v", d)
		}
	}
	if d = decide(Request{Asked: "neoproxy/route", Body: body("Other", "Fix the bug", "turn 1")}, state, -1, nil); d.AccountID != "z" {
		t.Fatalf("other prefix: %+v", d)
	}
	state.snapshot.Accounts[1].State = "cooldown" // x stops being a candidate
	if d = decide(later, state, -1, nil); d.AccountID != "z" {
		t.Fatalf("bound account gone: %+v", d)
	}
	// The cache is bounded and drops the least recently used prefix.
	for i := range affinityEntries + 1 {
		route.memory.bind("k", uint64(i), "a")
	}
	if _, ok := route.memory.bound("k", 0); ok {
		t.Fatal("LRU kept its oldest entry past the bound")
	}
	if _, ok := route.memory.bound("k", affinityEntries); !ok {
		t.Fatal("LRU dropped the newest entry")
	}
}

func TestFitContextSkipsSmallModels(t *testing.T) {
	big := Request{Asked: "neoproxy/route", Body: []byte(strings.Repeat("x", 1000))} // 250 tokens
	for _, change := range []func(*routeState){
		func(s *routeState) { s.vm.Stages[0].Draw = "fit-context" },
		func(s *routeState) { s.vm.Stages[0].FitContext = true },
	} {
		state := drawState("priority")
		state.snapshot.Models[0].Context = 100
		fallback := state.vm.Stages[0]
		fallback.ID, fallback.Model, fallback.Accounts, fallback.FitContext = "s1", "fallback/model", AccountScope{All: true}, false
		state.snapshot.Accounts = append(state.snapshot.Accounts, neopool.Account{ID: "c", ProviderID: "fallback", OwnerID: "o", State: "healthy", ModelIDs: []string{"fallback/model"}})
		state.vm.Stages = append(state.vm.Stages, fallback)
		change(&state)
		d := decide(big, state, -1, nil)
		if d.AccountID != "c" || d.Trail[0].Skipped != "context" || len(d.Trail[0].Candidates) != 0 {
			t.Fatalf("too long for the first stage: %+v", d)
		}
		if d = decide(Request{Asked: "neoproxy/route", Body: []byte("short")}, state, -1, nil); d.StageID != "s0" {
			t.Fatalf("short request skipped: %+v", d)
		}
		state.snapshot.Models[0].Context = 0
		if d = decide(big, state, -1, nil); d.StageID != "s0" {
			t.Fatalf("unknown context must fit: %+v", d)
		}
	}
	state := drawState("priority")
	state.snapshot.Models[0].Context = 100
	if d := decide(big, state, -1, nil); d.StageID != "s0" {
		t.Fatalf("fit-context off must not skip: %+v", d)
	}
}

func TestAutoDrawWithHistory(t *testing.T) {
	state := drawState("auto")
	if d := decide(Request{Asked: "neoproxy/route"}, state, -1, nil); d.AccountID != "z" {
		t.Fatalf("no history: %+v", d)
	}
	// z keeps failing; w is fast and reliable; y is slow.
	state.history = map[string]accountHistory{"z": {failed: 10}, "w": {ok: 10, p50: 100, latency: true}, "y": {ok: 10, p50: 1000, latency: true}}
	d := decide(Request{Asked: "neoproxy/route"}, state, -1, nil)
	if d.AccountID != "w" || !strings.Contains(drawStage(t, d).DrawNote, "score 0.90") {
		t.Fatalf("with history: %+v %q", d, drawStage(t, d).DrawNote)
	}
	if math.Abs(autoHeadroomWeight+autoSuccessWeight+autoLatencyWeight-1) > 1e-9 {
		t.Fatal("auto weights must sum to 1")
	}
}

func TestAutoThroughRunHistory(t *testing.T) {
	f := newRouteFixture(t, "auto")
	var first string
	_, err := f.route.Run(f.ctx, boundedRequest(false), func(ctx context.Context, selection Selection) (*Execution, error) {
		first = selection.Decision.AccountID
		return nil, errors.New("upstream down")
	})
	if err == nil || first == "" {
		t.Fatal("failing upstream succeeded")
	}
	history, err := f.route.drawHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 0 {
		t.Fatalf("cached history should still be the empty first read: %+v", history)
	}
	f.route.history = nil // expire the cache
	history, err = f.route.drawHistory(context.Background())
	if err != nil || history[first].failed != 1 || history[first].ok != 0 {
		t.Fatalf("history=%+v err=%v", history, err)
	}
	var second string
	if _, err = f.route.Run(f.ctx, boundedRequest(false), func(ctx context.Context, selection Selection) (*Execution, error) {
		second = selection.Decision.AccountID
		return successful(ctx, selection)
	}); err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatalf("auto ignored %s's failure", first)
	}
	f.route.history = nil
	history, _ = f.route.drawHistory(context.Background())
	if h := history[second]; h.ok != 1 || !h.latency {
		t.Fatalf("success not recorded: %+v", history)
	}
}

func TestDrawValidation(t *testing.T) {
	admin := neo.Principal{Role: "admin"}
	for _, mode := range DrawModes {
		for _, id := range append([]string{mode.ID}, mode.Aliases...) {
			state := drawState(id)
			if err := Validate(*state.vm, state.snapshot, admin); err != nil {
				t.Fatalf("%s rejected: %v", id, err)
			}
		}
		if mode.Label == "" || mode.Short == "" || mode.Doc == "" || mode.Needs == nil {
			t.Fatalf("incomplete catalogue entry %+v", mode)
		}
	}
	for _, bad := range []string{"", "bogus", "fusion", "pipeline", "fill-first"} {
		state := drawState(bad)
		if Validate(*state.vm, state.snapshot, admin) == nil {
			t.Fatalf("draw %q accepted", bad)
		}
	}
	for _, bad := range []float64{-1, math.NaN(), math.Inf(1)} {
		state := drawState("weighted")
		state.vm.Stages[0].Weights = map[string]float64{"w": bad}
		if Validate(*state.vm, state.snapshot, admin) == nil {
			t.Fatalf("weight %v accepted", bad)
		}
	}
	state := drawState("weighted")
	state.vm.Stages[0].Weights = map[string]float64{"w": 0, "x": 2.5}
	if err := Validate(*state.vm, state.snapshot, admin); err != nil {
		t.Fatal(err)
	}
	state.vm.Name = "draws"
	if Validate(*state.vm, state.snapshot, admin) == nil {
		t.Fatal("reserved name accepted")
	}
}

func TestDrawCatalogueEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(neo.WithPrincipal(c.Request.Context(), neo.Principal{UserID: "u", Role: "member", Session: true}))
	})
	(&Store{}).RegisterRoutes(router.Group("/v8/neo"))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest("GET", "/v8/neo/virtual-models/draws", nil))
	var body struct {
		Draws      []DrawMode     `json:"draws"`
		OutOfScope []DrawOmission `json:"outOfScope"`
	}
	if recorder.Code != 200 || json.Unmarshal(recorder.Body.Bytes(), &body) != nil || len(body.Draws) != len(DrawModes) || len(body.OutOfScope) != 2 {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body)
	}
	ids := map[string]bool{}
	for _, mode := range body.Draws {
		if ids[mode.ID] {
			t.Fatalf("duplicate draw %s", mode.ID)
		}
		ids[mode.ID] = true
		for _, need := range mode.Needs {
			if !slices.Contains([]string{"meter", "weights", "price", "history"}, need) {
				t.Fatalf("%s needs unknown input %q", mode.ID, need)
			}
		}
	}
}
