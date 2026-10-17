package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	statsusage "github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

type businessErrorLogHook struct{ entries []string }

func (h *businessErrorLogHook) Levels() []log.Level { return log.AllLevels }
func (h *businessErrorLogHook) Fire(entry *log.Entry) error {
	if strings.HasPrefix(entry.Message, "Coding Plan upstream failure:") {
		raw, _ := (&logging.LogFormatter{}).Format(entry)
		h.entries = append(h.entries, string(raw))
	}
	return nil
}

func TestBusinessErrorsReachUsageAcrossProtocols(t *testing.T) {
	for _, path := range []string{"/v1/messages", "/v1/chat/completions", "/v1/responses"} {
		for _, mode := range []string{"http", "sse"} {
			t.Run(strings.TrimPrefix(path, "/v1/")+"_"+mode, func(t *testing.T) {
				if isolateDynamic(t) {
					return
				}
				log.SetFormatter(&logging.LogFormatter{})
				f := newV2Fixture(t)
				f.cfg.RequestLog = false
				hook := &businessErrorLogHook{}
				log.AddHook(hook)
				f.transport.errorBody = `{"type":"error","error":{"code":1301,"message":"private-prompt synthetic-secret"}}`
				wantStatus := 400
				if mode == "http" {
					f.transport.errorStatus = wantStatus
				} else {
					f.transport.mode = "business-error"
					wantStatus = 502
				}
				payload := fmt.Sprintf(`{"model":%q,"max_tokens":10,"messages":[{"role":"user","content":"hello"}],"stream":%t}`, v2Model, mode == "sse")
				if path == "/v1/responses" {
					payload = fmt.Sprintf(`{"model":%q,"input":"hello","stream":%t}`, v2Model, mode == "sse")
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				req := httptest.NewRequest("POST", path, strings.NewReader(payload)).WithContext(logging.WithRequestID(ctx, "synthetic-trace"))
				req.Header.Set("Content-Type", "application/json")
				response := httptest.NewRecorder()
				f.router.ServeHTTP(response, req)
				if mode == "http" && response.Code != wantStatus {
					t.Fatalf("HTTP status = %d", response.Code)
				}
				if !strings.Contains(response.Body.String(), "1301") || strings.Contains(response.Body.String(), "private-prompt") {
					t.Fatalf("unsafe/missing client error: %s", response.Body.String())
				}
				var record coreusage.Record
				select {
				case record = <-f.usage.records:
				case <-time.After(time.Second):
					t.Fatal("missing usage")
				}
				if !record.Failed || record.Fail.StatusCode != wantStatus || record.Fail.Body != "bigmodel_1301" {
					t.Fatalf("failure = %+v", record.Fail)
				}
				statsusage.SetStatisticsEnabled(true)
				stats := statsusage.NewRequestStatistics()
				stats.Record(context.Background(), record)
				wire, err := json.Marshal(stats.Snapshot())
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(wire), `"failure_reason":"bigmodel_1301"`) || !strings.Contains(string(wire), fmt.Sprintf(`"failure_status":%d`, wantStatus)) {
					t.Fatalf("usage missing fields: %s", wire)
				}
				if len(hook.entries) != 1 || !strings.Contains(hook.entries[0], "reason=bigmodel_1301") || strings.Contains(hook.entries[0], "private-prompt") || strings.Contains(hook.entries[0], "synthetic-secret") {
					t.Fatalf("diagnostic logs = %q", hook.entries)
				}
				select {
				case <-f.usage.records:
					t.Fatal("duplicate usage")
				case <-time.After(30 * time.Millisecond):
				}
				if len(f.transport.captured) != 1 {
					t.Fatal("unexpected retry")
				}
			})
		}
	}
}
