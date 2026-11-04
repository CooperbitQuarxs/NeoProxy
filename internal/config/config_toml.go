package config

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
	"gopkg.in/yaml.v3"
)

// TOML and YAML differ in what they can express. The conversions below map:
//   - YAML null: TOML has no null, so a null mapping value is omitted (absence
//     means the default). The loader treats a null scalar or struct like an
//     absent key, but null clears a list, map or pointer that has a default and
//     reaches plugin settings as a value; a conversion that would change the
//     decoded Config that way fails naming the keys. A null array element
//     cannot be omitted without shifting its siblings and is an error.
//   - YAML anchors, aliases and merge keys: expanded into copies.
//   - TOML datetimes: RFC 3339 strings (every config field that holds a time is
//     a string).
//   - Order: TOML requires a table's plain keys before its sub-tables, so those
//     keys move ahead of sibling sections. Comments travel with their keys.
//   - Comments: head and line comments of keys, tables, array-of-table entries
//     and multi-line array elements are kept in both directions.

// TOMLToYAML converts a TOML config document to the equivalent YAML document,
// keeping key order and comments.
func TOMLToYAML(data []byte) ([]byte, error) {
	var decoded map[string]any
	if err := toml.Unmarshal(data, &decoded); err != nil {
		return nil, tomlError(err)
	}
	layout, err := scanTOMLLayout(data)
	if err != nil {
		return nil, tomlError(err)
	}
	root, err := layout.yamlNode(nil, decoded)
	if err != nil {
		return nil, err
	}
	doc := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}
	doc.FootComment = strings.Join(layout.docFoot, "\n")
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err = enc.Encode(doc); err != nil {
		_ = enc.Close()
		return nil, err
	}
	if err = enc.Close(); err != nil {
		return nil, err
	}
	// Comment indentation is kept: it tells the YAML parser which key a comment
	// belongs to.
	return buf.Bytes(), nil
}

// tomlError adds the line and column of a TOML syntax error to its message.
func tomlError(err error) error {
	var decodeErr *toml.DecodeError
	if errors.As(err, &decodeErr) {
		row, column := decodeErr.Position()
		return fmt.Errorf("toml: line %d column %d: %s", row, column, decodeErr.Error())
	}
	var parserErr *unstable.ParserError
	if errors.As(err, &parserErr) {
		return fmt.Errorf("toml: %s", parserErr.Error())
	}
	return err
}

// tomlLayout records what toml.Unmarshal discards: the order keys appear in and
// the comments attached to them. Paths are joined key segments; array elements
// use "#<index>" segments.
type tomlLayout struct {
	order   map[string][]string
	known   map[string]map[string]bool
	head    map[string][]string
	line    map[string]string
	foot    map[string][]string
	arrays  map[string]int
	docFoot []string
}

const tomlPathSeparator = "\x1f"

func tomlPathKey(path []string) string { return strings.Join(path, tomlPathSeparator) }

func tomlChildPath(path []string, segment string) []string {
	out := make([]string, len(path)+1)
	copy(out, path)
	out[len(path)] = segment
	return out
}

func tomlIndexSegment(index int) string { return "#" + strconv.Itoa(index) }

// tomlComment is a standalone comment line and whether an empty line borders it.
type tomlComment struct {
	text        string
	blankBefore bool
	blankAfter  bool
}

func scanTOMLLayout(data []byte) (*tomlLayout, error) {
	l := &tomlLayout{
		order:  map[string][]string{},
		known:  map[string]map[string]bool{},
		head:   map[string][]string{},
		line:   map[string]string{},
		foot:   map[string][]string{},
		arrays: map[string]int{},
	}
	parser := unstable.Parser{KeepComments: true}
	parser.Reset(data)
	var current, previous []string
	var pending []tomlComment
	// attach assigns the comments gathered since the previous item the way YAML
	// does: a block directly below the previous item and followed by an empty
	// line is its foot comment; the rest heads the next item, with empty lines
	// between blocks kept.
	attach := func() []string {
		rest := pending
		pending = nil
		if previous != nil && len(rest) > 0 && !rest[0].blankBefore {
			end := 0
			for end < len(rest) && !rest[end].blankAfter {
				end++
			}
			if end < len(rest) {
				previousKey := tomlPathKey(previous)
				for _, c := range rest[:end+1] {
					l.foot[previousKey] = append(l.foot[previousKey], c.text)
				}
				rest = rest[end+1:]
			}
		}
		var head []string
		if previous != nil && len(rest) > 0 && rest[0].blankBefore {
			// A leading empty entry makes the YAML encoder keep the empty line.
			head = append(head, "")
		}
		for i, c := range rest {
			head = append(head, c.text)
			if c.blankAfter && i < len(rest)-1 {
				head = append(head, "")
			}
		}
		return head
	}
	for parser.NextExpression() {
		expr := parser.Expression()
		switch expr.Kind {
		case unstable.Comment:
			start := int(expr.Raw.Offset)
			pending = append(pending, tomlComment{
				text:        tomlCommentText(expr.Data),
				blankBefore: tomlBlankLineBefore(data, start),
				blankAfter:  tomlBlankLineAfter(data, start+int(expr.Raw.Length)),
			})
		case unstable.KeyValue:
			keys := tomlKeyParts(expr.Key())
			path := current
			for _, key := range keys {
				l.add(path, key)
				path = tomlChildPath(path, key)
			}
			l.comment(path, attach(), tomlTrailingComment(expr))
			l.scanValue(path, expr.Value())
			previous = path
		case unstable.Table, unstable.ArrayTable:
			head := attach()
			current = l.resolveTable(tomlKeyParts(expr.Key()), expr.Kind == unstable.ArrayTable)
			l.comment(current, head, tomlTrailingComment(expr))
			previous = current
		}
	}
	if err := parser.Error(); err != nil {
		return nil, err
	}
	for _, c := range pending {
		l.docFoot = append(l.docFoot, c.text)
	}
	return l, nil
}

// tomlBlankLineBefore reports whether the line above the one holding offset is
// empty or offset is on the first line.
func tomlBlankLineBefore(data []byte, offset int) bool {
	lineStart := bytes.LastIndexByte(data[:offset], '\n')
	if lineStart < 0 {
		return true
	}
	previousStart := bytes.LastIndexByte(data[:lineStart], '\n') + 1
	return len(bytes.TrimSpace(data[previousStart:lineStart])) == 0
}

// tomlBlankLineAfter reports whether the line below the one ending at offset is
// empty or absent.
func tomlBlankLineAfter(data []byte, offset int) bool {
	lineEnd := bytes.IndexByte(data[offset:], '\n')
	if lineEnd < 0 {
		return true
	}
	next := data[offset+lineEnd+1:]
	if nextEnd := bytes.IndexByte(next, '\n'); nextEnd >= 0 {
		next = next[:nextEnd]
	}
	return len(bytes.TrimSpace(next)) == 0
}

// resolveTable returns the path a table header opens. A segment naming an array
// of tables refers to its latest element; an [[array]] header appends one.
func (l *tomlLayout) resolveTable(keys []string, arrayTable bool) []string {
	var path []string
	for i, key := range keys {
		l.add(path, key)
		path = tomlChildPath(path, key)
		pathKey := tomlPathKey(path)
		if arrayTable && i == len(keys)-1 {
			index := l.arrays[pathKey]
			l.arrays[pathKey] = index + 1
			path = tomlChildPath(path, tomlIndexSegment(index))
			continue
		}
		if count, ok := l.arrays[pathKey]; ok && count > 0 {
			path = tomlChildPath(path, tomlIndexSegment(count-1))
		}
	}
	return path
}

func (l *tomlLayout) scanValue(path []string, value *unstable.Node) {
	switch value.Kind {
	case unstable.InlineTable:
		children := value.Children()
		for children.Next() {
			kv := children.Node()
			if kv.Kind != unstable.KeyValue {
				continue
			}
			child := path
			for _, key := range tomlKeyParts(kv.Key()) {
				l.add(child, key)
				child = tomlChildPath(child, key)
			}
			l.scanValue(child, kv.Value())
		}
	case unstable.Array:
		index := 0
		var pending []string
		children := value.Children()
		for children.Next() {
			element := children.Node()
			if element.Kind == unstable.Comment {
				pending = append(pending, tomlCommentText(element.Data))
				for nested := element.Child(); nested != nil; nested = nested.Next() {
					pending = append(pending, tomlCommentText(nested.Data))
				}
				continue
			}
			elementPath := tomlChildPath(path, tomlIndexSegment(index))
			l.comment(elementPath, pending, "")
			pending = nil
			l.scanValue(elementPath, element)
			index++
		}
	}
}

func (l *tomlLayout) add(path []string, key string) {
	pathKey := tomlPathKey(path)
	seen := l.known[pathKey]
	if seen == nil {
		seen = map[string]bool{}
		l.known[pathKey] = seen
	}
	if !seen[key] {
		seen[key] = true
		l.order[pathKey] = append(l.order[pathKey], key)
	}
}

func (l *tomlLayout) comment(path []string, head []string, line string) {
	pathKey := tomlPathKey(path)
	if len(head) > 0 {
		l.head[pathKey] = append(l.head[pathKey], head...)
	}
	if line != "" {
		l.line[pathKey] = line
	}
}

func tomlKeyParts(it unstable.Iterator) []string {
	var parts []string
	for it.Next() {
		parts = append(parts, string(it.Node().Data))
	}
	return parts
}

func tomlTrailingComment(expr *unstable.Node) string {
	if next := expr.Next(); next != nil && next.Kind == unstable.Comment {
		return tomlCommentText(next.Data)
	}
	return ""
}

func tomlCommentText(data []byte) string {
	return strings.TrimRight(string(data), " \t\r")
}

// yamlNode builds the YAML node for the decoded TOML value at path.
func (l *tomlLayout) yamlNode(path []string, value any) (*yaml.Node, error) {
	switch v := value.(type) {
	case map[string]any:
		node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, key := range l.orderedKeys(path, v) {
			child := tomlChildPath(path, key)
			keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
			valueNode, err := l.yamlNode(child, v[key])
			if err != nil {
				return nil, err
			}
			childKey := tomlPathKey(child)
			keyNode.HeadComment = strings.Join(l.head[childKey], "\n")
			keyNode.FootComment = strings.Join(l.foot[childKey], "\n")
			if line := l.line[childKey]; line != "" {
				if valueNode.Kind == yaml.ScalarNode {
					valueNode.LineComment = line
				} else {
					keyNode.LineComment = line
				}
			}
			node.Content = append(node.Content, keyNode, valueNode)
		}
		return node, nil
	case []any:
		node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for i, element := range v {
			child := tomlChildPath(path, tomlIndexSegment(i))
			elementNode, err := l.yamlNode(child, element)
			if err != nil {
				return nil, err
			}
			childKey := tomlPathKey(child)
			elementNode.HeadComment = strings.Join(l.head[childKey], "\n")
			elementNode.LineComment = l.line[childKey]
			elementNode.FootComment = strings.Join(l.foot[childKey], "\n")
			node.Content = append(node.Content, elementNode)
		}
		return node, nil
	case string:
		node := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
		if strings.Contains(v, "\n") {
			node.Style = yaml.LiteralStyle
		}
		return node, nil
	case bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(v)}, nil
	case int64:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.FormatInt(v, 10)}, nil
	case float64:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: yamlFloat(v)}, nil
	case time.Time:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v.Format(time.RFC3339Nano)}, nil
	case toml.LocalDate, toml.LocalTime, toml.LocalDateTime:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: fmt.Sprint(v)}, nil
	default:
		return nil, fmt.Errorf("toml: unsupported value %T at %s", value, strings.Join(path, "."))
	}
}

// orderedKeys returns the keys of m in document order. Keys the layout scan did
// not see, which a valid document cannot produce, follow in sorted order.
func (l *tomlLayout) orderedKeys(path []string, m map[string]any) []string {
	keys := make([]string, 0, len(m))
	listed := make(map[string]bool, len(m))
	for _, key := range l.order[tomlPathKey(path)] {
		if _, ok := m[key]; ok && !listed[key] {
			keys = append(keys, key)
			listed[key] = true
		}
	}
	var rest []string
	for key := range m {
		if !listed[key] {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	return append(keys, rest...)
}

func yamlFloat(f float64) string {
	switch {
	case math.IsNaN(f):
		return ".nan"
	case math.IsInf(f, 1):
		return ".inf"
	case math.IsInf(f, -1):
		return "-.inf"
	}
	return finiteFloatText(f)
}

// finiteFloatText formats f so it reads back as a float in TOML and YAML.
func finiteFloatText(f float64) string {
	abs := math.Abs(f)
	var text string
	if abs == 0 || (abs >= 1e-4 && abs < 1e16) {
		text = strconv.FormatFloat(f, 'f', -1, 64)
	} else {
		text = strconv.FormatFloat(f, 'e', -1, 64)
	}
	if !strings.ContainsAny(text, ".e") {
		text += ".0"
	}
	return text
}

// YAMLToTOML converts a YAML config document to TOML, keeping key order where
// TOML allows and comments. TOML has no null, so null mapping values are left
// out; when leaving one out changes how the document decodes as a Config (null
// clears a defaulted list, map or pointer, or is a plugin setting value), it
// fails naming the dropped keys.
func YAMLToTOML(data []byte) ([]byte, error) {
	out, dropped, err := renderTOML(data)
	if err != nil {
		return nil, err
	}
	if len(dropped) > 0 {
		if err = verifyDroppedNulls(data, out, dropped); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// verifyDroppedNulls checks that the TOML rendering of yamlData decodes to the
// same Config. Documents that do not decode as a Config are not compared.
func verifyDroppedNulls(yamlData, tomlData []byte, dropped []string) error {
	var before Config
	applyLoadDefaults(&before)
	if yaml.Unmarshal(yamlData, &before) != nil {
		return nil
	}
	converted, err := TOMLToYAML(tomlData)
	if err != nil {
		return err
	}
	var after Config
	applyLoadDefaults(&after)
	if err = yaml.Unmarshal(converted, &after); err != nil {
		return err
	}
	if equivalentConfigValues(reflect.ValueOf(&before).Elem(), reflect.ValueOf(&after).Elem()) {
		return nil
	}
	return fmt.Errorf("toml: cannot express null for %s: TOML has no null and leaving the key out applies its default instead; set an explicit value or remove the key", strings.Join(dropped, ", "))
}

// equivalentConfigValues reports whether two decoded configs hold the same
// settings. It is reflect.DeepEqual except that raw yaml.Node values (opaque
// plugin settings) compare by content, not by source position, style or comments.
func equivalentConfigValues(a, b reflect.Value) bool {
	if a.IsValid() != b.IsValid() {
		return false
	}
	if !a.IsValid() {
		return true
	}
	if a.Type() != b.Type() {
		return false
	}
	if a.Type() == yamlNodeType && a.CanInterface() && b.CanInterface() {
		nodeA, nodeB := a.Interface().(yaml.Node), b.Interface().(yaml.Node)
		return equivalentYAMLNodes(&nodeA, &nodeB)
	}
	switch a.Kind() {
	case reflect.Struct:
		for i := 0; i < a.NumField(); i++ {
			if !equivalentConfigValues(a.Field(i), b.Field(i)) {
				return false
			}
		}
		return true
	case reflect.Pointer, reflect.Interface:
		if a.IsNil() || b.IsNil() {
			return a.IsNil() == b.IsNil()
		}
		return equivalentConfigValues(a.Elem(), b.Elem())
	case reflect.Slice, reflect.Array:
		if a.Kind() == reflect.Slice && a.IsNil() != b.IsNil() {
			return false
		}
		if a.Len() != b.Len() {
			return false
		}
		for i := 0; i < a.Len(); i++ {
			if !equivalentConfigValues(a.Index(i), b.Index(i)) {
				return false
			}
		}
		return true
	case reflect.Map:
		if a.IsNil() != b.IsNil() || a.Len() != b.Len() {
			return false
		}
		iter := a.MapRange()
		for iter.Next() {
			other := b.MapIndex(iter.Key())
			if !other.IsValid() || !equivalentConfigValues(iter.Value(), other) {
				return false
			}
		}
		return true
	default:
		if !a.CanInterface() {
			// Unexported scalar state: compare through a copy.
			return reflect.DeepEqual(valueCopy(a), valueCopy(b))
		}
		return reflect.DeepEqual(a.Interface(), b.Interface())
	}
}

// valueCopy returns an interface holding a copy of an unexported scalar value.
func valueCopy(v reflect.Value) any {
	switch v.Kind() {
	case reflect.Bool:
		return v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint()
	case reflect.Float32, reflect.Float64:
		return v.Float()
	case reflect.String:
		return v.String()
	default:
		return v.Kind()
	}
}

// equivalentYAMLNodes compares YAML nodes by kind, resolved tag, value and
// content, following aliases.
func equivalentYAMLNodes(a, b *yaml.Node) bool {
	for a != nil && a.Kind == yaml.AliasNode {
		a = a.Alias
	}
	for b != nil && b.Kind == yaml.AliasNode {
		b = b.Alias
	}
	if a == nil || b == nil || a.Kind == 0 || b.Kind == 0 {
		return (a == nil || a.Kind == 0) == (b == nil || b.Kind == 0)
	}
	if a.Kind != b.Kind || len(a.Content) != len(b.Content) {
		return false
	}
	if a.Kind == yaml.ScalarNode && (a.ShortTag() != b.ShortTag() || a.Value != b.Value) {
		return false
	}
	for i := range a.Content {
		if !equivalentYAMLNodes(a.Content[i], b.Content[i]) {
			return false
		}
	}
	return true
}

func renderTOML(data []byte) ([]byte, []string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, nil, err
	}
	w := &tomlWriter{}
	if len(doc.Content) == 0 {
		w.comments("", doc.HeadComment, doc.FootComment)
		return w.bytes(), nil, nil
	}
	root := expandConfigAliases(doc.Content[0])
	if root.Kind == yaml.ScalarNode && root.ShortTag() == "!!null" {
		root = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	}
	if root.Kind != yaml.MappingNode {
		return nil, nil, fmt.Errorf("toml: config document must be a mapping")
	}
	w.comments("", doc.HeadComment)
	if doc.HeadComment != "" {
		w.blank()
	}
	w.comments("", root.HeadComment)
	if err := w.table(nil, root); err != nil {
		return nil, nil, err
	}
	w.comments("", root.FootComment, doc.FootComment)
	return w.bytes(), w.dropped, nil
}

type tomlWriter struct {
	buf bytes.Buffer
	// dropped lists the paths of null values left out of the rendering.
	dropped []string
}

func (w *tomlWriter) bytes() []byte {
	out := w.buf.Bytes()
	if len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	return out
}

func (w *tomlWriter) line(text string) {
	w.buf.WriteString(text)
	w.buf.WriteByte('\n')
}

// blank separates the next block from written content with one empty line.
func (w *tomlWriter) blank() {
	out := w.buf.Bytes()
	if len(out) == 0 || bytes.HasSuffix(out, []byte("\n\n")) {
		return
	}
	w.buf.WriteByte('\n')
}

// comments writes YAML comment blocks as TOML comment lines with indent.
func (w *tomlWriter) comments(indent string, blocks ...string) {
	for _, block := range blocks {
		if block == "" {
			continue
		}
		for _, text := range strings.Split(block, "\n") {
			text = strings.TrimRight(text, " \t\r")
			switch {
			case strings.TrimSpace(text) == "":
				w.blank()
			case strings.HasPrefix(strings.TrimSpace(text), "#"):
				w.line(indent + strings.TrimSpace(text))
			default:
				w.line(indent + "# " + strings.TrimSpace(text))
			}
		}
	}
}

// footComments writes foot comment blocks followed by an empty line, which is
// what marks them as belonging to the item above when read back.
func (w *tomlWriter) footComments(blocks ...string) {
	before := w.buf.Len()
	w.comments("", blocks...)
	if w.buf.Len() > before {
		w.blank()
	}
}

func lineCommentSuffix(comments ...string) string {
	for _, comment := range comments {
		comment = strings.TrimSpace(strings.ReplaceAll(comment, "\n", " "))
		if comment == "" {
			continue
		}
		if !strings.HasPrefix(comment, "#") {
			comment = "# " + comment
		}
		return " " + comment
	}
	return ""
}

func isYAMLNull(node *yaml.Node) bool {
	return node == nil || (node.Kind == yaml.ScalarNode && node.ShortTag() == "!!null")
}

// isTOMLArrayOfTables reports whether node is emitted as [[array]] entries.
func isTOMLArrayOfTables(node *yaml.Node) bool {
	if node.Kind != yaml.SequenceNode || len(node.Content) == 0 {
		return false
	}
	for _, element := range node.Content {
		if element.Kind != yaml.MappingNode {
			return false
		}
	}
	return true
}

// table writes the entries of mapping m, which lives at path: plain keys first,
// then sub-tables and arrays of tables in document order.
func (w *tomlWriter) table(path []string, m *yaml.Node) error {
	type entry struct{ key, value *yaml.Node }
	var plain, nested []entry
	for i := 0; i+1 < len(m.Content); i += 2 {
		key, value := m.Content[i], m.Content[i+1]
		if key.Kind != yaml.ScalarNode {
			return fmt.Errorf("toml: non-scalar key under %s", tomlDisplayPath(path))
		}
		if isYAMLNull(value) {
			w.dropped = append(w.dropped, tomlDisplayPath(tomlChildPath(path, key.Value)))
			continue
		}
		if (value.Kind == yaml.MappingNode && len(value.Content) > 0) || isTOMLArrayOfTables(value) {
			nested = append(nested, entry{key, value})
		} else {
			plain = append(plain, entry{key, value})
		}
	}
	for i, e := range plain {
		child := tomlChildPath(path, e.key.Value)
		text, err := w.inlineValue(child, e.value, true)
		if err != nil {
			return err
		}
		if i > 0 && (e.key.HeadComment != "" || e.value.HeadComment != "") {
			w.blank()
		}
		w.comments("", e.key.HeadComment, e.value.HeadComment)
		w.line(tomlKey(e.key.Value) + " = " + text + lineCommentSuffix(e.key.LineComment, e.value.LineComment))
		w.footComments(e.key.FootComment, e.value.FootComment)
	}
	for _, e := range nested {
		child := tomlChildPath(path, e.key.Value)
		header := tomlHeader(child)
		if e.value.Kind == yaml.MappingNode {
			w.blank()
			w.comments("", e.key.HeadComment, e.value.HeadComment)
			w.line("[" + header + "]" + lineCommentSuffix(e.key.LineComment, e.value.LineComment))
			if err := w.table(child, e.value); err != nil {
				return err
			}
			w.footComments(e.value.FootComment, e.key.FootComment)
			continue
		}
		for i, element := range e.value.Content {
			w.blank()
			suffix := lineCommentSuffix(element.LineComment)
			if i == 0 {
				w.comments("", e.key.HeadComment, e.value.HeadComment)
				suffix = lineCommentSuffix(element.LineComment, e.key.LineComment, e.value.LineComment)
			}
			w.comments("", element.HeadComment)
			w.line("[[" + header + "]]" + suffix)
			if err := w.table(child, element); err != nil {
				return err
			}
			w.footComments(element.FootComment)
		}
		w.footComments(e.value.FootComment, e.key.FootComment)
	}
	return nil
}

const tomlInlineArrayWidth = 88

// inlineValue renders node as a TOML value. Top-level arrays that are long or
// carry comments span multiple lines; nested values stay on one line.
func (w *tomlWriter) inlineValue(path []string, node *yaml.Node, top bool) (string, error) {
	switch node.Kind {
	case yaml.ScalarNode:
		return tomlScalar(path, node, top)
	case yaml.MappingNode:
		var parts []string
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, value := node.Content[i], node.Content[i+1]
			if key.Kind != yaml.ScalarNode {
				return "", fmt.Errorf("toml: non-scalar key under %s", tomlDisplayPath(path))
			}
			child := tomlChildPath(path, key.Value)
			if isYAMLNull(value) {
				w.dropped = append(w.dropped, tomlDisplayPath(child))
				continue
			}
			text, err := w.inlineValue(child, value, false)
			if err != nil {
				return "", err
			}
			parts = append(parts, tomlKey(key.Value)+" = "+text)
		}
		if len(parts) == 0 {
			return "{}", nil
		}
		return "{ " + strings.Join(parts, ", ") + " }", nil
	case yaml.SequenceNode:
		items := make([]string, 0, len(node.Content))
		width := 2
		commented := false
		for i, element := range node.Content {
			child := tomlChildPath(path, tomlIndexSegment(i))
			if isYAMLNull(element) {
				return "", fmt.Errorf("toml: cannot represent the null array element at %s; TOML has no null", tomlDisplayPath(child))
			}
			text, err := w.inlineValue(child, element, false)
			if err != nil {
				return "", err
			}
			items = append(items, text)
			width += len(text) + 2
			commented = commented || element.HeadComment != "" || element.LineComment != "" || element.FootComment != ""
		}
		if !top || (!commented && width <= tomlInlineArrayWidth) {
			return "[" + strings.Join(items, ", ") + "]", nil
		}
		var b strings.Builder
		b.WriteString("[\n")
		for i, element := range node.Content {
			sub := &tomlWriter{}
			sub.comments("  ", element.HeadComment)
			b.Write(sub.buf.Bytes())
			b.WriteString("  " + items[i] + "," + lineCommentSuffix(element.LineComment) + "\n")
			sub = &tomlWriter{}
			sub.comments("  ", element.FootComment)
			b.Write(sub.buf.Bytes())
		}
		b.WriteString("]")
		return b.String(), nil
	case yaml.AliasNode:
		return w.inlineValue(path, expandConfigAliases(node), top)
	default:
		return "", fmt.Errorf("toml: unsupported YAML node at %s", tomlDisplayPath(path))
	}
}

func tomlScalar(path []string, node *yaml.Node, top bool) (string, error) {
	switch node.ShortTag() {
	case "!!bool":
		var b bool
		if err := node.Decode(&b); err != nil {
			return "", fmt.Errorf("toml: %s: %w", tomlDisplayPath(path), err)
		}
		return strconv.FormatBool(b), nil
	case "!!int":
		var i int64
		if err := node.Decode(&i); err != nil {
			return "", fmt.Errorf("toml: %s: integer %s does not fit TOML's 64-bit integers", tomlDisplayPath(path), node.Value)
		}
		return strconv.FormatInt(i, 10), nil
	case "!!float":
		var f float64
		if err := node.Decode(&f); err != nil {
			return "", fmt.Errorf("toml: %s: %w", tomlDisplayPath(path), err)
		}
		switch {
		case math.IsNaN(f):
			return "nan", nil
		case math.IsInf(f, 1):
			return "inf", nil
		case math.IsInf(f, -1):
			return "-inf", nil
		}
		return finiteFloatText(f), nil
	case "!!binary":
		decoded, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(node.Value), ""))
		if err != nil {
			return "", fmt.Errorf("toml: %s: %w", tomlDisplayPath(path), err)
		}
		return tomlString(string(decoded), top), nil
	default:
		// Strings, timestamps and custom tags keep their text.
		return tomlString(node.Value, top), nil
	}
}

var tomlBareKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func tomlKey(key string) string {
	if tomlBareKey.MatchString(key) {
		return key
	}
	return tomlBasicString(key)
}

func tomlHeader(path []string) string {
	parts := make([]string, len(path))
	for i, segment := range path {
		parts[i] = tomlKey(segment)
	}
	return strings.Join(parts, ".")
}

func tomlDisplayPath(path []string) string {
	if len(path) == 0 {
		return "the document root"
	}
	return strings.Join(path, ".")
}

// tomlString renders s, as a multi-line literal string when allowed and
// representable, otherwise as a basic string.
func tomlString(s string, multiline bool) string {
	if multiline && strings.Contains(s, "\n") && tomlLiteralSafe(s) {
		return "'''\n" + s + "'''"
	}
	return tomlBasicString(s)
}

func tomlLiteralSafe(s string) bool {
	if strings.Contains(s, "'''") || strings.HasSuffix(s, "'") {
		return false
	}
	for _, r := range s {
		if (r < 0x20 && r != '\t' && r != '\n') || r == 0x7f {
			return false
		}
	}
	return true
}

func tomlBasicString(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
