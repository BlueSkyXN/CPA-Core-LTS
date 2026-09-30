package integration

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestThreeProtocolReasoningDisplay(t *testing.T) {
	if isolateDynamic(t) {
		return
	}
	f := newV2Fixture(t, "glm-5.3", "glm-5.3-flash")
	for _, model := range []string{"glm-5.3", "glm-5.3-flash"} {
		for _, stream := range []bool{false, true} {
			for _, tc := range []struct{ route, fields, path string }{
				{"/v1/responses", `"input":"hello","reasoning":{"effort":"max","summary":"auto"},"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]`, "output.0.summary.0.text"},
				{"/v1/chat/completions", `"messages":[{"role":"user","content":"hello"}],"reasoning_effort":"max","tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]`, "choices.0.message.reasoning_content"},
				{"/v1/messages", `"messages":[{"role":"user","content":"hello"}],"max_tokens":1000,"thinking":{"type":"enabled","display":"summarized"},"reasoning_effort":"max","tools":[{"name":"lookup","input_schema":{"type":"object"}}]`, "content.0.thinking"},
			} {
				rec := httptest.NewRecorder()
				body := fmt.Sprintf(`{"model":%q,"stream":%v,%s}`, model, stream, tc.fields)
				f.router.ServeHTTP(rec, httptest.NewRequest("POST", tc.route, strings.NewReader(body)))
				if rec.Code != 200 {
					t.Fatalf("%s stream=%v status=%d: %s", tc.route, stream, rec.Code, rec.Body.String())
				}
				if stream {
					field := "thinking"
					if tc.route == "/v1/responses" {
						field = "response.reasoning_summary_text.delta"
					} else if tc.route == "/v1/chat/completions" {
						field = "reasoning_content"
					}
					if !strings.Contains(rec.Body.String(), field) || !strings.Contains(rec.Body.String(), "synthetic reason") {
						t.Fatal("stream reasoning missing")
					}
				} else if gjson.GetBytes(rec.Body.Bytes(), tc.path).String() != "synthetic reason" {
					t.Fatal("JSON reasoning missing")
				}
				last := f.transport.captured[len(f.transport.captured)-1]
				if gjson.GetBytes(last, "thinking.display").Exists() || gjson.GetBytes(last, "reasoning_effort").String() != "max" || gjson.GetBytes(last, "thinking.type").String() != "enabled" {
					t.Fatal("effective controls changed")
				}
				f.checkUsage(t, false)
			}
		}
	}
}
