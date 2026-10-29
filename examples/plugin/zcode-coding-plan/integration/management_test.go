package integration

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/tidwall/gjson"
)

func TestV3ManagementReadinessAndConfiguration(t *testing.T) {
	if isolateDynamic(t) {
		return
	}
	f := newV2Fixture(t)
	get := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRecorder()
		f.router.ServeHTTP(r, httptest.NewRequest("GET", path, nil))
		return r
	}
	list := get("/v0/management/plugins")
	if !gjson.GetBytes(list.Body.Bytes(), "plugins.0.supports_auth").Bool() || gjson.GetBytes(list.Body.Bytes(), "plugins.0.supports_oauth").Bool() || !gjson.GetBytes(list.Body.Bytes(), "plugins.0.supports_readiness").Bool() {
		t.Fatal("plugin management capabilities missing")
	}
	unknown := get("/v0/management/plugins/zcode-coding-plan/readiness")
	if unknown.Code != 200 || gjson.GetBytes(unknown.Body.Bytes(), "Ready").Bool() {
		t.Fatal("provider probe falsely reported account readiness")
	}
	ready := get("/v0/management/plugins/zcode-coding-plan/readiness?auth_index=v2-fixture-index")
	if ready.Code != 200 || !gjson.GetBytes(ready.Body.Bytes(), "Ready").Bool() {
		t.Fatalf("readiness failed: %s", ready.Body.String())
	}
	if f.transport.handshakes != 0 || len(f.transport.captured) != 0 {
		t.Fatal("diagnostic called upstream")
	}
	if strings.Contains(ready.Body.String(), "synthetic-key") || strings.Contains(ready.Body.String(), "synthetic-device") {
		t.Fatal("private data in diagnostic")
	}
	next, err := config.ParseConfigBytes([]byte(fmt.Sprintf("plugins:\n  enabled: true\n  dir: %q\n  configs:\n    zcode-coding-plan:\n      enabled: true\n      host_logging_disabled: true\n      upstream: zai\n      models: [%q]\n", f.cfg.Plugins.Dir, v2Model)))
	if err != nil {
		t.Fatal(err)
	}
	f.host.ApplyConfig(context.Background(), next)
	res := f.post(t, fmt.Sprintf(`{"model":%q,"instructions":"caller","input":"hello"}`, v2Model))
	if res.Code != 200 {
		t.Fatalf("execution after config: %s", res.Body.String())
	}
	actual := gjson.GetBytes(f.transport.captured[0], "system").Raw
	if !strings.Contains(actual, "caller") {
		t.Fatal("caller system prompt not preserved")
	}
	f.checkUsage(t, false)
	rejected := f.post(t, fmt.Sprintf(`{"model":%q,"input":"hello","x_coding_plan":{"prompt":{"mode":"preserve"}}}`, v2Model))
	if rejected.Code != 400 || len(f.transport.captured) != 1 {
		t.Fatal("in-body prompt override accepted")
	}
}
