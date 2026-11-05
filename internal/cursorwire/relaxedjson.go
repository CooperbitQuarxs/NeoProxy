package cursorwire

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
)

// Ports of pi-utils json-parse.ts / json-lexer.ts (strict mode) and
// pi-ai utils/tool-call-arguments.ts. Text is scanned as UTF-16 code units so
// positions in diagnostics match JavaScript's.

const (
	jsonQuote     = 0x22
	jsonSQuote    = 0x27
	jsonBackslash = 0x5c
	jsonSlash     = 0x2f
	jsonColon     = 0x3a
	jsonComma     = 0x2c
	jsonLBrace    = 0x7b
	jsonRBrace    = 0x7d
	jsonLBracket  = 0x5b
	jsonRBracket  = 0x5d
)

// errSyntax is a JavaScript SyntaxError; its message is the diagnostic.
type errSyntax string

func (e errSyntax) Error() string { return string(e) }

func isHexDigit(cp int) bool {
	return (cp >= 0x30 && cp <= 0x39) || ((cp|0x20) >= 0x61 && (cp|0x20) <= 0x66)
}

func isJSONWhitespace(cp int) bool { return cp == 0x20 || cp == 0x09 || cp == 0x0a || cp == 0x0d }

func isIdentChar(cp int) bool {
	return (cp >= 0x30 && cp <= 0x39) || ((cp|0x20) >= 0x61 && (cp|0x20) <= 0x7a) || cp == 0x5f || cp == 0x24
}

func isNumberStart(cp int) bool {
	return cp == 0x2d || cp == 0x2b || cp == 0x2e || (cp >= 0x30 && cp <= 0x39)
}

func isNumberChar(cp int) bool {
	return (cp >= 0x30 && cp <= 0x39) || cp == 0x2d || cp == 0x2b || cp == 0x2e ||
		((cp|0x20) >= 0x61 && (cp|0x20) <= 0x66) || (cp|0x20) == 0x78
}

func validEscapeChar(cp int) bool { return cp < 128 && strings.ContainsRune(`"\/bfnrtu`, rune(cp)) }

var relaxedKeywords = []struct {
	word  string
	value any
}{{"true", true}, {"false", false}, {"null", nil}, {"True", true}, {"False", false}, {"None", nil}}

var nonRecoverableBarewords = map[string]bool{"NaN": true, "Infinity": true, "-Infinity": true, "+Infinity": true, "undefined": true}

// jsonLexer is JsonLexer in strict mode.
type jsonLexer struct {
	src []uint16
	pos int
}

func (l *jsonLexer) at(i int) int {
	if i < 0 || i >= len(l.src) {
		return -1
	}
	return int(l.src[i])
}

func (l *jsonLexer) atEnd() bool { return l.pos >= len(l.src) }
func (l *jsonLexer) peek() int   { return l.at(l.pos) }
func (l *jsonLexer) ws()         { l.pos = l.skipInsignificant(l.pos) }

func (l *jsonLexer) text(from, to int) string { return string(utf16.Decode(l.src[from:to])) }

// skipInsignificant skips whitespace and // or /* */ comments.
func (l *jsonLexer) skipInsignificant(i int) int {
	n := len(l.src)
	for {
		for i < n && isJSONWhitespace(int(l.src[i])) {
			i++
		}
		if i+1 < n && l.src[i] == jsonSlash {
			next := l.src[i+1]
			if next == jsonSlash {
				i += 2
				for i < n && l.src[i] != 0x0a {
					i++
				}
				continue
			}
			if next == 0x2a {
				i += 2
				for i+1 < n && !(l.src[i] == 0x2a && l.src[i+1] == jsonSlash) {
					i++
				}
				i = min(i+2, n)
				continue
			}
		}
		return i
	}
}

func hex4(s []uint16, pos int) int {
	if pos+4 > len(s) {
		return -1
	}
	value := 0
	for k := pos; k < pos+4; k++ {
		cp := int(s[k])
		if !isHexDigit(cp) {
			return -1
		}
		digit, _ := strconv.ParseInt(string(rune(cp)), 16, 32)
		value = value<<4 | int(digit)
	}
	return value
}

// quoteCloses is #quoteLookahead in strict mode: a quote closes only when a
// value terminator (or the end of input) follows.
func (l *jsonLexer) quoteCloses(from int) bool {
	k := l.skipInsignificant(from)
	if k >= len(l.src) {
		return true
	}
	c := l.src[k]
	return c == jsonComma || c == jsonRBrace || c == jsonRBracket || c == jsonColon
}

func (l *jsonLexer) string(quote int) (string, error) {
	s := l.src
	n := len(s)
	i := l.pos + 1
	var out []uint16
	runStart := i
	lenient := quote == jsonSQuote
	for i < n {
		cc := int(s[i])
		if cc != jsonBackslash && cc != quote {
			i++
			continue
		}
		if cc == quote {
			if !lenient || l.quoteCloses(i+1) {
				out = append(out, s[runStart:i]...)
				l.pos = i + 1
				return string(utf16.Decode(out)), nil
			}
			i++
			continue
		}
		out = append(out, s[runStart:i]...)
		i++
		if i >= n {
			out = append(out, '\\')
			runStart = i
			break
		}
		switch esc := int(s[i]); esc {
		case jsonQuote:
			out = append(out, '"')
		case jsonSQuote:
			out = append(out, '\'')
		case jsonBackslash:
			out = append(out, '\\')
		case jsonSlash:
			out = append(out, '/')
		case 0x62:
			out = append(out, '\b')
		case 0x66:
			out = append(out, '\f')
		case 0x6e:
			out = append(out, '\n')
		case 0x72:
			out = append(out, '\r')
		case 0x74:
			out = append(out, '\t')
		case 0x75:
			if unit := hex4(s, i+1); unit >= 0 {
				i += 4
				out = append(out, uint16(unit))
			} else {
				out = append(out, '\\', 'u')
			}
		default:
			out = append(out, '\\', s[i])
		}
		i++
		runStart = i
	}
	return "", errSyntax("Unterminated string")
}

var (
	jsDecimalNumber = regexp.MustCompile(`^[+-]?(?:[0-9]+\.?[0-9]*|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)
	jsRadixNumber   = regexp.MustCompile(`^0(?:[xX][0-9a-fA-F]+|[bB][01]+)$`)
)

// jsNumber is Number(token) for the characters isNumberChar admits.
func jsNumber(token string) float64 {
	switch {
	case jsDecimalNumber.MatchString(token):
		value, err := strconv.ParseFloat(token, 64)
		if err != nil {
			var numErr *strconv.NumError
			if errors.As(err, &numErr) && errors.Is(numErr.Err, strconv.ErrRange) {
				return value
			}
			return math.NaN()
		}
		return value
	case jsRadixNumber.MatchString(token):
		base := 16
		if token[1] == 'b' || token[1] == 'B' {
			base = 2
		}
		value, ok := new(big.Int).SetString(token[2:], base)
		if !ok {
			return math.NaN()
		}
		f, _ := new(big.Float).SetInt(value).Float64()
		return f
	}
	return math.NaN()
}

func (l *jsonLexer) number() (float64, error) {
	start := l.pos
	i := start
	for i < len(l.src) && isNumberChar(int(l.src[i])) {
		i++
	}
	l.pos = i
	token := l.text(start, i)
	value := jsNumber(token)
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, errSyntax("Invalid number: " + token)
	}
	return value, nil
}

func (l *jsonLexer) keyword() (any, bool) {
	for _, kw := range relaxedKeywords {
		end := l.pos + len(kw.word)
		if end > len(l.src) || l.text(l.pos, end) != kw.word || isIdentChar(l.at(end)) {
			continue
		}
		l.pos = end
		return kw.value, true
	}
	return nil, false
}

func (l *jsonLexer) unquotedKey() string {
	start := l.pos
	i := start
	for i < len(l.src) {
		cc := int(l.src[i])
		if cc == jsonColon || cc == jsonComma || cc == jsonRBrace || isJSONWhitespace(cc) {
			break
		}
		i++
	}
	l.pos = i
	return l.text(start, i)
}

func (l *jsonLexer) bareword() (string, error) {
	start := l.pos
	i := start
	unexpected := errSyntax(fmt.Sprintf("Unexpected token at position %d", start))
	for i < len(l.src) {
		cc := int(l.src[i])
		if cc == jsonComma || cc == jsonRBrace || cc == jsonRBracket || cc == 0x0a || cc == 0x0d {
			break
		}
		if cc == jsonQuote || cc == jsonLBrace || cc == jsonLBracket ||
			(cc == jsonColon && l.at(i+1) != jsonSlash && l.at(i+1) != jsonBackslash) {
			return "", unexpected
		}
		i++
	}
	if i >= len(l.src) {
		return "", unexpected
	}
	end := i
	for end > start && isJSONWhitespace(int(l.src[end-1])) {
		end--
	}
	word := l.text(start, end)
	if nonRecoverableBarewords[word] {
		return "", unexpected
	}
	l.pos = i
	return word, nil
}

// relaxedParse is RelaxedJson in strict (final-parse) mode.
func relaxedParse(source string) (any, error) {
	l := &jsonLexer{src: utf16.Encode([]rune(source))}
	l.ws()
	if l.atEnd() {
		return nil, errSyntax("Unexpected end of JSON input")
	}
	value, err := l.value(false)
	if err != nil {
		return nil, err
	}
	l.ws()
	if !l.atEnd() {
		return nil, errSyntax(fmt.Sprintf("Unexpected trailing characters at position %d", l.pos))
	}
	return value, nil
}

func (l *jsonLexer) value(allowBareword bool) (any, error) {
	c := l.peek()
	switch {
	case c == jsonLBrace:
		return l.object()
	case c == jsonLBracket:
		return l.array()
	case c == jsonQuote || c == jsonSQuote:
		return l.string(c)
	case c >= 0 && isNumberStart(c):
		return l.number()
	}
	if keyword, ok := l.keyword(); ok {
		return keyword, nil
	}
	if allowBareword {
		return l.bareword()
	}
	return nil, errSyntax(fmt.Sprintf("Unexpected token at position %d", l.pos))
}

func (l *jsonLexer) object() (any, error) {
	l.pos++
	out := &JSONObject{}
	for {
		l.ws()
		if l.atEnd() {
			return nil, errSyntax("Unterminated object")
		}
		c := l.peek()
		if c == jsonRBrace {
			l.pos++
			return out, nil
		}
		if c == jsonComma {
			l.pos++
			continue
		}
		key, err := l.key()
		if err != nil {
			return nil, err
		}
		l.ws()
		if l.peek() != jsonColon {
			return nil, errSyntax("Expected ':' in object")
		}
		l.pos++
		l.ws()
		if l.atEnd() {
			return nil, errSyntax("Expected value after ':'")
		}
		value, err := l.value(true)
		if err != nil {
			return nil, err
		}
		out.Set(key, value)
		l.ws()
		switch l.peek() {
		case jsonComma:
			l.pos++
			continue
		case jsonRBrace:
			l.pos++
			return out, nil
		}
		return nil, errSyntax("Expected ',' or '}' in object")
	}
}

func (l *jsonLexer) array() (any, error) {
	l.pos++
	out := []any{}
	for {
		l.ws()
		if l.atEnd() {
			return nil, errSyntax("Unterminated array")
		}
		c := l.peek()
		if c == jsonRBracket {
			l.pos++
			return out, nil
		}
		if c == jsonComma {
			l.pos++
			continue
		}
		value, err := l.value(true)
		if err != nil {
			return nil, err
		}
		out = append(out, value)
		l.ws()
		switch l.peek() {
		case jsonComma:
			l.pos++
			continue
		case jsonRBracket:
			l.pos++
			return out, nil
		}
		return nil, errSyntax("Expected ',' or ']' in array")
	}
}

func (l *jsonLexer) key() (string, error) {
	c := l.peek()
	if c == jsonQuote || c == jsonSQuote {
		return l.string(c)
	}
	key := l.unquotedKey()
	if key == "" {
		return "", errSyntax("Expected object key")
	}
	return key, nil
}

// ParseJSONWithRepair is parseJsonWithRepair: JSON.parse, then the relaxed
// grammar; an error carries the relaxed parser's SyntaxError message.
func ParseJSONWithRepair(text string) (any, error) {
	if value, ok := ParseJSON(text); ok {
		return value, nil
	}
	return relaxedParse(text)
}

// invalidArgumentsRawLimit is INVALID_ARGUMENTS_RAW_LIMIT.
const invalidArgumentsRawLimit = 512

// ParseToolCallArguments is parseToolCallArguments: final tool arguments,
// {} for an empty buffer and a bounded diagnostic for an unrepairable one.
func ParseToolCallArguments(text string) any {
	if strings.TrimSpace(text) == "" {
		return &JSONObject{}
	}
	value, err := ParseJSONWithRepair(text)
	if err == nil {
		return value
	}
	raw := text
	if units := utf16.Encode([]rune(raw)); len(units) > invalidArgumentsRawLimit {
		raw = fmt.Sprintf("%s… [truncated %d chars]", string(utf16.Decode(units[:invalidArgumentsRawLimit])), len(units)-invalidArgumentsRawLimit)
	}
	out := &JSONObject{}
	out.Set("__parseError", err.Error())
	out.Set("__rawJson", raw)
	return out
}

// JSONPrefixState is classifyJsonPrefix's verdict.
type JSONPrefixState string

const (
	JSONComplete JSONPrefixState = "complete"
	JSONPrefix   JSONPrefixState = "prefix"
	JSONInvalid  JSONPrefixState = "invalid"
)

// ClassifyJSONPrefix is classifyJsonPrefix: whether text is a strict JSON
// value, a prefix of one, or a dead end.
func ClassifyJSONPrefix(text string) JSONPrefixState {
	s := utf16.Encode([]rune(text))
	n := len(s)
	i := 0
	var stack []bool
	const (
		expValue = iota
		expObjKeyOrEnd
		expObjKey
		expObjColon
		expObjCommaOrEnd
		expArrValueOrEnd
		expArrCommaOrEnd
		expEnd
	)
	expect := expValue
	at := func(k int) int {
		if k < n {
			return int(s[k])
		}
		return 0
	}
	scanString := func() int {
		i++
		for i < n {
			c := int(s[i])
			if c == jsonQuote {
				i++
				return 1
			}
			if c == jsonBackslash {
				i++
				if i >= n {
					return 0
				}
				e := int(s[i])
				if !validEscapeChar(e) {
					return -1
				}
				i++
				if e == 0x75 {
					for k := 0; k < 4; k, i = k+1, i+1 {
						if i >= n {
							return 0
						}
						if !isHexDigit(int(s[i])) {
							return -1
						}
					}
				}
				continue
			}
			if c < 0x20 {
				return -1
			}
			i++
		}
		return 0
	}
	digit := func(k int) bool { return k < n && s[k] >= 0x30 && s[k] <= 0x39 }
	scanNumber := func() int {
		if at(i) == 0x2d {
			i++
		}
		if i >= n {
			return 0
		}
		switch c := at(i); {
		case c == 0x30:
			i++
		case c >= 0x31 && c <= 0x39:
			for digit(i) {
				i++
			}
		default:
			return -1
		}
		if i < n && s[i] == 0x2e {
			i++
			if i >= n {
				return 0
			}
			if !digit(i) {
				return -1
			}
			for digit(i) {
				i++
			}
		}
		if c := at(i); i < n && (c == 0x65 || c == 0x45) {
			i++
			if i < n && (s[i] == 0x2b || s[i] == 0x2d) {
				i++
			}
			if i >= n {
				return 0
			}
			if !digit(i) {
				return -1
			}
			for digit(i) {
				i++
			}
		}
		return 1
	}
	scanKeyword := func() int {
		for _, word := range []string{"true", "false", "null"} {
			if int(word[0]) != at(i) {
				continue
			}
			available := min(len(word), n-i)
			if !strings.HasPrefix(word, string(utf16.Decode(s[i:i+available]))) {
				return -1
			}
			i += available
			if available == len(word) {
				return 1
			}
			return 0
		}
		return -1
	}
	valueDone := func() int {
		switch {
		case len(stack) == 0:
			return expEnd
		case stack[len(stack)-1]:
			return expObjCommaOrEnd
		}
		return expArrCommaOrEnd
	}
	for i < n {
		c := int(s[i])
		if isJSONWhitespace(c) {
			i++
			continue
		}
		switch expect {
		case expValue, expArrValueOrEnd:
			if c == jsonRBracket && expect == expArrValueOrEnd {
				stack = stack[:len(stack)-1]
				i++
				expect = valueDone()
				continue
			}
			if c == jsonLBrace {
				stack = append(stack, true)
				i++
				expect = expObjKeyOrEnd
				continue
			}
			if c == jsonLBracket {
				stack = append(stack, false)
				i++
				expect = expArrValueOrEnd
				continue
			}
			var r int
			switch {
			case c == jsonQuote:
				r = scanString()
			case c == 0x2d || (c >= 0x30 && c <= 0x39):
				r = scanNumber()
			case c == 0x74 || c == 0x66 || c == 0x6e:
				r = scanKeyword()
			default:
				return JSONInvalid
			}
			if r == -1 {
				return JSONInvalid
			}
			if r == 0 {
				return JSONPrefix
			}
			expect = valueDone()
		case expObjKeyOrEnd, expObjKey:
			if c == jsonRBrace && expect == expObjKeyOrEnd {
				stack = stack[:len(stack)-1]
				i++
				expect = valueDone()
				continue
			}
			if c != jsonQuote {
				return JSONInvalid
			}
			switch scanString() {
			case -1:
				return JSONInvalid
			case 0:
				return JSONPrefix
			}
			expect = expObjColon
		case expObjColon:
			if c != jsonColon {
				return JSONInvalid
			}
			i++
			expect = expValue
		case expObjCommaOrEnd:
			if c == jsonRBrace {
				stack = stack[:len(stack)-1]
				i++
				expect = valueDone()
				continue
			}
			if c != jsonComma {
				return JSONInvalid
			}
			i++
			expect = expObjKey
		case expArrCommaOrEnd:
			if c == jsonRBracket {
				stack = stack[:len(stack)-1]
				i++
				expect = valueDone()
				continue
			}
			if c != jsonComma {
				return JSONInvalid
			}
			i++
			expect = expValue
		case expEnd:
			return JSONInvalid
		}
	}
	if expect == expEnd {
		return JSONComplete
	}
	return JSONPrefix
}
