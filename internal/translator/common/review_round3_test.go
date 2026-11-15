package common

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
)

// 评审第三轮复现用例（原文拷贝自评审 overlay，命名保持 TestReviewRound3 前缀）。
func TestReviewRound3ExplicitOnlyClusterResetsGuess(t *testing.T) {
	doc := ojson.NewDocString(`{"input":[
 {"type":"function_call_output","name":"page_context","output":"ctx one"},
 {"type":"function_call_output","name":"page_context","output":"ctx two"},
 {"type":"message","role":"user","content":"next"},
 {"type":"function_call","call_id":"a","name":"tool_a","arguments":"{}"},
 {"type":"function_call_output","call_id":"a","output":"A done"},
 {"type":"message","role":"user","content":"next"},
 {"type":"function_call","call_id":"b","name":"tool_b","arguments":"{}"},
 {"type":"function_call_output","output":"B done"}]}`)
	SanitizeOrphanResponsesToolOutputsDoc(doc)
	if got := doc.Get("input.7.call_id").String(); got != "b" {
		t.Errorf("completed explicit A remained pending after orphan cluster: B output=%s", doc.Get("input.7").Raw)
	}
}

func TestReviewRound3ExplicitMatchInAmbiguousClusterIsConsumed(t *testing.T) {
	doc := ojson.NewDocString(`{"input":[
 {"type":"function_call","call_id":"a","name":"tool_a","arguments":"{}"},
 {"type":"function_call","call_id":"b","name":"tool_b","arguments":"{}"},
 {"type":"function_call_output","call_id":"a","output":"A done"},
 {"type":"function_call_output","output":"unknown X"},
 {"type":"function_call_output","output":"unknown Y"},
 {"type":"message","role":"user","content":"next"},
 {"type":"function_call_output","output":"B done"}]}`)
	SanitizeOrphanResponsesToolOutputsDoc(doc)
	if got := doc.Get("input.6.call_id").String(); got != "b" {
		t.Errorf("explicit A was retained as pending because adjacent missing outputs were ambiguous: B output=%s", doc.Get("input.6").Raw)
	}
}

func TestReviewRound3UniqueNameMatchesRemainDeterministic(t *testing.T) {
	items := ojson.Parse(`[
 {"type":"function_call","call_id":"a","name":"tool_a","arguments":"{}"},
 {"type":"function_call","call_id":"b","name":"tool_b","arguments":"{}"},
 {"type":"function_call_output","name":"tool_b","output":"B done"},
 {"type":"function_call_output","name":"tool_a","output":"A done"}]`).Array()
	normalized := NormalizeResponsesToolCallOutputs(items)
	if a, b := normalized[3].Get("call_id").String(), normalized[2].Get("call_id").String(); a != "a" || b != "b" {
		t.Errorf("unique named matches discarded: a=%q b=%q", a, b)
	}
}
