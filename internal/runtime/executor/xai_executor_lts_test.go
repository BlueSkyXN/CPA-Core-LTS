package executor

import (
	"context"

	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"github.com/tidwall/gjson"
)

func TestXAIExecutorPrepareDoesNotInjectUnauthorizedXSearch(t *testing.T) {
	t.Parallel()

	exec := NewXAIExecutor(&config.Config{})
	prepared, err := exec.prepareResponsesRequest(context.Background(), cliproxyexecutor.Request{
		Model: "grok-4.5",
		Payload: []byte(`{
			"model":"grok-4.5",
			"input":"answer without tools",
			"tool_choice":"none",
			"parallel_tool_calls":false
		}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       false,
	}, false)
	if err != nil {
		t.Fatalf("prepareResponsesRequest() error = %v", err)
	}
	if gjson.GetBytes(prepared.body, "tools").Exists() {
		t.Fatalf("no-tools request gained x_search: %s", prepared.body)
	}
	if got := gjson.GetBytes(prepared.body, "tool_choice").String(); got != "none" {
		t.Fatalf("tool_choice = %q, want none; body=%s", got, prepared.body)
	}
}

func TestXAIExecutorPrepareCanonicalizesCustomToolChoices(t *testing.T) {
	t.Parallel()

	exec := NewXAIExecutor(&config.Config{})
	prepared, err := exec.prepareResponsesRequest(context.Background(), cliproxyexecutor.Request{
		Model: "grok-4.5",
		Payload: []byte(`{
			"model":"grok-4.5",
			"input":"run the tool",
			"tools":[{"type":"custom","name":"apply_change","parameters":{"type":"object"}}],
			"tool_choice":{"type":"allowed_tools","tools":[{"type":"custom","name":"apply_change"}]}
		}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       false,
	}, false)
	if err != nil {
		t.Fatalf("prepareResponsesRequest() error = %v", err)
	}
	if got := gjson.GetBytes(prepared.body, "tools.0.type").String(); got != "function" {
		t.Fatalf("tools.0.type = %q, want function; body=%s", got, prepared.body)
	}
	if got := gjson.GetBytes(prepared.body, "tool_choice.tools.0.type").String(); got != "function" {
		t.Fatalf("tool_choice.tools.0.type = %q, want function; body=%s", got, prepared.body)
	}
	if got := gjson.GetBytes(prepared.body, "tool_choice.tools.0.name").String(); got != "apply_change" {
		t.Fatalf("tool_choice.tools.0.name = %q, want apply_change; body=%s", got, prepared.body)
	}

	forced, err := exec.prepareResponsesRequest(context.Background(), cliproxyexecutor.Request{
		Model: "grok-4.5",
		Payload: []byte(`{
			"model":"grok-4.5",
			"input":"run the tool",
			"tools":[{"type":"custom","name":"apply_change","parameters":{"type":"object"}}],
			"tool_choice":{"type":"custom","name":"apply_change"}
		}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       false,
	}, false)
	if err != nil {
		t.Fatalf("prepareResponsesRequest() forced choice error = %v", err)
	}
	if got := gjson.GetBytes(forced.body, "tool_choice.type").String(); got != "function" {
		t.Fatalf("forced tool_choice.type = %q, want function; body=%s", got, forced.body)
	}
	if got := gjson.GetBytes(forced.body, "tool_choice.name").String(); got != "apply_change" {
		t.Fatalf("forced tool_choice.name = %q, want apply_change; body=%s", got, forced.body)
	}
}

func TestXAIExecutorPrepareFailsClosedWhenRestrictedChoiceIsOrphaned(t *testing.T) {
	t.Parallel()

	exec := NewXAIExecutor(&config.Config{})
	for _, tt := range []struct {
		name       string
		toolChoice string
	}{
		{
			name:       "allowed tools",
			toolChoice: `{"type":"allowed_tools","tools":[{"type":"image_generation"}]}`,
		},
		{
			name:       "empty allowed tools",
			toolChoice: `{"type":"allowed_tools","tools":[]}`,
		},
		{
			name:       "forced tool",
			toolChoice: `{"type":"image_generation"}`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			prepared, err := exec.prepareResponsesRequest(context.Background(), cliproxyexecutor.Request{
				Model: "grok-4.5",
				Payload: []byte(`{
					"model":"grok-4.5",
					"input":"do not broaden my tool restriction",
					"tools":[
						{"type":"image_generation"},
						{"type":"function","name":"destructive_action","parameters":{"type":"object"}}
					],
					"tool_choice":` + tt.toolChoice + `
				}`),
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FormatOpenAIResponse,
				Stream:       false,
			}, false)
			if err != nil {
				t.Fatalf("prepareResponsesRequest() error = %v", err)
			}
			if got := gjson.GetBytes(prepared.body, "tools.0.name").String(); got != "destructive_action" {
				t.Fatalf("surviving tool = %q, want destructive_action fixture: %s", got, prepared.body)
			}
			if got := gjson.GetBytes(prepared.body, "tool_choice").String(); got != "none" {
				t.Fatalf("orphaned restricted choice = %q, want none: %s", got, prepared.body)
			}
		})
	}
}

func TestXAIExecutorPrepareDropsOrphanedToolChoiceWithoutXSearchInject(t *testing.T) {
	t.Parallel()

	exec := NewXAIExecutor(&config.Config{})
	prepared, err := exec.prepareResponsesRequest(context.Background(), cliproxyexecutor.Request{
		Model: "grok-4.5",

		Payload: []byte(`{
			"model":"grok-4.5",
			"input":"draw something",
			"tools":[{"type":"image_generation"}],
			"tool_choice":{"type":"image_generation"}
		}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       false,
	}, false)
	if err != nil {
		t.Fatalf("prepareResponsesRequest() error = %v", err)
	}

	if gjson.GetBytes(prepared.body, "tools").Exists() {
		t.Fatalf("removed-only tools should not be replaced: %s", prepared.body)
	}
	if got := gjson.GetBytes(prepared.body, "tool_choice").String(); got != "none" {
		t.Fatalf("orphaned image_generation tool_choice = %q, want none: %s", got, prepared.body)
	}
}

func TestXAIExecutorPrepareAllowedToolsDoesNotExpandWithXSearch(t *testing.T) {
	t.Parallel()

	exec := NewXAIExecutor(&config.Config{})
	prepared, err := exec.prepareResponsesRequest(context.Background(), cliproxyexecutor.Request{
		Model: "grok-4.5",

		Payload: []byte(`{
			"model":"grok-4.5",
			"input":"search X",
			"tools":[{"type":"image_generation"},{"type":"function","name":"lookup","parameters":{"type":"object"}}],
			"tool_choice":{"type":"allowed_tools","tools":[
				{"type":"image_generation"},
				{"type":"function","name":"lookup"}
			]}
		}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       false,
	}, false)
	if err != nil {
		t.Fatalf("prepareResponsesRequest() error = %v", err)
	}

	tools := gjson.GetBytes(prepared.body, "tools").Array()
	if len(tools) != 1 {
		t.Fatalf("tools length = %d, want 1; body=%s", len(tools), prepared.body)
	}
	foundLookup := false
	for _, tool := range tools {
		switch tool.Get("type").String() {
		case "function":
			if tool.Get("name").String() == "lookup" {
				foundLookup = true
			}
		case "image_generation":
			t.Fatalf("image_generation must be removed; body=%s", prepared.body)
		}
	}
	if !foundLookup {
		t.Fatalf("expected lookup tool; body=%s", prepared.body)
	}

	allowed := gjson.GetBytes(prepared.body, "tool_choice.tools").Array()
	if len(allowed) != 1 {
		t.Fatalf("tool_choice.tools length = %d, want 1; body=%s", len(allowed), prepared.body)
	}
	if got := allowed[0].Get("name").String(); got != "lookup" {
		t.Fatalf("tool_choice.tools.0.name = %q, want lookup; body=%s", got, prepared.body)
	}
	for _, tool := range allowed {
		if tool.Get("type").String() == "image_generation" {
			t.Fatalf("orphaned image_generation choice leaked: %s", prepared.body)
		}
	}
}

func TestXAIExecutorPrepareDropsParallelToolCallsWithoutToolsEvenForExplicitNone(t *testing.T) {
	t.Parallel()

	exec := NewXAIExecutor(&config.Config{})
	prepared, err := exec.prepareResponsesRequest(context.Background(), cliproxyexecutor.Request{
		Model: "grok-4.5",
		Payload: []byte(`{
			"model":"grok-4.5",
			"input":"answer without tools",
			"tool_choice":"none",
			"parallel_tool_calls":true
		}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatCodex,
		Stream:       false,
	}, false)
	if err != nil {
		t.Fatalf("prepareResponsesRequest() error = %v", err)
	}

	if gjson.GetBytes(prepared.body, "tools").Exists() {
		t.Fatalf("tool_choice:none request gained a tool: %s", prepared.body)
	}
	if got := gjson.GetBytes(prepared.body, "tool_choice").String(); got != "none" {
		t.Fatalf("tool_choice = %q, want none; body=%s", got, prepared.body)
	}
	if gjson.GetBytes(prepared.body, "parallel_tool_calls").Exists() {
		t.Fatalf("parallel_tool_calls should be removed when tools are absent: %s", prepared.body)
	}
}

func TestNormalizeXAIToolChoiceForTools_PreservesNoneButDropsParallelCallsWithoutTools(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		body []byte
	}{
		{
			name: "tools missing",
			body: []byte(`{"model":"grok-4","tool_choice":"none","parallel_tool_calls":true,"input":"hi"}`),
		},
		{
			name: "tools empty",
			body: []byte(`{"model":"grok-4","tools":[],"tool_choice":"none","parallel_tool_calls":true,"input":"hi"}`),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out := normalizeXAIToolChoiceForTools(tt.body)

			if got := gjson.GetBytes(out, "tool_choice").String(); got != "none" {
				t.Fatalf("tool_choice = %q, want none: %s", got, out)
			}
			if gjson.GetBytes(out, "parallel_tool_calls").Exists() {
				t.Fatalf("parallel_tool_calls should be removed without tools: %s", out)
			}
		})
	}
}

func TestLogXAIResolvedBaseURLDoesNotExposeCustomURL(t *testing.T) {
	hook := logtest.NewLocal(log.StandardLogger())
	t.Cleanup(hook.Reset)

	customURL := "https://internal.example.test/private/v1"
	logXAIResolvedBaseURL(context.Background(), customURL)

	for _, entry := range hook.AllEntries() {
		if !strings.Contains(entry.Message, "xai: resolved base URL") {
			continue
		}
		if strings.Contains(entry.Message, customURL) {
			t.Fatalf("xAI resolution log leaked custom URL: %q", entry.Message)
		}
		if !strings.Contains(entry.Message, "source=custom") {
			t.Fatalf("xAI resolution log = %q, want custom source classification", entry.Message)
		}
		return
	}

	t.Fatal("xAI resolution log entry not found")
}
