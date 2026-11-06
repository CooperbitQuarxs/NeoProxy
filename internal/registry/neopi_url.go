package registry

import (
	"net"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/idna"
)

// neoPiWHATWGIDNA is the URL Standard's domain to ASCII with beStrict false:
// UTS #46 non-transitional processing with CheckBidi and CheckJoiners, and
// without CheckHyphens, UseSTD3ASCIIRules or VerifyDnsLength.
var neoPiWHATWGIDNA = idna.New(idna.MapForLookup(), idna.BidiRule(), idna.Transitional(false), idna.CheckHyphens(false),
	idna.CheckJoiners(true), idna.StrictDomainName(false), idna.VerifyDNSLength(false))

// neoPiURL is what NeoPi's runtime reads from a URL through the WHATWG
// parser (new URL): the lowercase scheme, URL.host (hostname plus a
// non-default port), URL.hostname, URL.port and URL.pathname.
type neoPiURL struct {
	scheme, host, hostname, port, path string
}

// neoPiParseWHATWG parses raw as new URL(raw) does, for the bases a local
// server's account configures: special schemes with any slashes or
// backslashes after the colon, userinfo dropped, percent-decoded and
// lowercased hosts, IPv4 in any of its number forms, IPv6 serialized
// compressed, default ports elided, and dot segments resolved. ok is false
// where new URL throws, and for what this port leaves out: file: URLs and
// percent-encoding a path's other characters.
func neoPiParseWHATWG(raw string) (neoPiURL, bool) {
	input := strings.TrimFunc(raw, func(r rune) bool { return r <= 0x20 })
	input = strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, input)
	colon := strings.IndexByte(input, ':')
	if colon <= 0 || !neoPiASCIIAlpha(input[0]) {
		return neoPiURL{}, false
	}
	for i := 1; i < colon; i++ {
		if c := input[i]; !neoPiASCIIAlpha(c) && !(c >= '0' && c <= '9') && c != '+' && c != '-' && c != '.' {
			return neoPiURL{}, false
		}
	}
	scheme, rest := strings.ToLower(input[:colon]), input[colon+1:]
	if end := strings.IndexAny(rest, "?#"); end >= 0 {
		rest = rest[:end]
	}
	switch scheme {
	case "file":
		return neoPiURL{}, false
	case "http", "https", "ws", "wss", "ftp":
	default:
		// A non-special URL: an opaque path, or an authority after "//".
		parsed := neoPiURL{scheme: scheme, path: rest}
		if strings.HasPrefix(rest, "//") {
			authority, path, _ := strings.Cut(rest[2:], "/")
			if at := strings.LastIndexByte(authority, '@'); at >= 0 {
				authority = authority[at+1:]
			}
			parsed.host, parsed.hostname, parsed.path = authority, authority, "/"+path
		}
		return parsed, true
	}
	rest = strings.TrimLeft(rest, `/\`)
	end := strings.IndexAny(rest, `/\`)
	if end < 0 {
		end = len(rest)
	}
	authority, tail := rest[:end], rest[end:]
	if at := strings.LastIndexByte(authority, '@'); at >= 0 {
		authority = authority[at+1:]
	}
	hostname, port, ok := neoPiWHATWGHostPort(scheme, authority)
	if !ok {
		return neoPiURL{}, false
	}
	host := hostname
	if port != "" {
		host += ":" + port
	}
	return neoPiURL{scheme: scheme, host: host, hostname: hostname, port: port, path: neoPiWHATWGPath(tail)}, true
}

func neoPiASCIIAlpha(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// neoPiWHATWGDefaultPort is a special scheme's default port.
func neoPiWHATWGDefaultPort(scheme string) int {
	switch scheme {
	case "http", "ws":
		return 80
	case "https", "wss":
		return 443
	case "ftp":
		return 21
	}
	return -1
}

// neoPiWHATWGHostPort is the host and port states of a special URL: the
// host before the first colon outside brackets, the port digits after it.
func neoPiWHATWGHostPort(scheme, authority string) (hostname, port string, ok bool) {
	hostPart, portPart := authority, ""
	if strings.HasPrefix(authority, "[") {
		end := strings.IndexByte(authority, ']')
		if end < 0 {
			return "", "", false
		}
		hostPart = authority[:end+1]
		if after := authority[end+1:]; after != "" {
			if after[0] != ':' {
				return "", "", false
			}
			portPart = after[1:]
		}
	} else if before, after, found := strings.Cut(authority, ":"); found {
		hostPart, portPart = before, after
	}
	if hostPart == "" {
		return "", "", false
	}
	if portPart != "" {
		for i := 0; i < len(portPart); i++ {
			if portPart[i] < '0' || portPart[i] > '9' {
				return "", "", false
			}
		}
		value := 0
		if digits := strings.TrimLeft(portPart, "0"); digits != "" {
			if len(digits) > 5 {
				return "", "", false
			}
			value, _ = strconv.Atoi(digits)
		}
		if value > 65535 {
			return "", "", false
		}
		if value != neoPiWHATWGDefaultPort(scheme) {
			port = strconv.Itoa(value)
		}
	}
	hostname, ok = neoPiWHATWGHost(hostPart)
	return hostname, port, ok
}

// neoPiWHATWGHost is the host parser for a special URL's host.
func neoPiWHATWGHost(input string) (string, bool) {
	if strings.HasPrefix(input, "[") {
		if !strings.HasSuffix(input, "]") {
			return "", false
		}
		return neoPiWHATWGIPv6(input[1 : len(input)-1])
	}
	decoded := neoPiPercentDecode(input)
	if !utf8.ValidString(decoded) {
		return "", false
	}
	// Domain to ASCII; its fast path lowercases an ASCII domain with no
	// punycode label.
	if neoPiNeedsIDNA(decoded) {
		ascii, err := neoPiWHATWGIDNA.ToASCII(decoded)
		if err != nil || ascii == "" {
			return "", false
		}
		decoded = ascii
	}
	for i := 0; i < len(decoded); i++ {
		c := decoded[i]
		if c <= 0x20 || c == 0x7f || strings.IndexByte("#%/:<>?@[\\]^|", c) >= 0 {
			return "", false
		}
	}
	domain := strings.ToLower(decoded)
	if domain == "" {
		return "", false
	}
	if neoPiEndsInNumber(domain) {
		return neoPiWHATWGIPv4(domain)
	}
	return domain, true
}

// neoPiNeedsIDNA reports a domain domain-to-ASCII must process: one with a
// non-ASCII code point or a label starting with xn-- (any case).
func neoPiNeedsIDNA(domain string) bool {
	for i := 0; i < len(domain); i++ {
		if domain[i] >= 0x80 {
			return true
		}
	}
	for _, label := range strings.Split(domain, ".") {
		if len(label) >= 4 && strings.EqualFold(label[:4], "xn--") {
			return true
		}
	}
	return false
}

// neoPiPercentDecode decodes %XX sequences, leaving malformed ones as they are.
func neoPiPercentDecode(input string) string {
	if !strings.Contains(input, "%") {
		return input
	}
	var out strings.Builder
	for i := 0; i < len(input); i++ {
		if input[i] == '%' && i+2 < len(input) {
			if value, err := strconv.ParseUint(input[i+1:i+3], 16, 8); err == nil {
				out.WriteByte(byte(value))
				i += 2
				continue
			}
		}
		out.WriteByte(input[i])
	}
	return out.String()
}

// neoPiEndsInNumber is the ends-in-a-number checker: the last label (a
// trailing empty one dropped) is all digits or an IPv4 number.
func neoPiEndsInNumber(domain string) bool {
	labels := strings.Split(domain, ".")
	if labels[len(labels)-1] == "" {
		if len(labels) == 1 {
			return false
		}
		labels = labels[:len(labels)-1]
	}
	last := labels[len(labels)-1]
	if last == "" {
		return false
	}
	if strings.Trim(last, "0123456789") == "" {
		return true
	}
	_, ok := neoPiIPv4Number(last)
	return ok
}

// neoPiWHATWGIPv4 is the IPv4 parser: up to four decimal, 0x hex or 0 octal
// numbers, the last filling the remaining bytes, serialized dotted.
func neoPiWHATWGIPv4(input string) (string, bool) {
	parts := strings.Split(input, ".")
	if parts[len(parts)-1] == "" && len(parts) > 1 {
		parts = parts[:len(parts)-1]
	}
	if len(parts) > 4 {
		return "", false
	}
	numbers := make([]uint64, len(parts))
	for i, part := range parts {
		value, ok := neoPiIPv4Number(part)
		if !ok {
			return "", false
		}
		numbers[i] = value
	}
	for _, value := range numbers[:len(numbers)-1] {
		if value > 255 {
			return "", false
		}
	}
	last := numbers[len(numbers)-1]
	if last >= uint64(1)<<(8*(5-len(numbers))) {
		return "", false
	}
	address := last
	for i, value := range numbers[:len(numbers)-1] {
		address += value << (8 * (3 - i))
	}
	return strconv.Itoa(int(address>>24)) + "." + strconv.Itoa(int(address>>16&255)) + "." +
		strconv.Itoa(int(address>>8&255)) + "." + strconv.Itoa(int(address&255)), true
}

// neoPiIPv4Number is the IPv4 number parser.
func neoPiIPv4Number(part string) (uint64, bool) {
	if part == "" {
		return 0, false
	}
	base := 10
	switch {
	case len(part) >= 2 && (part[:2] == "0x" || part[:2] == "0X"):
		base, part = 16, part[2:]
	case len(part) >= 2 && part[0] == '0':
		base, part = 8, part[1:]
	}
	if part == "" {
		return 0, true
	}
	value, err := strconv.ParseUint(part, base, 64)
	return value, err == nil
}

// neoPiWHATWGIPv6 parses an IPv6 address and serializes it as the standard
// does: lowercase hex pieces, the first longest run of two or more zero
// pieces compressed, never dotted.
func neoPiWHATWGIPv6(input string) (string, bool) {
	if !strings.Contains(input, ":") {
		return "", false
	}
	ip := net.ParseIP(input)
	if ip == nil || strings.Contains(input, "%") {
		return "", false
	}
	ip = ip.To16()
	var pieces [8]uint16
	for i := range pieces {
		pieces[i] = uint16(ip[2*i])<<8 | uint16(ip[2*i+1])
	}
	runStart, runLength := -1, 1
	for i := 0; i < 8; {
		if pieces[i] != 0 {
			i++
			continue
		}
		j := i
		for j < 8 && pieces[j] == 0 {
			j++
		}
		if j-i > runLength {
			runStart, runLength = i, j-i
		}
		i = j
	}
	var out strings.Builder
	out.WriteByte('[')
	for i := 0; i < 8; i++ {
		if i == runStart {
			if i == 0 {
				out.WriteByte(':')
			}
			out.WriteByte(':')
			i += runLength - 1
			continue
		}
		out.WriteString(strconv.FormatUint(uint64(pieces[i]), 16))
		if i < 7 {
			out.WriteByte(':')
		}
	}
	out.WriteByte(']')
	return out.String(), true
}

// neoPiWHATWGPath is a special URL's pathname: backslashes as slashes, "."
// and ".." segments (percent-encoded too) resolved, "/" when empty.
func neoPiWHATWGPath(tail string) string {
	tail = strings.ReplaceAll(tail, `\`, "/")
	if tail == "" {
		return "/"
	}
	segments := strings.Split(strings.TrimPrefix(tail, "/"), "/")
	out := make([]string, 0, len(segments))
	for i, segment := range segments {
		last := i == len(segments)-1
		switch strings.ToLower(segment) {
		case "..", ".%2e", "%2e.", "%2e%2e":
			if len(out) > 0 {
				out = out[:len(out)-1]
			}
			if last {
				out = append(out, "")
			}
		case ".", "%2e":
			if last {
				out = append(out, "")
			}
		default:
			out = append(out, segment)
		}
	}
	return "/" + strings.Join(out, "/")
}
