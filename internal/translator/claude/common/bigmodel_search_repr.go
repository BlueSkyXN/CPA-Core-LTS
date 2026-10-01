package claudecommon

import "strings"

// BigModel search hits arrive as one or more adjacent container values inside
// assistant-side bare tool_result blocks. The upstream alternates between two
// shapes: a Python-repr carrier form [{"text": [{"title", "link", "content",
// "refer"}]}] and a double-quoted JSON form [[{"title", "link", "content",
// "refer"}, ...]] without the carrier dict (both probe-confirmed against the
// real upstream, 2026-10-01). This parser accepts exactly those contracts:
// bounded nesting, quoted strings with Python escapes, numbers and the
// True/False/None literals. Accepted structure values are strings only;
// scalars parse but never satisfy a string field. Anything else - unknown
// tokens, truncated payloads, non-string hit or carrier values, malformed
// numbers, excessive depth - fails loudly so callers never mistake corruption
// for empty or partially-dropped results.

type BigModelSearchHit struct {
	Title  string
	URL    string
	Fields map[string]string
}

const bigModelReprMaxDepth = 64

type reprParser struct {
	source string
	offset int
	depth  int
}

func ParseBigModelSearchRepr(content string) (hits []BigModelSearchHit, ok bool) {
	p := &reprParser{source: content}
	if !p.skipSpace() || p.offset >= len(p.source) {
		return nil, false
	}
	for p.offset < len(p.source) {
		if p.source[p.offset] != '[' {
			return nil, false
		}
		value, parsed := p.value()
		if !parsed {
			return nil, false
		}
		if !collectTextHits(value, &hits) {
			return nil, false
		}
		if !p.skipSpace() {
			return nil, false
		}
	}
	return hits, true
}

func (p *reprParser) skipSpace() bool {
	for p.offset < len(p.source) {
		switch p.source[p.offset] {
		case ' ', '\t', '\n', '\r':
			p.offset++
		default:
			return true
		}
	}
	return true
}

func (p *reprParser) value() (any, bool) {
	p.skipSpace()
	if p.offset >= len(p.source) || p.depth >= bigModelReprMaxDepth {
		return nil, false
	}
	switch p.source[p.offset] {
	case '[':
		p.depth++
		list, ok := p.list()
		p.depth--
		return list, ok
	case '{':
		p.depth++
		dict, ok := p.dict()
		p.depth--
		return dict, ok
	case '\'', '"':
		return p.string()
	default:
		return p.scalar()
	}
}

func (p *reprParser) list() ([]any, bool) {
	p.offset++ // '['
	items := []any{}
	for {
		p.skipSpace()
		if p.offset >= len(p.source) {
			return nil, false
		}
		if p.source[p.offset] == ']' {
			p.offset++
			return items, true
		}
		item, ok := p.value()
		if !ok {
			return nil, false
		}
		items = append(items, item)
		p.skipSpace()
		if p.offset >= len(p.source) {
			return nil, false
		}
		switch p.source[p.offset] {
		case ',':
			p.offset++
		case ']':
		default:
			return nil, false
		}
	}
}

func (p *reprParser) dict() (map[string]any, bool) {
	p.offset++ // '{'
	entries := map[string]any{}
	for {
		p.skipSpace()
		if p.offset >= len(p.source) {
			return nil, false
		}
		if p.source[p.offset] == '}' {
			p.offset++
			return entries, true
		}
		key, ok := p.value()
		keyText, isString := key.(string)
		if !ok || !isString {
			return nil, false
		}
		p.skipSpace()
		if p.offset >= len(p.source) || p.source[p.offset] != ':' {
			return nil, false
		}
		p.offset++
		value, ok := p.value()
		if !ok {
			return nil, false
		}
		entries[keyText] = value
		p.skipSpace()
		if p.offset >= len(p.source) {
			return nil, false
		}
		switch p.source[p.offset] {
		case ',':
			p.offset++
		case '}':
		default:
			return nil, false
		}
	}
}

func (p *reprParser) string() (string, bool) {
	quote := p.source[p.offset]
	p.offset++
	var builder strings.Builder
	for p.offset < len(p.source) {
		c := p.source[p.offset]
		if c == quote {
			p.offset++
			return builder.String(), true
		}
		if c != '\\' {
			builder.WriteByte(c)
			p.offset++
			continue
		}
		p.offset++
		if p.offset >= len(p.source) {
			return "", false
		}
		if done := p.writeEscape(&builder); !done {
			return "", false
		}
	}
	return "", false
}

// writeEscape decodes one Python escape after the backslash was consumed.
func (p *reprParser) writeEscape(builder *strings.Builder) bool {
	escape := p.source[p.offset]
	p.offset++
	switch escape {
	case 'n':
		builder.WriteByte('\n')
	case 't':
		builder.WriteByte('\t')
	case 'r':
		builder.WriteByte('\r')
	case 'b':
		builder.WriteByte('\b')
	case 'f':
		builder.WriteByte('\f')
	case '\\', '\'', '"':
		builder.WriteByte(escape)
	case 'x':
		// In a str repr \xNN is the Unicode code point U+00NN.
		code, ok := p.readHexRune(2, 0xFF)
		if !ok {
			return false
		}
		builder.WriteRune(code)
	case 'u':
		code, ok := p.readHexRune(4, 0xFFFF)
		if !ok {
			return false
		}
		builder.WriteRune(code)
	case 'U':
		code, ok := p.readHexRune(8, 0x10FFFF)
		if !ok {
			return false
		}
		builder.WriteRune(code)
	default:
		// Unknown escapes are not silently mangled; the payload is rejected.
		return false
	}
	return true
}

func (p *reprParser) readHexRune(length int, maxValue int) (rune, bool) {
	if p.offset+length > len(p.source) {
		return 0, false
	}
	value := hexValue(p.source[p.offset : p.offset+length])
	if value < 0 || value > maxValue || (value >= 0xD800 && value <= 0xDFFF) {
		return 0, false
	}
	p.offset += length
	return rune(value), true
}

// scalar accepts only numbers and the three Python literals; any other bare
// token means the payload is not the expected repr structure.
func (p *reprParser) scalar() (any, bool) {
	start := p.offset
	for p.offset < len(p.source) {
		switch p.source[p.offset] {
		case ',', ']', '}', ' ', '\t', '\n', '\r':
			text := p.source[start:p.offset]
			return p.scalarValue(text)
		default:
			p.offset++
		}
	}
	if p.offset > start {
		return p.scalarValue(p.source[start:p.offset])
	}
	return nil, false
}

// reprScalar marks numbers and the True/False/None literals: they parse, but
// they are not strings and must never satisfy a string field requirement.
type reprScalar string

func (p *reprParser) scalarValue(text string) (any, bool) {
	if text == "" {
		return nil, false
	}
	if text == "True" || text == "False" || text == "None" {
		return reprScalar(text), true
	}
	if !isReprNumber(text) {
		return nil, false
	}
	return reprScalar(text), true
}

// isReprNumber validates the decimal literal grammar Python repr emits:
// an optional '-', a mandatory integer digit run, an optional fraction with
// mandatory digits, and an optional exponent with mandatory digits. Tokens
// such as 1e+, 1-2 or 1..2 use number characters but are not numbers and are
// rejected so corrupted payloads cannot parse as valid scalars.
func isReprNumber(text string) bool {
	i := 0
	if i < len(text) && text[i] == '-' {
		i++
	}
	intStart := i
	for i < len(text) && isReprDigit(text[i]) {
		i++
	}
	if i == intStart {
		return false
	}
	if i < len(text) && text[i] == '.' {
		i++
		fracStart := i
		for i < len(text) && isReprDigit(text[i]) {
			i++
		}
		if i == fracStart {
			return false
		}
	}
	if i < len(text) && (text[i] == 'e' || text[i] == 'E') {
		i++
		if i < len(text) && (text[i] == '+' || text[i] == '-') {
			i++
		}
		expStart := i
		for i < len(text) && isReprDigit(text[i]) {
			i++
		}
		if i == expStart {
			return false
		}
	}
	return i == len(text)
}

func isReprDigit(c byte) bool {
	return c >= '0' && c <= '9'
}

func hexValue(text string) int {
	value := 0
	for i := 0; i < len(text); i++ {
		digit := strings.IndexByte("0123456789abcdef", lowerHex(text[i]))
		if digit < 0 {
			return -1
		}
		value = value*16 + digit
	}
	return value
}

func lowerHex(c byte) byte {
	if c >= 'A' && c <= 'F' {
		return c + ('a' - 'A')
	}
	return c
}

// collectTextHits enforces the hit-list structure. Every element of a
// top-level container is either the {"text": [hit...]} carrier dict (repr
// shape), a possibly-empty list of hit dicts (JSON shape, where empty stays a
// legitimate no-hit marker), and every value in a carrier or hit dict is a
// string (title and link additionally required, link non-empty). A corrupt
// field type - a numeric or list-valued refer/content - fails the whole
// payload instead of being silently dropped as a "successful" parse with
// missing fields.
func collectTextHits(value any, hits *[]BigModelSearchHit) bool {
	list, isList := value.([]any)
	if !isList {
		return false
	}
	for _, entry := range list {
		switch typed := entry.(type) {
		case map[string]any:
			text, hasText := typed["text"]
			if !hasText {
				return false
			}
			textList, isTextList := text.([]any)
			if !isTextList {
				return false
			}
			for key, value := range typed {
				if key == "text" {
					continue
				}
				if _, isString := value.(string); !isString {
					return false
				}
			}
			if !collectHitList(textList, hits) {
				return false
			}
		case []any:
			if !collectHitList(typed, hits) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// collectHitList validates one list of hit dicts; an empty list is valid and
// yields no hits.
func collectHitList(candidates []any, hits *[]BigModelSearchHit) bool {
	for _, raw := range candidates {
		hit, isHit := raw.(map[string]any)
		if !isHit {
			return false
		}
		title, titleOK := hit["title"].(string)
		link, linkOK := hit["link"].(string)
		if !titleOK || !linkOK || link == "" {
			return false
		}
		fields := map[string]string{}
		for key, value := range hit {
			text, isString := value.(string)
			if !isString {
				return false
			}
			fields[key] = text
		}
		*hits = append(*hits, BigModelSearchHit{Title: title, URL: link, Fields: fields})
	}
	return true
}
