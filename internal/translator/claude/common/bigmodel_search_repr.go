package claudecommon

import "strings"

// BigModel search hits arrive as a Python-repr string inside assistant-side
// bare tool_result blocks: [{"text": [{"title", "link", "content", "refer"}]}].
// This is a bounded structural parser for exactly that subset (lists, string
// dicts, scalars); it never evaluates code and rejects anything it cannot
// fully consume so callers can fail loudly instead of dropping results.

type BigModelSearchHit struct {
	Title  string
	URL    string
	Fields map[string]string
}

type reprParser struct {
	source string
	offset int
}

func ParseBigModelSearchRepr(content string) (hits []BigModelSearchHit, ok bool) {
	p := &reprParser{source: content}
	if !p.skipSpace() || p.offset >= len(p.source) {
		return nil, false
	}
	// The upstream payload is one or more adjacent repr values (for example a
	// text list directly followed by an empty tool list); parse them all.
	for p.offset < len(p.source) {
		// Only container reprs are valid top-level payloads; a bare scalar run
		// such as free text must fail loudly instead of parsing as words.
		if c := p.source[p.offset]; c != '[' && c != '{' {
			return nil, false
		}
		value, parsed := p.value()
		if !parsed {
			return nil, false
		}
		collectHits(value, &hits)
		if !p.skipSpace() {
			return nil, false
		}
	}
	return hits, true
}

func (p *reprParser) atEnd() bool {
	p.skipSpace()
	return p.offset >= len(p.source)
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
	if p.offset >= len(p.source) {
		return nil, false
	}
	switch p.source[p.offset] {
	case '[':
		return p.list()
	case '{':
		return p.dict()
	case '\'', '"':
		text, ok := p.string()
		return text, ok
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
		switch escape := p.source[p.offset]; escape {
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
		case 'x':
			if code, ok := p.hexEscape(2); ok {
				builder.WriteByte(code)
			} else {
				return "", false
			}
		case 'u':
			if p.offset+4 < len(p.source) {
				builder.WriteRune(rune(hexValue(p.source[p.offset+1 : p.offset+5])))
				p.offset += 4
			} else {
				return "", false
			}
		default:
			// covers \\, \', \" plus unknown escapes kept literally
			builder.WriteByte(escape)
		}
		p.offset++
	}
	return "", false
}

func (p *reprParser) hexEscape(length int) (byte, bool) {
	if p.offset+length >= len(p.source) {
		return 0, false
	}
	value := hexValue(p.source[p.offset+1 : p.offset+1+length])
	if value < 0 {
		return 0, false
	}
	p.offset += length
	return byte(value), true
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

func (p *reprParser) scalar() (any, bool) {
	start := p.offset
	for p.offset < len(p.source) {
		switch p.source[p.offset] {
		case ',', ']', '}', ' ', '\t', '\n', '\r':
			text := p.source[start:p.offset]
			if text == "" {
				return nil, false
			}
			return text, true
		default:
			p.offset++
		}
	}
	if p.offset > start {
		return p.source[start:p.offset], true
	}
	return nil, false
}

func collectHits(value any, hits *[]BigModelSearchHit) {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			collectHits(item, hits)
		}
	case map[string]any:
		title, titleOK := typed["title"].(string)
		link, linkOK := typed["link"].(string)
		if titleOK && linkOK && link != "" {
			fields := map[string]string{}
			for key, raw := range typed {
				if text, isString := raw.(string); isString {
					fields[key] = text
				}
			}
			*hits = append(*hits, BigModelSearchHit{Title: title, URL: link, Fields: fields})
			return
		}
		for _, raw := range typed {
			collectHits(raw, hits)
		}
	}
}
