package integration

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/tidwall/gjson"
)

func TestV3ManagementReadinessAndConfiguration(t *testing.T) {
	if isolateDynamic(t) {
		return
	}
	f := newV2Fixture(t, true)
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
	item := f.cfg.Plugins.Configs["zcode-coding-plan"]
	var obj map[string]any
	if err := item.Raw.Decode(&obj); err != nil {
		t.Fatal(err)
	}
	path, _ := obj["config_file"].(string)
	next, err := config.ParseConfigBytes([]byte(fmt.Sprintf("plugins:\n  enabled: true\n  dir: %q\n  configs:\n    zcode-coding-plan:\n      enabled: true\n      config_file: %q\n      prompt_mode: replace\n      prompt_template: t\n      allow_request_override: false\n", f.cfg.Plugins.Dir, path)))
	if err != nil {
		t.Fatal(err)
	}
	f.host.ApplyConfig(context.Background(), next)
	res := f.post(t, fmt.Sprintf(`{"model":%q,"instructions":"caller","input":"hello"}`, v2Model), "")
	if res.Code != 200 {
		t.Fatalf("execution after config: %s", res.Body.String())
	}
	actual := gjson.GetBytes(f.transport.captured[0], "system").Raw
	if !strings.Contains(actual, "template") || strings.Contains(actual, "caller") {
		t.Fatal("management prompt config not applied")
	}
	f.checkUsage(t, false)
	rejected := f.post(t, fmt.Sprintf(`{"model":%q,"input":"hello"}`, v2Model), `{"mode":"preserve"}`)
	if rejected.Code != 400 || len(f.transport.captured) != 1 {
		t.Fatal("management override restriction bypassed")
	}
}
