package chat_completions

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func streamPatchChat(t *testing.T, deltas []string, doneEvent string) (string, *ConvertCliToOpenAIParams) {
	t.Helper()
	request := []byte(`{"tools":[{"type":"custom","name":"apply_patch"}]}`)
	var param any
	send := func(event string) [][]byte {
		return ConvertCodexResponseToOpenAI(t.Context(), "m", request, request, []byte("data: "+event), &param)
	}
	var args strings.Builder
	collect := func(out [][]byte) {
		for _, chunk := range out {
			args.WriteString(gjson.GetBytes(chunk, "choices.0.delta.tool_calls.0.function.arguments").String())
		}
	}
	collect(send(`{"type":"response.output_item.added","output_index":0,"item":{"type":"custom_tool_call","id":"a","call_id":"c","name":"apply_patch","input":""}}`))
	for _, delta := range deltas {
		collect(send(`{"type":"response.custom_tool_call_input.delta","item_id":"a","output_index":0,"delta":` + jsonString(delta) + `}`))
	}
	collect(send(doneEvent))
	return args.String(), param.(*ConvertCliToOpenAIParams)
}

func jsonString(value string) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

// F14: a completed input longer than the streamed deltas must not be truncated.
func TestApplyPatchChatStreamCompletesMissingTail(t *testing.T) {
	full := "*** Begin Patch\n+tail\n*** End Patch"
	for _, done := range []string{
		`{"type":"response.custom_tool_call_input.done","item_id":"a","output_index":0,"input":` + jsonString(full) + `}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"custom_tool_call","id":"a","call_id":"c","name":"apply_patch","input":` + jsonString(full) + `}}`,
	} {
		args, p := streamPatchChat(t, []string{"*** Begin Patch\n"}, done)
		if got := gjson.Get(args, "input").String(); got != full {
			t.Fatalf("arguments=%s, want full input %q", args, full)
		}
		if err := p.ToolInputError(); err != nil {
			t.Fatalf("unexpected tool input error: %v", err)
		}
	}
}

func TestApplyPatchChatStreamConflictingCompletionFails(t *testing.T) {
	done := `{"type":"response.custom_tool_call_input.done","item_id":"a","output_index":0,"input":"*** Begin Patch\n-other\n*** End Patch"}`
	_, p := streamPatchChat(t, []string{"*** Begin Patch\n+mine\n"}, done)
	if p.ToolInputError() == nil {
		t.Fatal("conflicting completed input must report a tool input error")
	}
}
