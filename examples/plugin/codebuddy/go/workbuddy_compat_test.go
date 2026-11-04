package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

const completedWorkBuddyChunk = `{"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`

func TestWorkBuddySSEEventFraming(t *testing.T) {
	for _, ending := range []string{"\n", "\r\n", "\r"} {
		for _, step := range []int{1, 17, 1024} {
			t.Run(fmt.Sprintf("ending=%q/step=%d", ending, step), func(t *testing.T) {
				raw := "\xef\xbb\xbf" + strings.Join([]string{": keepalive", "", "event: ping", "data: heartbeat", "", "event: message", "id: fixture", `data: {"choices":[`, `data: {"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`, "", `data: {"choices":[],"usage":{"total_tokens":7}}`, "", "data: [DONE]", "", ""}, ending)
				v := &sseValidator{}
				var frames [][]byte
				for len(raw) > 0 {
					n := min(step, len(raw))
					got, err := v.consume([]byte(raw[:n]))
					if err != nil {
						t.Fatal(err)
					}
					frames = append(frames, got...)
					raw = raw[n:]
				}
				last, err := v.finish()
				frames = append(frames, last...)
				if err != nil || len(frames) != 2 || !bytes.Contains(frames[0], []byte(`"content":"ok"`)) || !bytes.Contains(frames[1], []byte(`"total_tokens":7`)) {
					t.Fatalf("frames=%q err=%v", frames, err)
				}
			})
		}
	}
}

func TestWorkBuddySSERejectsErrors(t *testing.T) {
	for _, tc := range []struct {
		name, event, data, code string
		status                  int
	}{
		{"finish_error", "", `{"choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":"error"}]}`, "upstream_model_error", 502},
		{"daily_token_limit", "", `{"code":6004,"msg":"` + testSecret + `"}`, "upstream_rate_limited", 429},
		{"nested_rate_limit", "error", `{"error":{"code":"6006","message":"` + testSecret + `"}}`, "upstream_rate_limited", 429},
		{"credits", "", `{"code":14018,"msg":"` + testSecret + `"}`, "upstream_credits_exhausted", 402},
		{"error_event", "error", `{"message":"` + testSecret + `"}`, "upstream_error", 502},
		{"text_error", "error", testSecret, "upstream_error", 502},
		{"empty_error", "error", "", "upstream_error", 502},
		{"error_done", "error", "[DONE]", "upstream_error", 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := &sseValidator{}
			raw := "event: " + tc.event + "\ndata: " + tc.data + "\n\n"
			_, err := v.consume([]byte(raw))
			if err == nil {
				_, err = v.finish()
			}
			var callErr *pluginCallError
			if !errors.As(err, &callErr) || callErr.code != tc.code || callErr.statusCode != tc.status || strings.Contains(err.Error(), testSecret) {
				t.Fatalf("error=%#v", err)
			}
		})
	}
}

func TestWorkBuddySSEFailurePreservesUsage(t *testing.T) {
	v := &sseValidator{}
	raw := `data: {"choices":[{"index":0,"delta":{},"finish_reason":"error"}],"usage":{"prompt_tokens":9,"completion_tokens":2,"total_tokens":11}}` + "\n\n"
	frames, err := v.consume([]byte(raw))
	if err == nil || len(frames) != 1 || bytes.Contains(frames[0], []byte("finish_reason")) || !bytes.Contains(frames[0], []byte(`"total_tokens":11`)) {
		t.Fatalf("failure usage=%q error=%v", frames, err)
	}
	var frame map[string]any
	_ = json.Unmarshal(frames[0], &frame)
	if choices := frame["choices"].([]any); len(choices) != 0 {
		t.Fatal("failed model delta was forwarded")
	}
}

func TestWorkBuddySSEErrorFinishWaitsForUsageTail(t *testing.T) {
	v := &sseValidator{}
	frames, err := v.consume([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"error\"}]}\n\n"))
	if err != nil || len(frames) != 0 {
		t.Fatalf("error finish should wait for its usage tail: frames=%q error=%v", frames, err)
	}
	frames, err = v.consume([]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":9,\"total_tokens\":11}}\n\n"))
	var callErr *pluginCallError
	if !errors.As(err, &callErr) || callErr.code != "upstream_model_error" || len(frames) != 1 || !bytes.Contains(frames[0], []byte(`"total_tokens":11`)) {
		t.Fatalf("usage tail=%q error=%v", frames, err)
	}
}

func TestWorkBuddySSETerminationAndLimits(t *testing.T) {
	for _, raw := range []string{
		"data: [DONE]\n\n",
		"data: {\"choices\":[],\"usage\":{\"total_tokens\":1}}\n\ndata: [DONE]\n\n",
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\ndata: [DONE]\n\n",
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"},{\"index\":1,\"delta\":{\"content\":\"partial\"}}]}\n\ndata: [DONE]\n\n",
	} {
		v := &sseValidator{}
		_, err := v.consume([]byte(raw))
		if err == nil {
			_, err = v.finish()
		}
		if err == nil {
			t.Fatal("incomplete completion became success")
		}
	}
	v := &sseValidator{}
	line := append([]byte("data: "), bytes.Repeat([]byte{'x'}, maxSSELineBytes-16)...)
	line = append(line, '\n')
	for i := 0; i < 6; i++ {
		_, err := v.consume(line)
		if err != nil {
			if !strings.Contains(err.Error(), "event exceeds the bounded limit") {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatal("multiline event bound ignored")
}

func TestWorkBuddySSERejectsInvalidChoiceIndexes(t *testing.T) {
	for _, index := range []string{`"invalid"`, `1.5`, `null`, `-1`, `16`, `999999999999999999999999`} {
		v := &sseValidator{}
		raw := fmt.Sprintf("data: {\"choices\":[{\"index\":%s,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n", index)
		if _, err := v.consume([]byte(raw)); err == nil {
			t.Fatalf("invalid index %s became choice zero", index)
		}
	}
}

func TestWorkBuddyStreamReadErrorPreservesUsage(t *testing.T) {
	host := newFakeHost()
	host.reads = []hostHTTPStreamReadResponse{{Payload: []byte("data: {\"choices\":[],\"usage\":{\"total_tokens\":7}}\n\n"), Error: testSecret}}
	runtime := newPluginRuntime(host)
	if _, err := runtime.executeStream(executorRequestJSON(t, "read-error-usage")); err != nil {
		t.Fatal(err)
	}
	closed := host.waitPluginClose(t)
	if closed.Error == "" || strings.Contains(closed.Error, testSecret) || !bytes.Contains(bytes.Join(host.emittedPayloads(), nil), []byte(`"total_tokens":7`)) {
		t.Fatal("transport failure lost observed usage or exposed the upstream error")
	}
}

func TestWorkBuddyHTTPBusinessErrors(t *testing.T) {
	for _, tc := range []struct {
		name, body      string
		upstream, want  int
		contentTypeJSON bool
	}{
		{"rate_limit", `{"code":6004,"msg":"` + testSecret + `"}`, 400, 429, false},
		{"nested_credits", `{"error":{"code":14018,"message":"` + testSecret + `"}}`, 400, 402, false},
		{"license", `{"code":14015}`, 400, 403, false},
		{"context", `{"code":11115}`, 400, 400, false},
		{"auth_precedence", `{"code":6004}`, 401, 401, false},
		{"html", "<html>" + testSecret + "</html>", 503, 503, false},
		{"json_200", `{"code":6004}`, 200, 429, true},
		{"oversize", strings.Repeat("x", maxCodeBuddyErrorBytes+1), 429, 429, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := newFakeHost()
			host.openResponse.StatusCode = tc.upstream
			if tc.contentTypeJSON {
				host.openResponse.Headers.Set("Content-Type", "application/json")
			}
			host.reads = []hostHTTPStreamReadResponse{{Payload: []byte(tc.body), Done: true}}
			_, err := newPluginRuntime(host).executeStream(executorRequestJSON(t, "http-"+tc.name))
			var callErr *pluginCallError
			if !errors.As(err, &callErr) || callErr.statusCode != tc.want || callErr.retryable != (tc.want == 429 || tc.want >= 500) {
				t.Fatalf("error=%#v", err)
			}
			if strings.Contains(err.Error(), testSecret) || host.openCount() != 1 || host.httpCloseCount() != 1 {
				t.Fatal("error leaked, replayed or left the upstream open")
			}
		})
	}
}

func TestWorkBuddyErrorsReachBothExecutorModes(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			host := newFakeHost()
			host.reads = []hostHTTPStreamReadResponse{{Payload: []byte("data: " + completedWorkBuddyChunk + "\n\ndata: {\"code\":6004,\"usage\":{\"total_tokens\":7}}\n\n")}}
			runtime := newPluginRuntime(host)
			if stream {
				if _, err := runtime.executeStream(executorRequestJSON(t, "stream-error")); err != nil {
					t.Fatal(err)
				}
				closed := host.waitPluginClose(t)
				if closed.HTTPStatus != 429 || closed.ErrorCode != "upstream_rate_limited" {
					t.Fatalf("close=%+v", closed)
				}
				if !bytes.Contains(bytes.Join(host.emittedPayloads(), nil), []byte(`"total_tokens":7`)) {
					t.Fatal("failed stream usage lost")
				}
			} else {
				_, err := runtime.execute(nonStreamRequest(t, "nonstream-error"))
				var callErr *pluginCallError
				if !errors.As(err, &callErr) || callErr.statusCode != 429 {
					t.Fatalf("error=%#v", err)
				}
			}
			if host.openCount() != 1 || host.httpCloseCount() != 1 {
				t.Fatal("failed response replayed or leaked")
			}
		})
	}
}

func TestWorkBuddyMaxTokensCapabilities(t *testing.T) {
	for _, tc := range []struct {
		model, field, body, want string
	}{
		{"hy3", "", `{"max_completion_tokens":17}`, `{"max_completion_tokens":17}`},
		{"gpt-5", "", `{"max_tokens":17}`, `{"max_completion_tokens":17}`},
		{"openai:o3", "", `{"max_tokens":17}`, `{"max_completion_tokens":17}`},
		{"gpt-5", "max_tokens", `{"max_completion_tokens":17}`, `{"max_tokens":17}`},
		{"hy3", "max_completion_tokens", `{"max_tokens":17}`, `{"max_completion_tokens":17}`},
		{"gpt-5", "", `{"max_tokens":17,"max_completion_tokens":23}`, `{"max_tokens":17,"max_completion_tokens":23}`},
	} {
		t.Run(tc.model+"/"+tc.field+"/"+tc.body, func(t *testing.T) {
			var body, want map[string]any
			_ = json.Unmarshal([]byte(tc.body), &body)
			_ = json.Unmarshal([]byte(tc.want), &want)
			normalizeCodeBuddyMaxTokens(body, tc.model, tc.field)
			got, _ := json.Marshal(body)
			encodedWant, _ := json.Marshal(want)
			if !bytes.Equal(got, encodedWant) {
				t.Fatalf("got=%s want=%s", got, encodedWant)
			}
		})
	}
	// 目录能力必须跟随所选 Key 进入真实执行，不能只在解析器里保存。
	host := newFakeHost()
	host.catalogResponse.Body = []byte(`{"code":0,"data":{"models":[{"id":"hy3","compat":{"maxTokensField":"max_completion_tokens"}}],"agents":[{"name":"cli","models":["hy3"]}]}}`)
	host.reads = []hostHTTPStreamReadResponse{{Payload: []byte("data: " + completedWorkBuddyChunk + "\n\ndata: [DONE]\n\n"), Done: true}}
	var req rpcExecutorRequest
	_ = json.Unmarshal(nonStreamRequest(t, "caps"), &req)
	req.Payload = []byte(`{"model":"hy3","messages":[{"role":"developer","content":"keep original instructions"}],"max_tokens":17,"reasoning_effort":"high"}`)
	raw, _ := json.Marshal(req)
	response, err := newPluginRuntime(host).execute(raw)
	if err != nil || len(response.Payload) == 0 {
		t.Fatal(err)
	}
	var body map[string]any
	_ = json.Unmarshal(host.lastOpenRequest().Body, &body)
	if body["max_completion_tokens"] != float64(17) || body["max_tokens"] != nil || body["reasoning_effort"] != "high" {
		t.Fatal("catalog capability or original reasoning lost")
	}
	message := body["messages"].([]any)[0].(map[string]any)
	if message["role"] != "developer" || message["content"] != "keep original instructions" {
		t.Fatal("unverified role or prompt rewriting was introduced")
	}
}
