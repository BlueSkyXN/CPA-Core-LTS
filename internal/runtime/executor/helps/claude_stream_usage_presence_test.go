package helps

import "testing"

func TestClaudeStreamUsageFieldPresence(t *testing.T) {
	cases := []struct {
		name, start, delta             string
		input, cache, creation, output int64
	}{
		{"partial cache update", `{"input_tokens":13,"cache_read_input_tokens":100,"cache_creation_input_tokens":7}`, `{"cache_read_input_tokens":22000,"cache_creation_input_tokens":31,"output_tokens":4}`, 22044, 22000, 31, 4},
		{"explicit zero", `{"input_tokens":100,"cache_read_input_tokens":900}`, `{"input_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":5}`, 0, 0, 0, 5},
		{"output only", `{"input_tokens":13,"cache_read_input_tokens":100,"cache_creation_input_tokens":7}`, `{"output_tokens":4}`, 120, 100, 7, 4},
		{"uncached only", `{"input_tokens":13,"cache_read_input_tokens":100,"cache_creation_input_tokens":7}`, `{"input_tokens":20,"output_tokens":4}`, 127, 100, 7, 4},
		{"zero output", `{"input_tokens":13,"output_tokens":7}`, `{"output_tokens":0}`, 13, 0, 0, 0},
	}
	for _, tc := range cases {
		for _, plugin := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: "/native", true: "/plugin"}[plugin], func(t *testing.T) {
				var buffer StreamUsageBuffer
				for _, line := range []string{
					`data: {"type":"message_start","message":{"usage":` + tc.start + `}}`,
					`data: {"type":"message_delta","usage":` + tc.delta + `}`,
				} {
					if plugin {
						ObservePluginExecutorStreamUsage("claude", []byte(line), &buffer)
					} else {
						buffer.ObserveClaudeStream([]byte(line))
					}
				}
				got, ok := buffer.Detail()
				if !ok || got.InputTokens != tc.input || got.CachedTokens != tc.cache || got.CacheCreationTokens != tc.creation || got.OutputTokens != tc.output || got.TotalTokens != tc.input+tc.output {
					t.Fatalf("usage=%+v, want input=%d cache=%d creation=%d output=%d", got, tc.input, tc.cache, tc.creation, tc.output)
				}
			})
		}
	}
}

func TestClaudeStreamUsageNewMessageResetsSnapshot(t *testing.T) {
	var buffer StreamUsageBuffer
	buffer.ObserveClaudeStream([]byte(`data: {"type":"message_start","message":{"usage":{"input_tokens":50,"cache_read_input_tokens":900}}}`))
	buffer.ObserveClaudeStream([]byte(`data: {"type":"message_start","message":{"usage":{"input_tokens":20}}}`))
	got, _ := buffer.Detail()
	if got.InputTokens != 20 || got.CachedTokens != 0 {
		t.Fatalf("prior message usage leaked: %+v", got)
	}
}
