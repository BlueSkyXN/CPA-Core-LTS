package common

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
)

// 评审第四轮复现用例（原文拷贝自评审 overlay）。
func TestReviewRound4UniqueNameRequiresUniquePendingCall(t *testing.T) {
	tests := []struct{ name, input, want string }{
		{"two same-name pending calls", `{"input":[{"type":"function_call","call_id":"a","name":"read_file","arguments":"{\"path\":\"a.txt\"}"},{"type":"function_call","call_id":"b","name":"read_file","arguments":"{\"path\":\"b.txt\"}"},{"type":"function_call_output","name":"read_file","output":"contents"}]}`, ""},
		{"reversing pending order must remain ambiguous", `{"input":[{"type":"function_call","call_id":"b","name":"read_file","arguments":"{\"path\":\"b.txt\"}"},{"type":"function_call","call_id":"a","name":"read_file","arguments":"{\"path\":\"a.txt\"}"},{"type":"function_call_output","name":"read_file","output":"contents"}]}`, ""},
		{"future explicit result reserves A", `{"input":[{"type":"function_call","call_id":"a","name":"read_file","arguments":"{}"},{"type":"function_call","call_id":"b","name":"read_file","arguments":"{}"},{"type":"function_call_output","name":"read_file","output":"B"},{"type":"message","role":"user","content":"next"},{"type":"function_call_output","call_id":"a","output":"A"}]}`, "b"},
		{"explicit result in same cluster reserves A", `{"input":[{"type":"function_call","call_id":"a","name":"read_file","arguments":"{}"},{"type":"function_call","call_id":"b","name":"read_file","arguments":"{}"},{"type":"function_call_output","name":"read_file","output":"B"},{"type":"function_call_output","call_id":"a","output":"A"}]}`, "b"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc := ojson.NewDocString(tc.input)
			SanitizeOrphanResponsesToolOutputsDoc(doc)
			if got := doc.Get("input.2.call_id").String(); got != tc.want {
				t.Errorf("call_id=%q, want %q; output=%s", got, tc.want, doc.Get("input.2").Raw)
			}
		})
	}
}
