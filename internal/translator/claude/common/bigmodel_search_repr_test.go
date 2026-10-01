package claudecommon

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

func acceptLines(t *testing.T, lines ...string) (*PluginResponseState, int) {
	t.Helper()
	state := &PluginResponseState{}
	for i, line := range lines {
		_ = state.Accept([]byte("data: " + line))
		if state.Err != nil {
			return state, i
		}
	}
	return state, -1
}

// Real probe shape (2026-10-01): the server_tool_use stream start carries no
// input field at all and the hit payload arrives as a bare tool_result block.
func TestBigModelRealStreamShapePassesStateMachine(t *testing.T) {
	state, failedAt := acceptLines(t,
		`{"type":"message_start","message":{"id":"msg_x","type":"message","role":"assistant","content":[],"usage":{"input_tokens":9,"output_tokens":0}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_a","name":"web_search_prime"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_result","tool_use_id":"srvtoolu_a","content":"[{'text': [{'title': 'T', 'link': 'https://e.com', 'content': 'c', 'refer': 'ref_1'}]}]"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"thinking","thinking":""}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"thinking_delta","thinking":"th"}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"signature_delta","signature":"sig"}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"content_block_start","index":3,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":3,"delta":{"type":"text_delta","text":"final"}}`,
		`{"type":"content_block_stop","index":3}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
		`{"type":"message_stop"}`,
	)
	if failedAt >= 0 {
		t.Fatalf("real shape rejected at line %d: %v", failedAt, state.Err)
	}
	if !state.Terminal {
		t.Fatal("stream did not reach terminal state")
	}
}

// A parseable payload with zero hits is a legitimate empty search result.
func TestBigModelEmptyHitReprPasses(t *testing.T) {
	state, failedAt := acceptLines(t,
		`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","content":[]}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"s1","name":"web_search_prime"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_result","tool_use_id":"s1","content":"[{'text': [], 'type': 'text'}][[]]"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
		`{"type":"message_stop"}`,
	)
	if failedAt >= 0 || !state.Terminal {
		t.Fatalf("empty-hit repr rejected: failedAt=%d terminal=%v err=%v", failedAt, state.Terminal, state.Err)
	}
}

func TestBigModelSearchGuards(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
	}{
		{"duplicate bare result rejected", []string{
			`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","content":[]}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"s1","name":"web_search_prime"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_result","tool_use_id":"s1","content":"[{'text': []}]"}}`,
			`{"type":"content_block_stop","index":1}`,
			`{"type":"content_block_start","index":2,"content_block":{"type":"tool_result","tool_use_id":"s1","content":"[{'text': []}]"}}`,
		}},
		{"unparsable repr rejected", []string{
			`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","content":[]}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"s1","name":"web_search_prime"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_result","tool_use_id":"s1","content":"not a repr at all"}}`,
		}},
		{"non-string repr content rejected", []string{
			`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","content":[]}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"s1","name":"web_search_prime"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_result","tool_use_id":"s1","content":[{"type":"text"}]}}`,
		}},
		{"client tool_use without input rejected (stream)", []string{
			`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","content":[]}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_1","name":"lookup"}}`,
		}},
		{"unknown server tool rejected", []string{
			`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","content":[]}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"s1","name":"code_execution"}}`,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state, failedAt := acceptLines(t, tc.lines...)
			if failedAt < 0 || state.Err == nil {
				t.Fatalf("expected rejection, got failedAt=%d err=%v", failedAt, state.Err)
			}
		})
	}
}

// The non-stream JSON path must keep the strict client tool_use requirement.
func TestValidatePluginMessageClientToolUseRequiresInput(t *testing.T) {
	message := `{"id":"m","type":"message","role":"assistant","stop_reason":"end_turn","content":[{"type":"tool_use","id":"call_1","name":"lookup"}]}`
	if err := ValidatePluginMessage(mustParse(message), false); err == nil {
		t.Fatal("client tool_use without input accepted in JSON validation")
	}
	withInput := `{"id":"m","type":"message","role":"assistant","stop_reason":"end_turn","content":[{"type":"tool_use","id":"call_1","name":"lookup","input":{"q":1}}]}`
	if err := ValidatePluginMessage(mustParse(withInput), false); err != nil {
		t.Fatalf("client tool_use with input rejected: %v", err)
	}
	noInputServer := `{"id":"m","type":"message","role":"assistant","stop_reason":"end_turn","content":[{"type":"server_tool_use","id":"srvtoolu_a","name":"web_search_prime"}]}`
	if err := ValidatePluginMessage(mustParse(noInputServer), false); err != nil {
		t.Fatalf("server_tool_use without input rejected in JSON validation: %v", err)
	}
}

func TestParseBigModelSearchReprEdgeCases(t *testing.T) {
	// Python repr switches to double quotes when the value contains a quote.
	hits, ok := ParseBigModelSearchRepr(`[{'text': [{"title": "What's new", 'link': 'https://e.com/a'}]}]`)
	if !ok || len(hits) != 1 || hits[0].Title != "What's new" {
		t.Fatalf("double-quoted title: ok=%v hits=%+v", ok, hits)
	}
	// Literal \xNN / \t sequences in a raw fixture decode to Unicode code
	// points; the result must stay valid UTF-8 (U+00A0 encodes as C2 A0).
	hits, ok = ParseBigModelSearchRepr(`[{'text': [{'title': 'a\xa0b\tc', 'link': 'https://e.com/b'}]}]`)
	if !ok || len(hits) != 1 || hits[0].Title != "a\u00a0b\tc" || !utf8.ValidString(hits[0].Title) {
		t.Fatalf("escape decoding: ok=%v title=%q valid=%v", ok, firstTitle(hits), utf8.ValidString(firstTitle(hits)))
	}
	// \U capital escapes and \u escapes decode; invalid hex is rejected.
	hits, ok = ParseBigModelSearchRepr(`[{'text': [{'title': 'x\U0001F600y\u4e2dz', 'link': 'https://e.com/c'}]}]`)
	if !ok || len(hits) != 1 || hits[0].Title != "x\U0001F600y\u4e2dz" {
		t.Fatalf("unicode escapes: ok=%v title=%q", ok, firstTitle(hits))
	}
	if _, ok = ParseBigModelSearchRepr(`[{'text': [{'title': 'a\uZZZZb', 'link': 'https://e.com/d'}]}]`); ok {
		t.Fatal("invalid \\u hex accepted")
	}
	if _, ok = ParseBigModelSearchRepr(`[{'text': [{'title': 'a\q b', 'link': 'https://e.com/e'}]}]`); ok {
		t.Fatal("unknown escape accepted")
	}
	// Text that merely looks like fields inside a hit's content is a string
	// value in the structure and must never be re-scanned as separate hits.
	hits, ok = ParseBigModelSearchRepr(`[{'text': [{'title': 'Real', 'link': 'https://e.com/r', 'content': "'title': 'Fake', 'link': 'https://evil.example/x'"}]}]`)
	if !ok || len(hits) != 1 || hits[0].Title != "Real" {
		t.Fatalf("embedded example extracted as hit: ok=%v hits=%+v", ok, hits)
	}
	// Corrupt or unrecognized payloads fail loudly instead of returning
	// partial or empty success.
	if _, ok = ParseBigModelSearchRepr(`[{'text': [{'title': 'Broken'`); ok {
		t.Fatal("truncated repr parsed successfully")
	}
	if _, ok = ParseBigModelSearchRepr(``); ok {
		t.Fatal("empty string parsed successfully")
	}
	if _, ok = ParseBigModelSearchRepr(`[garbage]`); ok {
		t.Fatal("bare token list parsed successfully")
	}
	if _, ok = ParseBigModelSearchRepr(`[{'text': [{'title': 'A', 'link': 'https://e.com/a'}, {'title': 'B'}]}]`); ok {
		t.Fatal("hit without link silently dropped")
	}
	if _, ok = ParseBigModelSearchRepr(`[{'text': [{'title': 123, 'link': 'https://e.com/n'}]}]`); ok {
		t.Fatal("non-string title accepted")
	}
	// Non-string refer/content corrupt the hit: the whole payload must fail
	// instead of parsing "successfully" with the field silently dropped.
	if _, ok = ParseBigModelSearchRepr(`[{'text': [{'title': 'T', 'link': 'https://e.com/a', 'refer': 123}]}]`); ok {
		t.Fatal("numeric refer silently dropped")
	}
	if _, ok = ParseBigModelSearchRepr(`[{'text': [{'title': 'T', 'link': 'https://e.com/a', 'refer': ['ref_1']}]}]`); ok {
		t.Fatal("list refer silently dropped")
	}
	if _, ok = ParseBigModelSearchRepr(`[{'text': [{'title': 'T', 'link': 'https://e.com/a', 'content': ['body']}]}]`); ok {
		t.Fatal("list content silently dropped")
	}
	// Carrier metadata values must be strings too.
	if _, ok = ParseBigModelSearchRepr(`[{'text': [], 'type': 123}]`); ok {
		t.Fatal("non-string carrier field accepted")
	}
	if _, ok = ParseBigModelSearchRepr(`[{'text': [{'title': 'T', 'link': 'https://e.com/a', 'score': 1e+}]}]`); ok {
		t.Fatal("malformed number inside hit accepted")
	}
	// Intact refer/content must ride through Fields for replay.
	hits, ok = ParseBigModelSearchRepr(`[{'text': [{'title': 'T', 'link': 'https://e.com/a', 'content': 'body', 'refer': 'ref_1'}]}]`)
	if !ok || len(hits) != 1 || hits[0].Fields["content"] != "body" || hits[0].Fields["refer"] != "ref_1" {
		t.Fatalf("hit body/source fields lost: ok=%v hits=%+v", ok, hits)
	}
	deep := "[" + strings.Repeat("[", 10000) + strings.Repeat("]", 10000) + "]"
	if _, ok = ParseBigModelSearchRepr(deep); ok {
		t.Fatal("unbounded nesting accepted")
	}
}

// Number tokens must match the decimal literal grammar Python repr emits;
// character-class matching alone would accept corruption like 1e+ or 1-2.
func TestReprNumberGrammar(t *testing.T) {
	for _, token := range []string{"0", "123", "-5", "1.5", "-0.5", "1e10", "1e+30", "1E-7", "1.5e-3", "-0.0"} {
		if !isReprNumber(token) {
			t.Fatalf("valid number token %q rejected", token)
		}
	}
	for _, token := range []string{"1e+", "1-2", "1..2", "1.", ".5", "-", "+1", "1e", "--1", "1.5.6", "e5", "", "0x10", "NaN", "inf"} {
		if isReprNumber(token) {
			t.Fatalf("invalid number token %q accepted", token)
		}
	}
}

// Real upstream alternates between the repr carrier shape and a
// double-quoted JSON shape with no carrier dict (live capture 2026-10-01):
// [[{"title","link","content","refer"}, ...]].
const bigModelJSONShapeFixture = `[[{"title": "人民币汇率中间价", "link": "https://www.safe.gov.cn/AppStructured/hlw/RMBQuery.do", "content": "人民币汇率中间价 USD/CNY 6.7351", "refer": "ref_1"}, {"title": "美元兑人民币汇率 - 我查", "link": "https://chl.cn/huilv/?usd", "content": "1美元=6.7351元人民币", "refer": "ref_2"}]]`

func TestBigModelJSONShapeHitsParse(t *testing.T) {
	hits, ok := ParseBigModelSearchRepr(bigModelJSONShapeFixture)
	if !ok || len(hits) != 2 {
		t.Fatalf("JSON shape: ok=%v hits=%d", ok, len(hits))
	}
	if hits[0].Title != "人民币汇率中间价" || hits[0].URL != "https://www.safe.gov.cn/AppStructured/hlw/RMBQuery.do" ||
		hits[0].Fields["content"] != "人民币汇率中间价 USD/CNY 6.7351" || hits[0].Fields["refer"] != "ref_1" {
		t.Fatalf("JSON shape fields lost: %+v", hits[0])
	}
	// Adjacent empty padding after the JSON shape stays valid.
	if _, ok = ParseBigModelSearchRepr(bigModelJSONShapeFixture + `[[]]`); !ok {
		t.Fatal("JSON shape with adjacent empty padding rejected")
	}
	// Several hit lists inside one container are valid.
	if hits, ok = ParseBigModelSearchRepr(`[[{"title": "A", "link": "https://e.com/a"}], [{"title": "B", "link": "https://e.com/b"}]]`); !ok || len(hits) != 2 {
		t.Fatalf("multiple inner hit lists: ok=%v hits=%d", ok, len(hits))
	}
	// The corruption guards apply to the JSON shape exactly as to the repr shape.
	if _, ok = ParseBigModelSearchRepr(`[[{"title": "T", "link": "https://e.com/a", "refer": 123}]]`); ok {
		t.Fatal("JSON shape numeric refer accepted")
	}
	if _, ok = ParseBigModelSearchRepr(`[[{"title": "T", "link": "https://e.com/a"}, "stray"]]`); ok {
		t.Fatal("non-dict entry inside hit list accepted")
	}
	if _, ok = ParseBigModelSearchRepr(`[[]]`); !ok {
		t.Fatal("empty-only JSON shape rejected")
	}
}

// Live capture 2026-10-01 (UAT, real upstream): a leading text block, the
// server_tool_use carrying its full input at start, the bare tool_result with
// the JSON-shaped hit payload, then signed thinking and the final text.
func TestBigModelJSONShapeRealStreamReplay(t *testing.T) {
	content := fmt.Sprintf("%q", bigModelJSONShapeFixture)
	state, failedAt := acceptLines(t,
		`{"type":"message_start","message":{"id":"msg_json","type":"message","role":"assistant","content":[],"usage":{"input_tokens":11,"output_tokens":0}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"先搜索。"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"server_tool_use","id":"call_c4e7c0e15537463b9ee991e3","name":"web_search_prime","input":{"search_query":"今日人民币对美元汇率中间价","search_recency_filter":"oneDay"}}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"查到了。"}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"content_block_start","index":3,"content_block":{"type":"tool_result","tool_use_id":"call_c4e7c0e15537463b9ee991e3","content":`+content+`}}`,
		`{"type":"content_block_stop","index":3}`,
		`{"type":"content_block_start","index":4,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		`{"type":"content_block_delta","index":4,"delta":{"type":"thinking_delta","thinking":"th"}}`,
		`{"type":"content_block_delta","index":4,"delta":{"type":"signature_delta","signature":"sig"}}`,
		`{"type":"content_block_stop","index":4}`,
		`{"type":"content_block_start","index":5,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":5,"delta":{"type":"text_delta","text":"今日中间价 6.7351。"}}`,
		`{"type":"content_block_stop","index":5}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":42}}`,
		`{"type":"message_stop"}`,
	)
	if failedAt >= 0 {
		t.Fatalf("JSON-shape stream rejected at line %d: %v", failedAt, state.Err)
	}
	if !state.Terminal {
		t.Fatal("stream did not reach terminal state")
	}
}

// JSON-legal string encodings must decode in the JSON shape: escaped slashes
// and UTF-16 surrogate pairs (external review repro, 2026-10-02). Paired
// surrogates follow the JSON pairing rule; lone or malformed surrogates stay
// rejected in both dialects.
func TestBigModelJSONEscapeDecoding(t *testing.T) {
	hits, ok := ParseBigModelSearchRepr(`[[{"title": "T", "link": "https:\/\/example.com\/a", "content": "body", "refer": "ref_1"}]]`)
	if !ok || len(hits) != 1 || hits[0].URL != "https://example.com/a" || !utf8.ValidString(hits[0].URL) {
		t.Fatalf("escaped slash: ok=%v hits=%+v", ok, hits)
	}
	hits, ok = ParseBigModelSearchRepr(`[[{"title": "T\ud83d\ude00", "link": "https://example.com/a", "content": "b\ud83d\ude00y", "refer": "r\ud83d\ude00f"}]]`)
	if !ok || len(hits) != 1 {
		t.Fatalf("surrogate pair: ok=%v hits=%d", ok, len(hits))
	}
	if hits[0].Title != "T\U0001F600" || hits[0].Fields["content"] != "b\U0001F600y" || hits[0].Fields["refer"] != "r\U0001F600f" {
		t.Fatalf("surrogate pair fields: %+v", hits[0])
	}
	if !utf8.ValidString(hits[0].Title) || !utf8.ValidString(hits[0].Fields["content"]) || !utf8.ValidString(hits[0].Fields["refer"]) {
		t.Fatal("surrogate pair decoded to invalid UTF-8")
	}
	// The JSON-only slash escape is unambiguous in the union and must not
	// change what a repr-shape payload means.
	if hits, ok = ParseBigModelSearchRepr(`[{'text': [{'title': 'a\/b', 'link': 'https://e.com/x'}]}]`); !ok || len(hits) != 1 || hits[0].Title != "a/b" {
		t.Fatalf("repr shape with escaped slash: ok=%v hits=%+v", ok, hits)
	}
	for _, payload := range []string{
		`[[{"title": "T\ud83d", "link": "https://example.com/a"}]]`,
		`[[{"title": "T\ud83dx", "link": "https://example.com/a"}]]`,
		`[[{"title": "T\ud83d\ud83d", "link": "https://example.com/a"}]]`,
		`[[{"title": "T\ud83d\ud004", "link": "https://example.com/a"}]]`,
		`[[{"title": "T\udc00", "link": "https://example.com/a"}]]`,
		`[[{"title": "T\ud83d\/", "link": "https://example.com/a"}]]`,
	} {
		if _, ok = ParseBigModelSearchRepr(payload); ok {
			t.Fatalf("lone or malformed surrogate accepted: %s", payload)
		}
	}
}

// The escaped encodings must also survive the full non-stream JSON message
// validation path, not only the direct parser entry point.
func TestBigModelJSONEscapesJSONMessagePath(t *testing.T) {
	content := `[[{\"title\": \"T\\ud83d\\ude00\", \"link\": \"https:\\\/\\\/example.com\\\/a\", \"content\": \"b\\ud83d\\ude00y\", \"refer\": \"ref_1\"}]]`
	message := `{"id":"m","type":"message","role":"assistant","stop_reason":"end_turn","content":[` +
		`{"type":"server_tool_use","id":"s_esc","name":"web_search_prime"},` +
		`{"type":"tool_result","tool_use_id":"s_esc","content":"` + content + `"}]}`
	if err := ValidatePluginMessage(mustParse(message), false); err != nil {
		t.Fatalf("escaped JSON-shape payload rejected in JSON validation: %v", err)
	}
}

func TestDuplicateServerToolUseIDRejected(t *testing.T) {
	// JSON path: the same server_tool_use id appears twice.
	dupJSON := `{"id":"m","type":"message","role":"assistant","stop_reason":"end_turn","content":[` +
		`{"type":"server_tool_use","id":"s1","name":"web_search_prime"},` +
		`{"type":"server_tool_use","id":"s1","name":"web_search_prime"}]}`
	if err := ValidatePluginMessage(mustParse(dupJSON), false); err == nil {
		t.Fatal("duplicate server_tool_use id accepted in JSON validation")
	}
	// Stream path: reopening an already consumed search id is rejected.
	state, failedAt := acceptLines(t,
		`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","content":[]}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"s1","name":"web_search_prime"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_result","tool_use_id":"s1","content":"[{'text': []}]"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"server_tool_use","id":"s1","name":"web_search_prime"}}`,
	)
	if failedAt < 0 || state.Err == nil {
		t.Fatalf("reopened search id accepted: failedAt=%d err=%v", failedAt, state.Err)
	}
}

func firstTitle(hits []BigModelSearchHit) string {
	if len(hits) == 0 {
		return ""
	}
	return hits[0].Title
}

func mustParse(raw string) gjson.Result {
	return gjson.Parse(raw)
}
