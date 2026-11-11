package common

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

func TestSetResponsesToolCallIdentity(t *testing.T) {
	tests := []struct {
		name                string
		input               string
		toolName            string
		namespace           string
		itemPath            string
		namePath            string
		namespacePath       string
		wantName            string
		wantNamespace       string
		wantNamespaceExists bool
	}{
		{
			name:                "top level",
			input:               `{"name":"functions__exec"}`,
			toolName:            "exec",
			namespace:           "functions",
			namePath:            "name",
			namespacePath:       "namespace",
			wantName:            "exec",
			wantNamespace:       "functions",
			wantNamespaceExists: true,
		},
		{
			name:                "nested item",
			input:               `{"item":{"name":"functions__exec"}}`,
			toolName:            "exec",
			namespace:           "functions",
			itemPath:            "item",
			namePath:            "item.name",
			namespacePath:       "item.namespace",
			wantName:            "exec",
			wantNamespace:       "functions",
			wantNamespaceExists: true,
		},
		{
			name:          "remove stale namespace",
			input:         `{"name":"old","namespace":"stale"}`,
			toolName:      "plain",
			namePath:      "name",
			namespacePath: "namespace",
			wantName:      "plain",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := SetResponsesToolCallIdentity([]byte(test.input), test.toolName, test.namespace, test.itemPath)
			if actual := ojson.GetBytesNoCopy(got, test.namePath).String(); actual != test.wantName {
				t.Fatalf("name = %q, want %q; output=%s", actual, test.wantName, got)
			}
			namespace := ojson.GetBytesNoCopy(got, test.namespacePath)
			if namespace.Exists() != test.wantNamespaceExists {
				t.Fatalf("namespace exists = %t, want %t; output=%s", namespace.Exists(), test.wantNamespaceExists, got)
			}
			if test.wantNamespaceExists && namespace.String() != test.wantNamespace {
				t.Fatalf("namespace = %q, want %q; output=%s", namespace.String(), test.wantNamespace, got)
			}
		})
	}
}

func TestExtractResponsesCallID(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "call_id standard",
			input:    `{"call_id":"call_1"}`,
			expected: "call_1",
		},
		{
			name:     "tool_call_id",
			input:    `{"tool_call_id":"call_2"}`,
			expected: "call_2",
		},
		{
			name:     "callId",
			input:    `{"callId":"call_3"}`,
			expected: "call_3",
		},
		{
			name:     "id fallback",
			input:    `{"id":"call_4"}`,
			expected: "call_4",
		},
		{
			name:     "dedicated call_id takes precedence over id",
			input:    `{"id":"item_5","call_id":"call_5"}`,
			expected: "call_5",
		},
		{
			name:     "tool_call_id takes precedence over id",
			input:    `{"id":"item_6","tool_call_id":"call_6"}`,
			expected: "call_6",
		},
		{
			name:     "callId takes precedence over id",
			input:    `{"id":"item_7","callId":"call_7"}`,
			expected: "call_7",
		},
		{
			name:     "empty",
			input:    `{"output":"result"}`,
			expected: "",
		},
		{
			name:     "fco item id is ignored",
			input:    `{"id":"fco_01a08664-2d16-7a91-8ab2-2eccd49e4c3e"}`,
			expected: "",
		},
		{
			name:     "fco item id with explicit call_id retains call_id",
			input:    `{"id":"fco_01a08664-2d16-7a91-8ab2-2eccd49e4c3e","call_id":"call_1"}`,
			expected: "call_1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := ojson.Parse(tt.input)
			if got := ExtractResponsesCallID(node); got != tt.expected {
				t.Fatalf("ExtractResponsesCallID(%s) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestNormalizeResponsesToolCallOutputs(t *testing.T) {
	t.Run("preserves explicit call_id and fills missing in reverse order", func(t *testing.T) {
		input := []byte(`[
			{"type":"function_call","call_id":"call_a","name":"tool_a"},
			{"type":"function_call","call_id":"call_b","name":"tool_b"},
			{"type":"function_call_output","output":"result_b"},
			{"type":"function_call_output","tool_call_id":"call_a","output":"result_a"}
		]`)
		items := util.ParseGJSONBytesNoCopy(input).Array()
		normalized := NormalizeResponsesToolCallOutputs(items)
		if len(normalized) != 4 {
			t.Fatalf("expected 4 items, got %d", len(normalized))
		}

		// First output had no ID, but call_a is reserved by second output, so first output gets call_b
		if got := normalized[2].Get("call_id").String(); got != "call_b" {
			t.Fatalf("normalized[2].call_id = %q, want call_b", got)
		}
		// Second output had explicit tool_call_id call_a, so its call_id is set to call_a
		if got := normalized[3].Get("call_id").String(); got != "call_a" {
			t.Fatalf("normalized[3].call_id = %q, want call_a", got)
		}
	})

	t.Run("preserves explicit call_id across intervening user message", func(t *testing.T) {
		input := []byte(`[
			{"type":"function_call","call_id":"call_a","name":"tool_a"},
			{"type":"function_call","call_id":"call_b","name":"tool_b"},
			{"type":"function_call_output","output":"result_b"},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"intervening"}]},
			{"type":"function_call_output","call_id":"call_a","output":"result_a"}
		]`)
		items := util.ParseGJSONBytesNoCopy(input).Array()
		normalized := NormalizeResponsesToolCallOutputs(items)
		if len(normalized) != 5 {
			t.Fatalf("expected 5 items, got %d", len(normalized))
		}

		// First output (index 2) had no ID, but call_a is reserved across the intervening message by index 4,
		// so index 2 must get call_b
		if got := normalized[2].Get("call_id").String(); got != "call_b" {
			t.Fatalf("normalized[2].call_id = %q, want call_b", got)
		}
		// Last output (index 4) has explicit call_a
		if got := normalized[4].Get("call_id").String(); got != "call_a" {
			t.Fatalf("normalized[4].call_id = %q, want call_a", got)
		}
	})

	t.Run("does not steal explicit unmatched call_id", func(t *testing.T) {
		input := []byte(`[
			{"type":"function_call","call_id":"call_a","name":"tool_a"},
			{"type":"function_call_output","call_id":"call_other","output":"result_other"}
		]`)
		items := util.ParseGJSONBytesNoCopy(input).Array()
		normalized := NormalizeResponsesToolCallOutputs(items)
		if got := normalized[1].Get("call_id").String(); got != "call_other" {
			t.Fatalf("normalized[1].call_id = %q, want call_other", got)
		}
	})

	t.Run("pairs function_call_output with fco item id and no call_id using fallback", func(t *testing.T) {
		input := []byte(`[
			{"type":"function_call","call_id":"call_1788961125480214178_817","name":"Bash"},
			{"type":"function_call_output","id":"fco_01a08664-2d16-7a91-8ab2-2eccd49e4c3e","output":"result"}
		]`)
		items := util.ParseGJSONBytesNoCopy(input).Array()
		normalized := NormalizeResponsesToolCallOutputs(items)
		if len(normalized) != 2 {
			t.Fatalf("expected 2 items, got %d", len(normalized))
		}
		if got := normalized[1].Get("call_id").String(); got != "call_1788961125480214178_817" {
			t.Fatalf("normalized[1].call_id = %q, want call_1788961125480214178_817", got)
		}
	})

	t.Run("preserves explicit call_id while pairing fco item id output via fallback", func(t *testing.T) {
		input := []byte(`[
			{"type":"function_call","call_id":"call_a","name":"tool_a"},
			{"type":"function_call","call_id":"call_b","name":"tool_b"},
			{"type":"function_call_output","id":"fco_b","output":"result_b"},
			{"type":"function_call_output","call_id":"call_a","output":"result_a"}
		]`)
		items := util.ParseGJSONBytesNoCopy(input).Array()
		normalized := NormalizeResponsesToolCallOutputs(items)
		if len(normalized) != 4 {
			t.Fatalf("expected 4 items, got %d", len(normalized))
		}
		if got := normalized[2].Get("call_id").String(); got != "call_b" {
			t.Fatalf("normalized[2].call_id = %q, want call_b", got)
		}
		if got := normalized[3].Get("call_id").String(); got != "call_a" {
			t.Fatalf("normalized[3].call_id = %q, want call_a", got)
		}
	})
}

// 评审 I3 回归：逐轮可确定的配对不得被全局歧义计数撤销。
// call A → 无 ID 结果 A → 下一轮 → call B → 无 ID 结果 B：每个簇各只有一个
// 候选、各只有一个 pending 调用，历史全局有两个 missing 输出但无局部歧义。
func TestNormalizeResponsesToolCallOutputsPerTurnPairsNotGloballyAmbiguous(t *testing.T) {
	input := []byte(`[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"q1"}]},
		{"type":"function_call","call_id":"call_a","name":"lookup"},
		{"type":"function_call_output","output":"result A"},
		{"type":"message","role":"assistant","content":[{"type":"output_text","text":"a1"}]},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"q2"}]},
		{"type":"function_call","call_id":"call_b","name":"lookup"},
		{"type":"function_call_output","output":"result B"},
		{"type":"message","role":"assistant","content":[{"type":"output_text","text":"a2"}]}
	]`)
	items := util.ParseGJSONBytesNoCopy(input).Array()
	if responsesToolOutputAssignmentAmbiguous(items) {
		t.Fatal("per-turn single-candidate pairs must not be flagged ambiguous")
	}
	normalized := NormalizeResponsesToolCallOutputs(items)
	if got := normalized[2].Get("call_id").String(); got != "call_a" {
		t.Fatalf("first turn pair lost: call_id=%q, want call_a", got)
	}
	if got := normalized[6].Get("call_id").String(); got != "call_b" {
		t.Fatalf("second turn pair lost: call_id=%q, want call_b", got)
	}
}

// 真歧义仍须拦截：同一簇两个无 ID 输出对两个 pending 调用（不可确定归属）。
func TestResponsesToolOutputAssignmentAmbiguousLocalCluster(t *testing.T) {
	ambiguous := util.ParseGJSONBytesNoCopy([]byte(`[
		{"type":"function_call","call_id":"call_a","name":"tool_a"},
		{"type":"function_call","call_id":"call_b","name":"tool_b"},
		{"type":"function_call_output","output":"x"},
		{"type":"function_call_output","output":"y"}
	]`)).Array()
	if !responsesToolOutputAssignmentAmbiguous(ambiguous) {
		t.Fatal("two candidates in one cluster must remain ambiguous")
	}

	// 两个 pending 调用 + 单个无 ID 输出（归属二选一不可确定）。
	twoPending := util.ParseGJSONBytesNoCopy([]byte(`[
		{"type":"function_call","call_id":"call_a","name":"tool_a"},
		{"type":"function_call","call_id":"call_b","name":"tool_b"},
		{"type":"function_call_output","output":"only"}
	]`)).Array()
	if !responsesToolOutputAssignmentAmbiguous(twoPending) {
		t.Fatal("single output against two pending calls must remain ambiguous")
	}

	// 跨轮两对 + 末尾一个额外孤立输出（全局 3 个 missing，前两对仍应保留，
	// 末尾孤块按孤儿规则转 user message，不影响前面已确定的配对）。
	mixed := util.ParseGJSONBytesNoCopy([]byte(`[
		{"type":"function_call","call_id":"call_a","name":"lookup"},
		{"type":"function_call_output","output":"result A"},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"next"}]},
		{"type":"function_call","call_id":"call_b","name":"lookup"},
		{"type":"function_call_output","output":"result B"},
		{"type":"function_call_output","output":"stray"}
	]`)).Array()
	if !responsesToolOutputAssignmentAmbiguous(mixed) {
		t.Fatal("trailing two-output cluster (B + stray) is locally ambiguous")
	}
}
