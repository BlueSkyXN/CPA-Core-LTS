package responses

import (
	"strings"
	"testing"
)

func TestClaudeResponsesRejectsOpaqueDelegatedTask(t *testing.T) {
	for _, content := range []string{
		`[{"type":"encrypted_content","encrypted_content":"opaque-task-fixture"}]`,
		`[{"type":"input_text","text":"Message Type: NEW_TASK\nPayload:\n"},{"type":"encrypted_content","encrypted_content":"opaque-task-fixture"}]`,
	} {
		payload := []byte(`{"model":"test-model","instructions":"Follow the delegated task.","input":[{"type":"message","role":"user","content":"Earlier context is not the new task."},{"type":"agent_message","content":` + content + `}]}`)
		_, err := ConvertOpenAIResponsesRequestToClaude("test-model", payload, false)
		if err == nil {
			t.Fatal("opaque delegated task was discarded without rejecting the request")
		}
		status, ok := err.(interface{ StatusCode() int })
		if !ok || status.StatusCode() != 400 {
			t.Fatalf("error must be a 400 client request error: %v", err)
		}
		scoped, ok := err.(interface{ IsRequestScoped() bool })
		if !ok || !scoped.IsRequestScoped() {
			t.Fatalf("error must not cause credential rotation: %v", err)
		}
		if strings.Contains(err.Error(), "opaque-task-fixture") {
			t.Fatal("error leaked delegated content")
		}
	}
}
