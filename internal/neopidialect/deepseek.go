package neopidialect

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/cursorwire"
)

// DeepSeek's chat-template tokens and DSML tool-call tags (dialect/deepseek.ts).
const (
	dsToolCallsBegin = "<｜tool▁calls▁begin｜>"
	dsToolCallsEnd   = "<｜tool▁calls▁end｜>"
	dsToolCallBegin  = "<｜tool▁call▁begin｜>"
	dsToolCallEnd    = "<｜tool▁call▁end｜>"
	dsToolSeparator  = "<｜tool▁sep｜>"
	dsToolOutBegin   = "<｜tool▁output▁begin｜>"
	dsToolOutEnd     = "<｜tool▁output▁end｜>"
	dsBOS            = "<｜begin▁of▁sentence｜>"
	dsUser           = "<｜User｜>"
	dsAssistant      = "<｜Assistant｜>"
	dsEOS            = "<｜end▁of▁sentence｜>"

	dsThinkOpen       = "<think>"
	dsThinkClose      = "</think>"
	dsLegacyToolType  = "function"
	dsLegacyJSONFence = "```json"
	dsCodeFence       = "```"

	dsmlCallsOpenFull   = "<｜DSML｜tool_calls>"
	dsmlCallsCloseFull  = "</｜DSML｜tool_calls>"
	dsmlCallsOpenASCII  = "<|DSML|tool_calls>"
	dsmlCallsCloseASCII = "</|DSML|tool_calls>"
	dsmlInvokeCloseFull = "</｜DSML｜invoke>"
	dsmlInvokeCloseASCI = "</|DSML|invoke>"
	dsmlParamCloseFull  = "</｜DSML｜parameter>"
	dsmlParamCloseASCII = "</|DSML|parameter>"

	// dsTemplateBar is TEMPLATE_TOKEN_BAR: every accepted re-spelling carries it.
	dsTemplateBar = "｜"
	// dsVisibleTailLimit is VISIBLE_TAIL_LIMIT (UTF-16 units; all BMP here).
	dsVisibleTailLimit = 48
)

var (
	dsControlTokens = []string{
		dsBOS, dsEOS, "<｜▁pad▁｜>", dsUser, dsAssistant, "<|EOT|>",
		"<｜search▁begin｜>", "<｜search▁end｜>", "<｜fim▁hole｜>", "<｜fim▁begin｜>", "<｜fim▁end｜>",
		"<｜tool▁outputs▁begin｜>", "<｜tool▁outputs▁end｜>", dsToolOutBegin, dsToolOutEnd,
	}
	// dsmlOrphanCloseTokens are bare DSML closers leaked into visible text.
	dsmlOrphanCloseTokens = []string{dsmlInvokeCloseFull, dsmlInvokeCloseASCI, dsmlParamCloseFull, dsmlParamCloseASCII}
	dsOutsideTokens       = append(append([]string{
		dsToolCallsBegin, dsToolCallsEnd, dsToolCallBegin, dsThinkOpen, dsThinkClose,
		dsmlCallsOpenFull, dsmlCallsOpenASCII, dsmlCallsCloseFull, dsmlCallsCloseASCII,
	}, dsmlOrphanCloseTokens...), dsControlTokens...)
	dsOutsideDropped = setOf(append([]string{dsToolCallsEnd, dsThinkClose, dsmlCallsCloseFull, dsmlCallsCloseASCII}, dsControlTokens...))

	dsTemplateSpelling        = regexp.MustCompile(`<(｜?)([A-Za-z]+(?:[▁_][A-Za-z]+)*)(｜?)>`)
	dsTemplateSpellingAtStart = regexp.MustCompile(`^<(｜?)([A-Za-z]+(?:[▁_][A-Za-z]+)*)(｜?)>`)
	dsTemplatePartial         = regexp.MustCompile(`^<｜?([A-Za-z▁_]*)(｜?)$`)
	dsCanonicalName           = regexp.MustCompile(`^<｜([A-Za-z]+(?:▁[A-Za-z]+)*)｜>$`)

	dsTemplateByKey    = dsTemplateTokenIndex()
	dsTemplateKeys     = dsSortedKeys(dsTemplateByKey)
	dsTemplateMaxSpell = dsMaxKeyLen(dsTemplateByKey) + 4

	dsOutsideTemplate = setOf(append([]string{dsToolCallsBegin, dsToolCallsEnd, dsToolCallBegin}, dsControlTokens...))
	dsSeparatorTokens = setOf([]string{dsToolSeparator})
	dsArgsTerminators = setOf([]string{dsToolCallEnd, dsToolCallsEnd, dsToolCallBegin})

	dsmlSectionTokens    = []string{dsmlCallsCloseFull, dsmlCallsCloseASCII, "<｜DSML｜invoke", "<|DSML|invoke"}
	dsmlInvokeTokens     = []string{dsmlInvokeCloseFull, dsmlInvokeCloseASCI, "<｜DSML｜parameter", "<|DSML|parameter"}
	dsmlParamCloseTokens = []string{dsmlParamCloseFull, dsmlParamCloseASCII}

	dsBareDSMLOpen   = regexp.MustCompile(`<[｜|]DSML[｜|](?:invoke|parameter)\b`)
	dsDSMLNameAttr   = regexp.MustCompile(`\sname="([^"]*)"`)
	dsDSMLStringAttr = regexp.MustCompile(`\sstring="(true|false)"`)
)

func setOf(tokens []string) map[string]bool {
	out := make(map[string]bool, len(tokens))
	for _, token := range tokens {
		out[token] = true
	}
	return out
}

func dsTemplateKey(name string) string { return strings.ReplaceAll(name, "▁", "_") }

// dsTemplateTokenIndex is TEMPLATE_TOKEN_BY_KEY: the canonical token for
// each spelling-independent key; tokens outside the re-spelling grammar
// (`<｜▁pad▁｜>`, `<|EOT|>`) stay literal.
func dsTemplateTokenIndex() map[string]string {
	out := map[string]string{}
	for _, token := range append([]string{dsToolCallsBegin, dsToolCallsEnd, dsToolCallBegin, dsToolCallEnd, dsToolSeparator}, dsControlTokens...) {
		if match := dsCanonicalName.FindStringSubmatch(token); match != nil {
			out[dsTemplateKey(match[1])] = token
		}
	}
	return out
}

func dsSortedKeys(index map[string]string) []string {
	keys := make([]string, 0, len(index))
	for key := range index {
		keys = append(keys, key)
	}
	return keys
}

func dsMaxKeyLen(index map[string]string) int {
	best := 0
	for key := range index {
		best = max(best, len(key))
	}
	return best
}

// dsTokenMatch is DeepSeekTokenMatch: a canonical token and its written form.
type dsTokenMatch struct {
	token string
	raw   string
	index int
}

func dsResolveSpelling(text string, loc []int) string {
	if loc[3]-loc[2] == 0 && loc[7]-loc[6] == 0 {
		return ""
	}
	return dsTemplateByKey[dsTemplateKey(text[loc[4]:loc[5]])]
}

// dsFindTemplateToken is findDeepSeekTemplateToken: the earliest of tokens at
// or after from, canonically spelled or re-spelled.
func dsFindTemplateToken(text string, tokens map[string]bool, from int) *dsTokenMatch {
	if from > len(text) {
		return nil
	}
	for _, loc := range dsTemplateSpelling.FindAllStringSubmatchIndex(text[from:], -1) {
		sub := text[from:]
		if token := dsResolveSpelling(sub, loc); token != "" && tokens[token] {
			return &dsTokenMatch{token: token, raw: sub[loc[0]:loc[1]], index: from + loc[0]}
		}
	}
	return nil
}

func dsTemplateTokenAtStart(text string) *dsTokenMatch {
	loc := dsTemplateSpellingAtStart.FindStringSubmatchIndex(text)
	if loc == nil {
		return nil
	}
	if token := dsResolveSpelling(text, loc); token != "" {
		return &dsTokenMatch{token: token, raw: text[loc[0]:loc[1]]}
	}
	return nil
}

// dsTemplatePartialLength is templateTokenPartialLength: the byte length of a
// trailing fragment that may still complete into a template token spelling.
func dsTemplatePartialLength(text string) int {
	start := strings.LastIndex(text, "<")
	if start == -1 || utf8.RuneCountInString(text[start:]) >= dsTemplateMaxSpell {
		return 0
	}
	match := dsTemplatePartial.FindStringSubmatch(text[start:])
	if match == nil {
		return 0
	}
	key := dsTemplateKey(match[1])
	viable := false
	if match[2] != "" {
		_, viable = dsTemplateByKey[key]
	} else {
		for _, known := range dsTemplateKeys {
			if strings.HasPrefix(known, key) {
				viable = true
				break
			}
		}
	}
	if !viable {
		return 0
	}
	return len(text) - start
}

// dsCodeState is CodeState: the Markdown code the visible text is inside.
type dsCodeState struct {
	fence, run int
	newline    bool
}

func dsSettleBacktickRun(fence, run int) int {
	switch {
	case run == 0:
		return fence
	case fence == 0:
		return run
	case run == fence:
		return 0
	}
	return fence
}

func dsAdvanceCodeState(state dsCodeState, text string) dsCodeState {
	fence, run, newline := state.fence, state.run, state.newline
	for _, r := range text {
		if r == '`' {
			run++
			newline = false
			continue
		}
		fence = dsSettleBacktickRun(fence, run)
		run = 0
		switch {
		case r == '\n':
			if newline && fence < 3 {
				fence = 0
			}
			newline = true
		case !isJSWhitespace(r):
			newline = false
		}
	}
	return dsCodeState{fence: fence, run: run, newline: newline}
}

// dsInlineSpanCloses is inlineSpanCloses; decided is false until the stream
// decides.
func dsInlineSpanCloses(text string, fence int, final bool) (closes, decided bool) {
	run, newline := 0, false
	for _, r := range text {
		if r == '`' {
			run++
			newline = false
			continue
		}
		if run == fence {
			return true, true
		}
		run = 0
		switch {
		case r == '\n':
			if newline {
				return false, true
			}
			newline = true
		case !isJSWhitespace(r):
			newline = false
		}
	}
	if !final {
		return false, false
	}
	return run == fence, true
}

type dsState int

const (
	dsOutside dsState = iota
	dsThinking
	dsSection
	dsHeader
	dsArgs
	dsLegacyName
	dsLegacyArgs
	dsmlSection
	dsmlInvoke
	dsmlParam
)

// deepseekScanner is NeoPi's DeepSeekInbandScanner (parseThinking on): the
// template tool-call grammar (canonical or re-spelled tokens, legacy fenced
// calls) and the DSML envelope become tool calls, <think> blocks thinking,
// and stray template control tokens are dropped from visible text.
type deepseekScanner struct {
	buffer          string
	state           dsState
	inToolSection   bool
	name            string
	thinking        string
	dsmlArgs        *cursorwire.JSONObject
	dsmlParamName   string
	dsmlParamString bool
	dsmlParamRaw    string
	pendingSection  *string
	stripLeadingWS  bool
	bareDSMLVisible bool
	visibleTail     string
	code            dsCodeState
	closeWait       terminatorWait
}

func newDeepseekScanner() *deepseekScanner {
	return &deepseekScanner{dsmlParamString: true}
}

func (s *deepseekScanner) feed(text string) []scanEvent {
	if text == "" {
		return nil
	}
	if s.closeWait.absorb(text) {
		return nil
	}
	s.buffer = s.closeWait.release(s.buffer) + text
	events := s.consume(false)
	if (s.state == dsArgs || s.state == dsLegacyArgs) && dsTemplatePartialLength(s.buffer) == 0 {
		s.closeWait.arm(dsTemplateBar, s.buffer)
	}
	return events
}

func (s *deepseekScanner) flush() []scanEvent {
	s.buffer = s.closeWait.release(s.buffer)
	return s.consume(true)
}

func (s *deepseekScanner) consume(final bool) []scanEvent {
	var events []scanEvent
	var progressed bool
loop:
	for s.buffer != "" {
		switch s.state {
		case dsOutside:
			events = s.consumeOutside(final, events)
			if s.state != dsOutside && s.buffer != "" {
				continue
			}
			break loop
		case dsThinking:
			events = s.consumeThinking(final, events)
			if !final && s.state == dsThinking {
				break loop
			}
			continue
		case dsSection:
			events, progressed = s.consumeSection(final, events)
		case dsHeader:
			progressed = s.consumeHeader(final)
		case dsLegacyName:
			progressed = s.consumeLegacyName(final)
		case dsArgs, dsLegacyArgs:
			events, progressed = s.consumeArgs(final, events)
		case dsmlSection:
			events, progressed = s.consumeDSMLSection(final, events)
		case dsmlInvoke:
			events, progressed = s.consumeDSMLInvoke(final, events)
		default:
			progressed = s.consumeDSMLParam(final)
		}
		if !progressed {
			break
		}
	}
	if final && s.state == dsThinking {
		events = s.endThinking(events)
	}
	if final && s.pendingSection != nil {
		events = s.emitText(*s.pendingSection+s.buffer, events)
		s.pendingSection = nil
		s.buffer = ""
		s.state = dsOutside
	}
	return events
}

func (s *deepseekScanner) consumeOutside(final bool, events []scanEvent) []scanEvent {
	for s.buffer != "" {
		if s.stripLeadingWS {
			// A dropped control token's template whitespace never leaks.
			trimmed := strings.TrimLeftFunc(s.buffer, isJSWhitespace)
			if trimmed == "" {
				s.buffer = ""
				return events
			}
			s.buffer = trimmed
			s.stripLeadingWS = false
		}
		match, hold := s.findOutsideToken(final)
		if hold >= 0 {
			events = s.emitText(s.buffer[:hold], events)
			s.buffer = s.buffer[hold:]
			return events
		}
		if match == nil {
			keep := 0
			if !final {
				keep = max(partialSuffixOverlapAny(s.buffer, dsOutsideTokens), dsTemplatePartialLength(s.buffer))
			}
			events = s.emitText(s.buffer[:len(s.buffer)-keep], events)
			s.buffer = s.buffer[len(s.buffer)-keep:]
			return events
		}
		events = s.emitText(s.buffer[:match.index], events)
		s.buffer = s.buffer[match.index+len(match.raw):]
		// Markup inside an open inline span proves its backticks never closed.
		if match.raw != match.token {
			if fence := dsSettleBacktickRun(s.code.fence, s.code.run); fence > 0 && fence < 3 {
				s.code = dsCodeState{}
			}
		}
		switch match.token {
		case dsToolCallsBegin:
			s.inToolSection = true
			s.state = dsSection
			return events
		case dsToolCallBegin:
			s.inToolSection = false
			s.state = dsHeader
			return events
		case dsThinkOpen:
			s.state = dsThinking
			s.thinking = ""
			return append(events, scanEvent{kind: scanThinkingStart})
		case dsmlCallsOpenFull, dsmlCallsOpenASCII:
			raw := match.raw
			s.pendingSection = &raw
			s.state = dsmlSection
			return events
		}
		if containsString(dsmlOrphanCloseTokens, match.token) {
			if s.bareDSMLVisible {
				events = s.emitText(match.raw, events)
			}
			continue
		}
		if s.bareDSMLVisible && (match.token == dsmlCallsCloseFull || match.token == dsmlCallsCloseASCII) {
			// Closes the malformed call: keep it as its end marker.
			events = s.emitText(match.raw, events)
			s.bareDSMLVisible = false
			continue
		}
		if dsOutsideDropped[match.token] {
			s.stripLeadingWS = true
		}
	}
	return events
}

func containsString(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func (s *deepseekScanner) emitText(text string, events []scanEvent) []scanEvent {
	if text == "" {
		return events
	}
	events = append(events, scanEvent{kind: scanText, text: text})
	tail := []rune(s.visibleTail + text)
	s.visibleTail = string(tail[max(0, len(tail)-dsVisibleTailLimit):])
	s.code = dsAdvanceCodeState(s.code, text)
	if !s.bareDSMLVisible && dsBareDSMLOpen.MatchString(s.visibleTail) {
		s.bareDSMLVisible = true
	}
	return events
}

func (s *deepseekScanner) consumeThinking(final bool, events []scanEvent) []scanEvent {
	end := strings.Index(s.buffer, dsThinkClose)
	if end == -1 {
		hold := 0
		if !final {
			hold = partialSuffixOverlap(s.buffer, dsThinkClose)
		}
		events = s.emitThinking(s.buffer[:len(s.buffer)-hold], events)
		s.buffer = s.buffer[len(s.buffer)-hold:]
		if final {
			events = s.endThinking(events)
		}
		return events
	}
	events = s.emitThinking(s.buffer[:end], events)
	s.buffer = s.buffer[end+len(dsThinkClose):]
	return s.endThinking(events)
}

func (s *deepseekScanner) emitThinking(delta string, events []scanEvent) []scanEvent {
	if delta == "" {
		return events
	}
	s.thinking += delta
	return append(events, scanEvent{kind: scanThinkingDelta, text: delta})
}

func (s *deepseekScanner) endThinking(events []scanEvent) []scanEvent {
	events = append(events, scanEvent{kind: scanThinkingEnd, text: s.thinking})
	s.thinking = ""
	s.state = dsOutside
	return events
}

func (s *deepseekScanner) consumeSection(final bool, events []scanEvent) ([]scanEvent, bool) {
	for s.buffer != "" {
		// Whitespace before a token is template layout dropped with it;
		// before prose it belongs to the prose.
		rest := strings.TrimLeftFunc(s.buffer, isJSWhitespace)
		indent := len(s.buffer) - len(rest)
		lead := dsTemplateTokenAtStart(rest)
		if lead != nil && lead.token == dsToolCallsEnd {
			s.buffer = rest[len(lead.raw):]
			s.inToolSection = false
			s.state = dsOutside
			return events, true
		}
		if lead != nil && lead.token == dsToolCallBegin {
			s.buffer = rest[len(lead.raw):]
			s.state = dsHeader
			return events, true
		}
		if lead != nil {
			// Any other template token in a section is structure: dropped.
			s.buffer = rest[len(lead.raw):]
			continue
		}
		if rest == "" || dsTemplatePartialLength(rest) == len(rest) {
			if !final {
				return events, false
			}
			// The stream ended on layout or mid-token: markup, not prose.
			s.buffer = ""
			return events, true
		}
		// Prose after a stray section open stays visible; trailing
		// whitespace waits until what follows decides.
		runEnd := len(s.buffer)
		if next := strings.Index(rest[1:], "<"); next != -1 {
			runEnd = indent + 1 + next
		}
		prose := strings.TrimRightFunc(s.buffer[:runEnd], isJSWhitespace)
		events = s.emitText(prose, events)
		s.buffer = s.buffer[len(prose):]
	}
	return events, final
}

func (s *deepseekScanner) consumeHeader(final bool) bool {
	sep := dsFindTemplateToken(s.buffer, dsSeparatorTokens, 0)
	if sep == nil {
		if final {
			s.resetTool(dsOutside)
		}
		return false
	}
	head := jsTrim(s.buffer[:sep.index])
	s.buffer = s.buffer[sep.index+len(sep.raw):]
	if head == dsLegacyToolType {
		s.state = dsLegacyName
		return true
	}
	s.name = head
	s.state = dsArgs
	return true
}

func (s *deepseekScanner) consumeLegacyName(final bool) bool {
	fence := strings.Index(s.buffer, dsLegacyJSONFence)
	if fence == -1 {
		if final {
			s.resetTool(dsOutside)
		}
		return false
	}
	s.name = jsTrim(s.buffer[:fence])
	s.buffer = s.buffer[fence+len(dsLegacyJSONFence):]
	if strings.HasPrefix(s.buffer, "\r\n") {
		s.buffer = s.buffer[2:]
	} else if strings.HasPrefix(s.buffer, "\n") {
		s.buffer = s.buffer[1:]
	}
	s.state = dsLegacyArgs
	return true
}

func (s *deepseekScanner) consumeArgs(final bool, events []scanEvent) ([]scanEvent, bool) {
	var end *dsTokenMatch
	rawArgs := ""
	for candidate := dsFindTemplateToken(s.buffer, dsArgsTerminators, 0); candidate != nil; candidate = dsFindTemplateToken(s.buffer, dsArgsTerminators, candidate.index+len(candidate.raw)) {
		rawArgs = s.buffer[:candidate.index]
		if s.state == dsLegacyArgs {
			if fence := strings.LastIndex(rawArgs, dsCodeFence); fence != -1 {
				rawArgs = rawArgs[:fence]
			}
		}
		// The call's canonical close always ends it; a re-spelled close and
		// the fallbacks only once the arguments are no longer an open JSON
		// prefix (inside one they are argument text).
		trimmed := jsTrim(rawArgs)
		if candidate.raw == dsToolCallEnd || trimmed == "" || cursorwire.ClassifyJSONPrefix(trimmed) != cursorwire.JSONPrefix {
			end = candidate
			break
		}
	}
	if end == nil {
		if final {
			s.resetTool(dsOutside)
		}
		return events, false
	}
	// Only the call's own close belongs to it; a section close or the next
	// call's open stays buffered for the state that owns it.
	consumed := end.index
	if end.token == dsToolCallEnd {
		consumed += len(end.raw)
	}
	args := &cursorwire.JSONObject{}
	if trimmed := jsTrim(rawArgs); trimmed != "" {
		args = parseToolArgs(trimmed)
	}
	events = append(events, scanEvent{kind: scanToolStart, name: s.name}, scanEvent{kind: scanToolEnd, name: s.name, args: args})
	s.buffer = s.buffer[consumed:]
	if s.inToolSection {
		s.resetTool(dsSection)
	} else {
		s.resetTool(dsOutside)
	}
	return events, true
}

func (s *deepseekScanner) consumeDSMLSection(final bool, events []scanEvent) ([]scanEvent, bool) {
	initial := s.buffer
	consumedSoFar := func() string { return initial[:len(initial)-len(s.buffer)] }
	for s.buffer != "" {
		s.buffer = strings.TrimLeftFunc(s.buffer, isJSWhitespace)
		if close := s.matchingDSMLClose(dsmlCallsCloseFull, dsmlCallsCloseASCII); close != "" {
			if s.pendingSection != nil {
				events = s.emitText(*s.pendingSection+consumedSoFar()+close, events)
				s.pendingSection = nil
			}
			s.buffer = s.buffer[len(close):]
			s.state = dsOutside
			return events, true
		}
		if name, _, ok := s.matchDSMLOpen("invoke"); ok {
			s.pendingSection = nil
			s.name = name
			s.dsmlArgs = &cursorwire.JSONObject{}
			events = append(events, scanEvent{kind: scanToolStart, name: name})
			s.state = dsmlInvoke
			return events, true
		}
		if !final {
			if (strings.HasPrefix(s.buffer, "<｜DSML｜invoke") || strings.HasPrefix(s.buffer, "<|DSML|invoke")) && !strings.Contains(s.buffer, ">") {
				break
			}
			if partialSuffixOverlapAny(s.buffer, dsmlSectionTokens) == len(s.buffer) {
				break
			}
		}
		if s.buffer == "" {
			break
		}
		_, size := utf8.DecodeRuneInString(s.buffer)
		s.buffer = s.buffer[size:]
	}
	if s.pendingSection != nil {
		pending := *s.pendingSection + consumedSoFar()
		s.pendingSection = &pending
	}
	return events, final
}

func (s *deepseekScanner) consumeDSMLInvoke(final bool, events []scanEvent) ([]scanEvent, bool) {
	for s.buffer != "" {
		s.buffer = strings.TrimLeftFunc(s.buffer, isJSWhitespace)
		if close := s.matchingDSMLClose(dsmlInvokeCloseFull, dsmlInvokeCloseASCI); close != "" {
			s.buffer = s.buffer[len(close):]
			events = append(events, scanEvent{kind: scanToolEnd, name: s.name, args: s.dsmlArgs})
			s.resetDSMLTool()
			s.state = dsmlSection
			return events, true
		}
		if name, stringAttr, ok := s.matchDSMLOpen("parameter"); ok {
			s.dsmlParamName = name
			s.dsmlParamString = stringAttr != "false"
			s.state = dsmlParam
			return events, true
		}
		if !final {
			if (strings.HasPrefix(s.buffer, "<｜DSML｜parameter") || strings.HasPrefix(s.buffer, "<|DSML|parameter")) && !strings.Contains(s.buffer, ">") {
				return events, false
			}
			if partialSuffixOverlapAny(s.buffer, dsmlInvokeTokens) == len(s.buffer) {
				return events, false
			}
		}
		if s.buffer == "" {
			break
		}
		_, size := utf8.DecodeRuneInString(s.buffer)
		s.buffer = s.buffer[size:]
	}
	return events, final
}

func (s *deepseekScanner) consumeDSMLParam(final bool) bool {
	close, at := dsEarliestToken(s.buffer, dsmlParamCloseTokens)
	if at == -1 {
		hold := 0
		if !final {
			hold = partialSuffixOverlapAny(s.buffer, dsmlParamCloseTokens)
		}
		s.dsmlParamRaw += s.buffer[:len(s.buffer)-hold]
		s.buffer = s.buffer[len(s.buffer)-hold:]
		if final {
			s.resetDSMLTool()
		}
		return false
	}
	s.dsmlParamRaw += s.buffer[:at]
	s.dsmlArgs.Set(s.dsmlParamName, dsCoerceDSMLValue(s.dsmlParamRaw, s.dsmlParamString))
	s.buffer = s.buffer[at+len(close):]
	s.dsmlParamName, s.dsmlParamString, s.dsmlParamRaw = "", true, ""
	s.state = dsmlInvoke
	return true
}

// findOutsideToken is #findOutsideToken: the earliest outside-state token,
// literal or re-spelled (ties keep the literal). A re-spelled token inside
// code the model quoted is prose; after an inline run whose close is still
// undecided, hold is the index to hold from (else -1).
func (s *deepseekScanner) findOutsideToken(final bool) (match *dsTokenMatch, hold int) {
	literalToken, literalAt := dsEarliestToken(s.buffer, dsOutsideTokens)
	template := dsFindTemplateToken(s.buffer, dsOutsideTemplate, 0)
	for template != nil && template.raw != template.token && (literalAt == -1 || template.index < literalAt) {
		code := dsAdvanceCodeState(s.code, s.buffer[:template.index])
		fence := dsSettleBacktickRun(code.fence, code.run)
		if fence == 0 {
			break
		}
		if fence < 3 {
			closes, decided := dsInlineSpanCloses(s.buffer[template.index+len(template.raw):], fence, final)
			if !decided {
				return nil, template.index
			}
			if !closes {
				break
			}
		}
		template = dsFindTemplateToken(s.buffer, dsOutsideTemplate, template.index+len(template.raw))
	}
	if template != nil && (literalAt == -1 || template.index < literalAt) {
		return template, -1
	}
	if literalAt == -1 {
		return nil, -1
	}
	return &dsTokenMatch{token: literalToken, raw: literalToken, index: literalAt}, -1
}

func (s *deepseekScanner) matchingDSMLClose(fullwidth, ascii string) string {
	switch {
	case strings.HasPrefix(s.buffer, fullwidth):
		return fullwidth
	case strings.HasPrefix(s.buffer, ascii):
		return ascii
	}
	return ""
}

// matchDSMLOpen consumes a complete `<｜DSML｜kind … name="…">` tag.
func (s *deepseekScanner) matchDSMLOpen(kind string) (name, stringAttr string, ok bool) {
	if !strings.HasPrefix(s.buffer, "<｜DSML｜"+kind) && !strings.HasPrefix(s.buffer, "<|DSML|"+kind) {
		return "", "", false
	}
	end := strings.Index(s.buffer, ">")
	if end == -1 {
		return "", "", false
	}
	tag := s.buffer[:end+1]
	nameMatch := dsDSMLNameAttr.FindStringSubmatch(tag)
	if nameMatch == nil {
		return "", "", false
	}
	if attr := dsDSMLStringAttr.FindStringSubmatch(tag); attr != nil {
		stringAttr = attr[1]
	}
	s.buffer = s.buffer[end+1:]
	return nameMatch[1], stringAttr, true
}

func (s *deepseekScanner) resetTool(next dsState) {
	s.state = next
	s.name = ""
}

func (s *deepseekScanner) resetDSMLTool() {
	s.name = ""
	s.dsmlArgs = nil
	s.dsmlParamName, s.dsmlParamString, s.dsmlParamRaw = "", true, ""
}

// dsEarliestToken is findEarliestToken: the earliest token (the longest on a
// tie) and its index, -1 when none occurs.
func dsEarliestToken(text string, tokens []string) (string, int) {
	best, bestToken := -1, ""
	for _, token := range tokens {
		i := strings.Index(text, token)
		if i == -1 {
			continue
		}
		if best == -1 || i < best || i == best && len(token) > len(bestToken) {
			best, bestToken = i, token
		}
	}
	return bestToken, best
}

// dsCoerceDSMLValue is coerceDsmlValue: a string="false" parameter is JSON.
func dsCoerceDSMLValue(raw string, isString bool) any {
	if isString {
		return raw
	}
	trimmed := jsTrim(raw)
	if trimmed == "" {
		return raw
	}
	if value, err := cursorwire.ParseJSONWithRepair(trimmed); err == nil {
		return value
	}
	return raw
}
