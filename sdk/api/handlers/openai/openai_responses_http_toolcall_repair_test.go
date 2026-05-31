package openai

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestRepairResponsesHTTPToolCallsReordersOutputBeforeCall(t *testing.T) {
	raw := []byte(`{"input":[{"type":"function_call_output","call_id":"call-1","output":"ok"},{"type":"message","id":"msg-1"},{"type":"function_call","call_id":"call-1","name":"tool"}]}`)

	repaired := repairResponsesHTTPToolCalls(raw)

	input := gjson.GetBytes(repaired, "input").Array()
	if len(input) != 3 {
		t.Fatalf("input len = %d, want 3: %s", len(input), repaired)
	}
	if input[0].Get("type").String() != "message" {
		t.Fatalf("unexpected first item: %s", input[0].Raw)
	}
	if input[1].Get("type").String() != "function_call" || input[1].Get("call_id").String() != "call-1" {
		t.Fatalf("unexpected call item: %s", input[1].Raw)
	}
	if input[2].Get("type").String() != "function_call_output" || input[2].Get("call_id").String() != "call-1" {
		t.Fatalf("unexpected output item: %s", input[2].Raw)
	}
}

func TestRepairResponsesHTTPToolCallsKeepsOrphanOutputsStable(t *testing.T) {
	raw := []byte(`{"input":[{"type":"function_call_output","call_id":"b","output":"second"},{"type":"message","id":"msg-1"},{"type":"function_call_output","call_id":"a","output":"first"}]}`)

	repaired := repairResponsesHTTPToolCalls(raw)

	input := gjson.GetBytes(repaired, "input").Array()
	if len(input) != 3 {
		t.Fatalf("input len = %d, want 3: %s", len(input), repaired)
	}
	if input[0].Get("type").String() != "message" {
		t.Fatalf("unexpected first item: %s", input[0].Raw)
	}
	if input[1].Get("call_id").String() != "a" || input[2].Get("call_id").String() != "b" {
		t.Fatalf("orphan outputs not stable: %s", repaired)
	}
}
