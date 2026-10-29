package integration

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestReasoningEffortPolicyThreeProtocols(t *testing.T) {
	for _, mode := range []string{"follow", "default", "force"} {
		t.Run(mode, func(t *testing.T) {
			if isolateDynamic(t) {
				return
			}
			models := []string{"glm-5.3", "glm-5.3-flash", "glm-5.3-flashx"}
			f := newV2Fixture(t, models...)
			cfg := f.cfg.CloneForRuntime()
			effort := "max"
			if mode == "follow" {
				effort = ""
			}
			cfg.ReasoningEffort = []config.ReasoningEffortRule{{Models: []string{"glm-*"}, Mode: mode, Effort: effort}}
			// 旧的跨协议 payload 规则仍可存在，但不能抢占专用策略的强度字段。
			cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "glm-*"}}, Params: map[string]any{"reasoning.effort": "low", "reasoning_effort": "low"}}}
			cfg.Payload.Filter = []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "glm-*"}}, Params: []string{"output_config.effort"}}}
			f.host.ApplyConfig(context.Background(), cfg)
			f.manager.SetConfig(cfg)
			inputs := []string{"", "high"}
			if mode == "force" {
				inputs = []string{"low", "medium", "high", "xhigh", "max"}
			}
			for _, model := range models {
				for _, stream := range []bool{false, true} {
					for _, tc := range []struct{ route, body, effortPath string }{
						{"/v1/messages", `{"max_tokens":32000,"thinking":{"type":"adaptive"},"messages":[{"role":"user","content":"hello"}]}`, "output_config.effort"},
						{"/v1/responses", `{"max_output_tokens":32000,"input":"hello"}`, "reasoning.effort"},
						{"/v1/chat/completions", `{"max_tokens":32000,"messages":[{"role":"user","content":"hello"}]}`, "reasoning_effort"},
					} {
						for _, requested := range inputs {
							t.Run(fmt.Sprintf("%s/%s/%s/stream=%t", model, tc.route, requested, stream), func(t *testing.T) {
								body := []byte(tc.body)
								body, _ = sjson.SetBytes(body, "model", model)
								body, _ = sjson.SetBytes(body, "stream", stream)
								if requested != "" {
									body, _ = sjson.SetBytes(body, tc.effortPath, requested)
								}
								recorder := httptest.NewRecorder()
								f.router.ServeHTTP(recorder, httptest.NewRequest("POST", tc.route, strings.NewReader(string(body))))
								if recorder.Code != 200 || !strings.Contains(recorder.Body.String(), "fixture-ok") {
									t.Fatalf("status=%d response=%s", recorder.Code, recorder.Body.String())
								}
								want := requested
								if mode == "force" || mode == "default" && requested == "" {
									want = "max"
								}
								f.transport.lock.Lock()
								wire := f.transport.captured[len(f.transport.captured)-1]
								f.transport.lock.Unlock()
								if gjson.GetBytes(wire, "reasoning_effort").String() != want || gjson.GetBytes(wire, "output_config.effort").Exists() || gjson.GetBytes(wire, "reasoning.effort").Exists() {
									t.Fatalf("unexpected provider effort, want %q: %s", want, wire)
								}
								select {
								case record := <-f.usage.records:
									if record.Failed || record.ReasoningEffort != want || record.Detail.TotalTokens != 12 || record.AuthIndex != "v2-fixture-index" {
										t.Fatalf("usage mismatch: effort=%q failed=%t tokens=%d", record.ReasoningEffort, record.Failed, record.Detail.TotalTokens)
									}
								case <-time.After(3 * time.Second):
									t.Fatal("usage missing")
								}
							})
						}
					}
				}
			}
		})
	}
}
