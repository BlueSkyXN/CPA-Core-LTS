package helps

import "testing"

func TestCacheObservationPresence(t *testing.T) {
	for _, tc := range []struct {
		name, extra, state string
		value              int64
	}{{"absent", "", "absent", 0}, {"zero", `,"prompt_tokens_details":{"cached_tokens":0}`, "present", 0}, {"hit", `,"prompt_tokens_details":{"cached_tokens":64,"cache_write_tokens":128}`, "present", 64}, {"invalid", `,"prompt_tokens_details":{"cached_tokens":"0"}`, "invalid", 0}} {
		t.Run(tc.name, func(t *testing.T) {
			d := ParseOpenAIUsage([]byte(`{"usage":{"prompt_tokens":256,"completion_tokens":1` + tc.extra + `}}`))
			if d.CacheObservation == nil {
				t.Fatal("missing observation")
			}
			r := d.CacheObservation.Read
			if r.State != tc.state {
				t.Fatalf("state=%s want %s", r.State, tc.state)
			}
			if tc.state == "present" && (r.Value == nil || *r.Value != tc.value) {
				t.Fatalf("value=%v", r.Value)
			}
			if tc.state != "present" && r.Value != nil {
				t.Fatal("missing/invalid observation acquired numeric zero")
			}
		})
	}
}
