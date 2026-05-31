package executor

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func validResponsesEncryptedContentForTest() string {
	payload := make([]byte, 1+8+16+16+32)
	payload[0] = 0x80
	for i := 9; i < len(payload); i++ {
		payload[i] = byte(i)
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

func TestIsInvalidResponsesEncryptedContentError(t *testing.T) {
	body := []byte(`{
		"error":{
			"code":"invalid_encrypted_content",
			"type":"invalid_request_error",
			"message":"The encrypted content gAAA...Vw== could not be verified. Reason: Encrypted content could not be decrypted or parsed."
		}
	}`)

	if !isInvalidResponsesEncryptedContentError(http.StatusBadRequest, body) {
		t.Fatalf("expected invalid encrypted content error to be detected")
	}
	if isInvalidResponsesEncryptedContentError(http.StatusInternalServerError, body) {
		t.Fatalf("non-400 response should not trigger encrypted content fallback")
	}
}

func TestSanitizeOpenAIResponsesReasoningEncryptedContent(t *testing.T) {
	valid := validResponsesEncryptedContentForTest()
	raw := []byte(`{
		"model":"gpt-5.4",
		"input":[
			{"type":"message","role":"user","content":"hello","encrypted_content":"message-field"},
			{"type":"reasoning","id":"rs_valid","encrypted_content":"` + valid + `"},
			{"type":"reasoning","id":"rs_bad_prefix","encrypted_content":"not-a-gpt-signature"},
			{"type":"reasoning","id":"rs_null","encrypted_content":null}
		]
	}`)

	got := sanitizeOpenAIResponsesReasoningEncryptedContent(context.Background(), "test executor", raw)
	if gotValid := gjson.GetBytes(got, "input.1.encrypted_content").String(); gotValid != valid {
		t.Fatalf("valid encrypted_content should be preserved, got %q; body=%s", gotValid, got)
	}
	if gjson.GetBytes(got, "input.2.encrypted_content").Exists() {
		t.Fatalf("invalid reasoning encrypted_content should be removed: %s", got)
	}
	if gjson.GetBytes(got, "input.3.encrypted_content").Exists() {
		t.Fatalf("null reasoning encrypted_content should be removed: %s", got)
	}
	if gotMessage := gjson.GetBytes(got, "input.0.encrypted_content").String(); gotMessage != "message-field" {
		t.Fatalf("non-reasoning encrypted_content should be left alone, got %q; body=%s", gotMessage, got)
	}
}

func TestStripInvalidEncryptedContentFromResponsesBody(t *testing.T) {
	raw := []byte(`{
		"model":"gpt-5.4",
		"input":[
			{"type":"message","role":"user","content":"hello"},
			{"type":"reasoning","id":"rs_bad","encrypted_content":"gAAA"},
			{"type":"function_call","call_id":"call_123","name":"lookup","arguments":"{}"},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done","encrypted_content":"nested"}]}
		]
	}`)

	got, changed := stripInvalidEncryptedContentFromResponsesBody(raw)
	if !changed {
		t.Fatalf("expected body to be changed")
	}
	items := gjson.GetBytes(got, "input").Array()
	if len(items) != 3 {
		t.Fatalf("expected reasoning item to be removed, got %d items: %s", len(items), got)
	}
	if typ := gjson.GetBytes(got, "input.0.type").String(); typ != "message" {
		t.Fatalf("first input should remain message, got %q; body=%s", typ, got)
	}
	if typ := gjson.GetBytes(got, "input.1.type").String(); typ != "function_call" {
		t.Fatalf("function call should remain, got %q; body=%s", typ, got)
	}
	if strings.Contains(string(got), "encrypted_content") {
		t.Fatalf("encrypted_content should be removed from retry body: %s", got)
	}
}

func TestCodexExecutorRetriesInvalidEncryptedContentWithStrippedBody(t *testing.T) {
	valid := validResponsesEncryptedContentForTest()
	var bodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		bodies = append(bodies, body)
		if len(bodies) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_encrypted_content","message":"Encrypted content could not be decrypted"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","created_at":1775555723,"status":"completed","model":"gpt-5.4","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model: "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","input":[` +
			`{"type":"message","role":"user","content":"hello"},` +
			`{"type":"reasoning","id":"rs_bad","encrypted_content":"` + valid + `"},` +
			`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done","encrypted_content":"nested"}]}` +
			`]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("request count = %d, want 2", len(bodies))
	}
	if !strings.Contains(string(bodies[0]), "encrypted_content") {
		t.Fatalf("first request should include encrypted_content: %s", bodies[0])
	}
	if got := len(gjson.GetBytes(bodies[1], "input").Array()); got != 2 {
		t.Fatalf("retry input item count = %d, want 2; body=%s", got, bodies[1])
	}
	for _, item := range gjson.GetBytes(bodies[1], "input").Array() {
		if item.Get("type").String() == "reasoning" {
			t.Fatalf("retry request should remove encrypted reasoning items: %s", bodies[1])
		}
		if item.Get("encrypted_content").Exists() {
			t.Fatalf("retry request should strip input encrypted_content fields: %s", bodies[1])
		}
		for _, part := range item.Get("content").Array() {
			if part.Get("encrypted_content").Exists() {
				t.Fatalf("retry request should strip nested encrypted_content fields: %s", bodies[1])
			}
		}
	}
}

func TestOpenAICompatCompactRetriesInvalidEncryptedContentWithStrippedBody(t *testing.T) {
	valid := validResponsesEncryptedContentForTest()
	var bodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		bodies = append(bodies, body)
		if len(bodies) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_encrypted_content","message":"Encrypted content could not be decrypted"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response.compaction","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model: "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","input":[` +
			`{"type":"message","role":"user","content":"hello"},` +
			`{"type":"reasoning","id":"rs_bad","encrypted_content":"` + valid + `"}` +
			`]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Alt:          "responses/compact",
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("request count = %d, want 2", len(bodies))
	}
	if !strings.Contains(string(bodies[0]), "encrypted_content") {
		t.Fatalf("first request should include encrypted_content: %s", bodies[0])
	}
	if got := len(gjson.GetBytes(bodies[1], "input").Array()); got != 1 {
		t.Fatalf("retry input item count = %d, want 1; body=%s", got, bodies[1])
	}
	for _, item := range gjson.GetBytes(bodies[1], "input").Array() {
		if item.Get("type").String() == "reasoning" {
			t.Fatalf("retry request should remove encrypted reasoning items: %s", bodies[1])
		}
		if item.Get("encrypted_content").Exists() {
			t.Fatalf("retry request should strip input encrypted_content fields: %s", bodies[1])
		}
	}
}
