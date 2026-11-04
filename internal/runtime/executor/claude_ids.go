package executor

import (
	"strconv"
	"strings"
)

// neoPiNormalizeToolID mirrors ai/src/utils.ts's UTF-16 sanitizer. Custom
// same-model endpoints retain opaque IDs; the official API requires this shape.
func neoPiNormalizeToolID(id string) string {
	valid := len(id) > 0 && len(id) <= 64
	for index := 0; valid && index < len(id); index++ {
		char := id[index]
		valid = char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-'
	}
	if valid {
		return id
	}
	if id == "" {
		// transform-messages.ts fallback: "toolu_" + Bun.hash("").toString(36).
		return "toolu_27k1wwwhf13t"
	}
	var out strings.Builder
	out.Grow(min(len(id), 64))
	for _, char := range id {
		if out.Len() == 64 {
			break
		}
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-' {
			out.WriteByte(byte(char))
		} else {
			out.WriteByte('_')
			if char > 0xffff && out.Len() < 64 {
				out.WriteByte('_')
			}
		}
	}
	return out.String()
}

type neoPiToolIDState struct {
	normalized map[string]string
	seen       map[string]int
	pending    map[string][]string
	touched    map[string]bool
}

func (s *neoPiToolIDState) beginTurn() { clear(s.touched) }

func (s *neoPiToolIDState) call(original string, normalize bool) string {
	base := original
	if normalize {
		base = neoPiNormalizeToolID(original)
	}
	if base != original {
		if s.normalized == nil {
			s.normalized = make(map[string]string)
		}
		s.normalized[original] = base
	}
	if s.seen == nil {
		s.seen = make(map[string]int)
		s.pending = make(map[string][]string)
		s.touched = make(map[string]bool)
	}
	if !s.touched[base] {
		delete(s.pending, base)
		s.touched[base] = true
	}
	emitted := base
	count := s.seen[base]
	if count > 0 {
		for {
			suffix := "_dup" + strconv.Itoa(count)
			budget := max(0, 64-len(suffix))
			if !strings.Contains(base, "|") {
				emitted = base[:min(len(base), budget)] + suffix
			} else {
				segments := strings.Split(base, "|")
				for index, segment := range segments {
					segments[index] = segment[:min(len(segment), budget)] + suffix
				}
				emitted = strings.Join(segments, "|")
			}
			if s.seen[emitted] == 0 {
				break
			}
			count++
		}
		s.seen[emitted] = 1
	}
	s.seen[base] = count + 1
	s.pending[base] = append(s.pending[base], emitted)
	return emitted
}

func (s *neoPiToolIDState) result(original string) string {
	base := original
	if normalized, ok := s.normalized[original]; ok {
		base = normalized
	}
	pending := s.pending[base]
	if len(pending) == 0 {
		return base
	}
	id := pending[0]
	if len(pending) == 1 {
		delete(s.pending, base)
	} else {
		s.pending[base] = pending[1:]
	}
	return id
}
