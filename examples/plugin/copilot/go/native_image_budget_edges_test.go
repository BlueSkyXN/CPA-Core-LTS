package main

import (
	"bytes"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestCarbonBudgetContinuationAfterAnswer(t *testing.T) {
	req := budgetFixture(t, "openai", []int{5, 2, 4}, "")
	_, b := budgetWire(t, req)
	m := b["messages"].([]any)
	m = append(m, budgetObj{"role": "assistant", "content": "Reviewed the four tool results."}, budgetObj{"role": "user", "content": "continue"})
	b["messages"] = m
	req.OriginalRequest = budgetJSON(t, b)
	req.Payload = req.OriginalRequest
	out, err := configuredNativePayload(req, pluginConfig{CarbonImageBudget: true})
	if err != nil || bytes.Contains(out, []byte("fixture-05.png")) || bytes.Count(out, []byte(carbonImageOmitted)) != 1 {
		t.Fatalf("text-only follow-up lost its projection: %v", err)
	}
	m[len(m)-1].(budgetObj)["content"] = "compare with the first image"
	req.OriginalRequest = budgetJSON(t, b)
	req.Payload = req.OriginalRequest
	if _, err := configuredNativePayload(req, pluginConfig{CarbonImageBudget: true}); err == nil {
		t.Fatal("guessed the referent of a substantive follow-up")
	}
}

func TestCarbonBudgetScopeAndFailClosed(t *testing.T) {
	on := pluginConfig{CarbonImageBudget: true}
	cfg, err := decodePluginConfig([]byte("carbon_image_budget: true\n"))
	if err != nil || !cfg.CarbonImageBudget || defaultPluginConfig().CarbonImageBudget {
		t.Fatal("unexpected opt-in default")
	}
	for _, tc := range []struct {
		name    string
		batch   []int
		model   string
		enabled bool
	}{{"disabled", []int{5, 2, 4}, carbonImageModel, false}, {"other-model", []int{5, 2, 4}, "gemini-4-argon", true}, {"at-limit", []int{4, 2, 4}, carbonImageModel, true}, {"below-limit", []int{2, 2, 4}, carbonImageModel, true}} {
		t.Run(tc.name, func(t *testing.T) {
			req := budgetFixture(t, "openai-response", tc.batch, "")
			req.Model = tc.model
			baseline, err := nativeRequestPayload(req, false)
			if err != nil {
				t.Fatal(err)
			}
			out, err := configuredNativePayload(req, pluginConfig{CarbonImageBudget: tc.enabled})
			if err != nil || !bytes.Equal(baseline, out) {
				t.Fatal("unexpected change to existing wire payload", err)
			}
		})
	}
	for _, name := range []string{"duplicate-url", "no-source", "unmatched-source", "reordered-source", "missing-tool-result", "untrusted-calls", "wrong-role"} {
		t.Run(name, func(t *testing.T) {
			req := budgetFixture(t, "openai-response", []int{5, 2, 4}, "")
			switch name {
			case "duplicate-url":
				req.OriginalRequest = bytes.ReplaceAll(req.OriginalRequest, []byte("fixture-11.png"), []byte("fixture-01.png"))
				req.Payload = bytes.ReplaceAll(req.Payload, []byte("fixture-11.png"), []byte("fixture-01.png"))
			case "no-source":
				req.OriginalRequest = nil
			case "unmatched-source":
				req.OriginalRequest = bytes.ReplaceAll(req.OriginalRequest, []byte("fixture-01.png"), []byte("other.png"))
			case "reordered-source":
				req.OriginalRequest = bytes.ReplaceAll(req.OriginalRequest, []byte("fixture-03.png"), []byte("tmp.png"))
				req.OriginalRequest = bytes.ReplaceAll(req.OriginalRequest, []byte("fixture-04.png"), []byte("fixture-03.png"))
				req.OriginalRequest = bytes.ReplaceAll(req.OriginalRequest, []byte("tmp.png"), []byte("fixture-04.png"))
			case "missing-tool-result":
				req.Payload = bytes.Replace(req.Payload, []byte(`"tool_call_id":"batch-2-4"`), []byte(`"tool_call_id":"no-such-call"`), 1)
			case "untrusted-calls":
				req.OriginalRequest = bytes.ReplaceAll(req.OriginalRequest, []byte("batch-0-3"), []byte("batch-0-4"))
			case "wrong-role":
				m, b := budgetWire(t, req)
				for _, v := range m {
					msg := v.(budgetObj)
					if msg["role"] == "user" && strings.Contains(string(budgetJSON(t, msg)), "fixture-02.png") {
						msg["role"] = "developer"
						break
					}
				}
				req.Payload = budgetJSON(t, b)
			}
			if _, err := configuredNativePayload(req, on); err == nil {
				t.Fatal("ambiguous request was projected")
			}
		})
	}
}

func TestCarbonBudgetKeepsExplicitWireImageReference(t *testing.T) {
	req := budgetFixture(t, "openai", []int{5, 2, 4}, "")
	msgs, b := budgetWire(t, req)
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i].(budgetObj)
		if m["role"] == "user" && m["content"] == "run the tools" {
			m["content"] = "Read the new files and compare with https://example.com/fixture-05.png"
			break
		}
	}
	req.Payload = budgetJSON(t, b)
	req.OriginalRequest = req.Payload
	out, err := configuredNativePayload(req, pluginConfig{CarbonImageBudget: true})
	if err != nil || !bytes.Contains(out, []byte(`"url":"https://example.com/fixture-05.png"`)) || bytes.Contains(out, []byte(`"url":"https://example.com/fixture-04.png"`)) {
		t.Fatalf("explicit image locator was not protected: %v", err)
	}
}

// The assistant text precedes tool production in the same user task.
func carbonProgressFixture(t *testing.T, req pluginapi.ExecutorRequest) (pluginapi.ExecutorRequest, map[string]any) {
	t.Helper()
	messages, body := budgetWire(t, req)
	for i := len(messages) - 1; i >= 0; i-- {
		m, _ := messages[i].(budgetObj)
		if m["role"] == "user" && m["content"] == "run the tools" {
			m["content"] = []any{
				budgetObj{"type": "text", "text": "run the tools"},
				budgetObj{"type": "image_url", "image_url": budgetObj{"url": "https://example.com/reference.png"}},
			}
			progress := budgetObj{"role": "assistant", "content": "I will load the screenshots now."}
			body["messages"] = slices.Insert(messages, i+1, any(progress))
			req.Payload = budgetJSON(t, body)
			req.OriginalRequest = req.Payload
			return req, body
		}
	}
	t.Fatal("fixture has no instruction for current tools")
	return req, nil
}

func TestCarbonBudgetKeepsCurrentAttachmentAfterProgress(t *testing.T) {
	req, _ := carbonProgressFixture(t, budgetFixture(t, "openai", []int{1, 9}, ""))
	out, err := configuredNativePayload(req, pluginConfig{CarbonImageBudget: true})
	if err != nil || !bytes.Contains(out, []byte(`"url":"https://example.com/reference.png"`)) || bytes.Contains(out, []byte("fixture-01.png")) {
		t.Fatalf("current user attachment or historical selection changed: %v", err)
	}
}

func TestCarbonBudgetNeverLeaksUpstreamBody(t *testing.T) {
	const message = "too many images: maximum allowed for model gemini-4-carbon is 10, got 11"
	for _, tc := range []struct {
		model, code, msg string
		status           int
		wrap, want       bool
	}{{carbonImageModel, "invalid_request_body", message, 400, false, true}, {carbonImageModel, "invalid_request_body", message, 400, true, true}, {"gemini-4-argon", "invalid_request_body", message, 400, true, false}, {carbonImageModel, "other", message, 400, true, false}, {carbonImageModel, "invalid_request_body", message + " private", 400, true, false}, {carbonImageModel, "invalid_request_body", message, 403, true, false}} {
		obj := budgetObj{"code": tc.code, "message": tc.msg, "sensitive": "private"}
		if tc.wrap {
			obj = budgetObj{"error": obj}
		}
		e := copilotChatRefusal(tc.model, tc.status, budgetJSON(t, obj)).(*pluginCallError)
		if e.statusCode != tc.status || e.retryable || strings.Contains(e.message, "private") || (e.code == "image_budget_exceeded") != tc.want {
			t.Fatal("incorrect refusal classification", e.code)
		}
	}
}

func TestCarbonBudgetRepeatableAcrossBranches(t *testing.T) {
	req := budgetFixture(t, "claude", []int{5, 2, 4}, "")
	var first []byte
	for i := 0; i < 3; i++ {
		out, err := configuredNativePayload(req, pluginConfig{CarbonImageBudget: true, ChatCacheBreakpoints: true})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = out
		} else if !bytes.Equal(first, out) {
			t.Fatal("retry changed projection")
		}
		branch := budgetFixture(t, "claude", []int{6, 2, 4}, "")
		if _, err := configuredNativePayload(branch, pluginConfig{CarbonImageBudget: true}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCarbonBudgetParseErrorBody(t *testing.T) {
	for _, b := range [][]byte{nil, []byte("unknown"), []byte(`{"message":42}`)} {
		e := copilotChatRefusal(carbonImageModel, http.StatusBadRequest, b).(*pluginCallError)
		if e.code != "direct_invalid_request" {
			t.Error(e.code)
		}
	}
}
