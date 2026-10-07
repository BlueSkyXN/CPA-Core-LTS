package common

import (
	"fmt"
	"testing"
)

// F11: once streamed arguments already form complete JSON, a final snapshot
// whose input extends them is inconsistent and must fail instead of emitting
// the extra tail as a successful custom tool call.
func TestApplyPatchResponsesBridgeRejectsSnapshotExtendingCompletedStream(t *testing.T) {
	for _, terminal := range []string{"arguments.done", "output_item.done"} {
		t.Run(terminal, func(t *testing.T) {
			b := NewApplyPatchResponsesBridge(patchResponsesRequest)
			patchSend(t, b, patchEvent("response.output_item.added", 0, patchItem("function_call", "fc1", "c1", "apply_patch", "")))
			streamed := `{"input":"*** Begin Patch\n*** End Patch\n"}`
			patchSend(t, b, []byte(fmt.Sprintf(`{"type":"response.function_call_arguments.delta","output_index":0,"delta":%s}`, patchJSON(streamed))))
			extended := `{"input":"*** Begin Patch\n*** End Patch\nextra"}`
			var event []byte
			if terminal == "arguments.done" {
				event = []byte(fmt.Sprintf(`{"type":"response.function_call_arguments.done","output_index":0,"arguments":%s}`, patchJSON(extended)))
			} else {
				event = patchEvent("response.output_item.done", 0, patchItem("function_call", "fc1", "c1", "apply_patch", extended))
			}
			if _, err := b.Transform(event); err == nil {
				t.Fatal("expected inconsistent snapshot to fail")
			}
		})
	}
}

func TestApplyPatchResponsesBridgeAcceptsMatchingSnapshotAfterCompletedStream(t *testing.T) {
	b := NewApplyPatchResponsesBridge(patchResponsesRequest)
	patchSend(t, b, patchEvent("response.output_item.added", 0, patchItem("function_call", "fc1", "c1", "apply_patch", "")))
	args := `{"input":"*** Begin Patch\n*** End Patch\n"}`
	patchSend(t, b, []byte(fmt.Sprintf(`{"type":"response.function_call_arguments.delta","output_index":0,"delta":%s}`, patchJSON(args))))
	patchSend(t, b, patchEvent("response.output_item.done", 0, patchItem("function_call", "fc1", "c1", "apply_patch", args)))
}
