package usage

import (
	"encoding/json"
	"testing"
)

func TestOutputTimingStrictJSONAndClone(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `{"version":1}`, `{"version":1,"streamed":null}`, `{"version":2,"streamed":true}`, `{"version":1,"streamed":false,"duration_ms":1}`, `{"version":1,"streamed":true,"duration_ms":null}`, `{"version":1,"streamed":true,"duration_ms":-1}`, `{"version":1,"streamed":true,"duration_ms":1.5}`} {
		var v OutputTiming
		if json.Unmarshal([]byte(raw), &v) == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	for _, raw := range []string{`{"version":1,"streamed":false}`, `{"version":1,"streamed":true}`, `{"version":1,"streamed":true,"duration_ms":0}`, `{"version":1,"streamed":true,"duration_ms":100}`} {
		var v OutputTiming
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			t.Fatal(err)
		}
		copy := v.Clone()
		if v.DurationMS != nil {
			*copy.DurationMS = 999
			if *v.DurationMS == 999 {
				t.Fatal("clone shares duration")
			}
		}
	}
}
