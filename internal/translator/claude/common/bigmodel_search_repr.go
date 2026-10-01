package claudecommon

import "strings"

// BigModel search hits arrive as one or more adjacent Python-repr container
// values inside assistant-side bare tool_result blocks, shaped as
// [{"text": [{"title", "link", "content", "refer"}]}]. This parser accepts
// exactly that contract: bounded nesting, quoted strings with Python escapes,
// numbers and the True/False/None literals. Anything else - unknown tokens,
// truncated payloads, hits with missing or non-string title/link, excessive
// depth - fails loudly so callers never mistake corruption for empty results.

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
	digits := 0
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case c >= '0' && c <= '9':
			digits++
		case c == '-' || c == '+' || c == '.' || c == 'e' || c == 'E':
		default:
			return nil, false
		}
	}
	if digits == 0 {
		return nil, false
	}
	return reprScalar(text), true
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

// collectTextHits enforces the hit-list structure: every element of a
// top-level container is either the {"text": [hit...]} carrier dict or an
// empty list, and every hit carries string title and link. A corrupt hit
// fails the whole payload instead of being silently dropped.
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
			for _, raw := range textList {
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
				for key, raw := range hit {
					if text, isString := raw.(string); isString {
						fields[key] = text
					}
				}
				*hits = append(*hits, BigModelSearchHit{Title: title, URL: link, Fields: fields})
			}
		case []any:
			if len(typed) != 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}
