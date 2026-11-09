package integration

import (
	"context"
	"fmt"
	"github.com/MoeForge/moego/ojson"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestV2DelegationContract(t *testing.T) {
	for _, client := range []string{"codex_exec/0.163.0", "zcode/1.0"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", client, stream), func(t *testing.T) {
				if isolateDynamic(t) {
					return
				}
				f := newV2Fixture(t, "glm-5.3")
				f.cfg.Client.Codex.OptimizeMultiAgentV2 = true
				post := func(input string) *httptest.ResponseRecorder {
					t.Helper()
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					body := fmt.Sprintf(`{"model":"glm-5.3","stream":%t,"input":%s}`, stream, input)
					req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body)).WithContext(ctx)
					req.Header.Set("Content-Type", "application/json")
					req.Header.Set("User-Agent", client)
					rec := httptest.NewRecorder()
					f.router.ServeHTTP(rec, req)
					return rec
				}

				for _, content := range []string{
					`[{"type":"encrypted_content","encrypted_content":"opaque-delegation-fixture"}]`,
					`[{"type":"input_text","text":"synthetic task envelope"},{"type":"encrypted_content","encrypted_content":"opaque-delegation-fixture"}]`,
				} {
					res := post(`[{"type":"agent_message","content":` + content + `}]`)
					if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "agent_message cannot be sent") {
						t.Fatalf("unreadable delegation: HTTP %d %s", res.Code, res.Body.String())
					}
					if strings.Contains(res.Body.String(), "opaque-delegation-fixture") || strings.Contains(res.Body.String(), "synthetic task envelope") {
						t.Fatal("delegation contents leaked into the error")
					}
				}
				f.transport.lock.Lock()
				modelCalls := len(f.transport.captured)
				f.transport.lock.Unlock()
				f.transport.mu.Lock()
				handshakes := f.transport.handshakes
				f.transport.mu.Unlock()
				if modelCalls != 0 || handshakes != 0 {
					t.Fatalf("rejected delegation reached the subscription: models=%d handshakes=%d", modelCalls, handshakes)
				}

				// The same account must still accept a normal request after the
				// request-scoped failures; neither failure should cool it down.
				res := post(`[{"type":"message","role":"user","content":"ordinary subscription task"}]`)
				if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "fixture-ok") {
					t.Fatalf("ordinary request after rejection: HTTP %d %s", res.Code, res.Body.String())
				}
				f.checkUsage(t, false)

				if strings.HasPrefix(client, "codex_exec/") {
					res = post(`[{"type":"message","role":"user","content":"earlier context"},{"type":"agent_message","content":[{"type":"input_text","text":"plaintext delegated task"}]}]`)
					if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "fixture-ok") {
						t.Fatalf("plaintext delegation: HTTP %d %s", res.Code, res.Body.String())
					}
					f.checkUsage(t, false)
					f.transport.lock.Lock()
					last := append([]byte(nil), f.transport.captured[len(f.transport.captured)-1]...)
					f.transport.lock.Unlock()
					messages := ojson.GetBytes(last, "messages").Array()
					if len(messages) == 0 || messages[len(messages)-1].Get("role").String() != "user" || !strings.Contains(messages[len(messages)-1].Raw, "plaintext delegated task") {
						t.Fatalf("delegated user task was lost or changed role: %s", last)
					}
				}
			})
		}
	}
}
