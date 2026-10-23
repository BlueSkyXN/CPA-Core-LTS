package integration

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestOutputTokenBudget(t *testing.T) {
	if isolateDynamic(t) {
		return
	}
	models := []string{"glm-5.3", "glm-5.3-flash"}
	f := newV2Fixture(t, models...)
	for _, model := range models {
		for _, stream := range []bool{false, true} {
			for _, tc := range []struct {
				name, path, fields string
				want               int64
			}{
				{"responses default", "/v1/responses", `"input":"hello"`, 64000},
				{"responses explicit smaller", "/v1/responses", `"input":"hello","max_output_tokens":4096`, 4096},
				{"responses explicit larger", "/v1/responses", `"input":"hello","max_output_tokens":128000`, 128000},
				{"responses model ceiling", "/v1/responses", `"input":"hello","max_output_tokens":128001`, 128000},
				{"chat default", "/v1/chat/completions", `"messages":[{"role":"user","content":"hello"}]`, 64000},
				{"chat explicit smaller", "/v1/chat/completions", `"messages":[{"role":"user","content":"hello"}],"max_tokens":4096`, 4096},
				{"chat explicit larger", "/v1/chat/completions", `"messages":[{"role":"user","content":"hello"}],"max_completion_tokens":128000`, 128000},
				{"native messages", "/v1/messages", `"messages":[{"role":"user","content":"hello"}],"max_tokens":4096`, 4096},
			} {
				t.Run(fmt.Sprintf("%s/%s/stream=%t", model, tc.name, stream), func(t *testing.T) {
					body := fmt.Sprintf(`{"model":%q,"stream":%t,%s}`, model, stream, tc.fields)
					rec := httptest.NewRecorder()
					f.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(body)))
					if rec.Code != http.StatusOK {
						t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
					}
					f.checkUsage(t, false)
					f.transport.lock.Lock()
					defer f.transport.lock.Unlock()
					if len(f.transport.captured) == 0 {
						t.Fatal("upstream request missing")
					}
					wire := f.transport.captured[len(f.transport.captured)-1]
					if got := gjson.GetBytes(wire, "max_tokens").Int(); got != tc.want {
						t.Fatalf("upstream max_tokens = %d, want %d", got, tc.want)
					}
				})
			}
		}
	}
}
