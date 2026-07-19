package responses

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertCodexResponseToOpenAIResponsesPreservesCacheWriteTokens(t *testing.T) {
	var param any
	upstream := []byte(`data: {"type":"response.completed","response":{"usage":{"input_tokens":1200,"output_tokens":10,"total_tokens":1210,"input_tokens_details":{"cached_tokens":128,"cache_write_tokens":1024}}}}`)

	stream := ConvertCodexResponseToOpenAIResponses(context.Background(), "gpt-5.6-sol", nil, nil, upstream, &param)
	if len(stream) != 1 {
		t.Fatalf("stream chunks = %d, want 1", len(stream))
	}
	if got := gjson.GetBytes(stream[0], "response.usage.input_tokens_details.cache_write_tokens").Int(); got != 1024 {
		t.Fatalf("stream cache_write_tokens = %d, want 1024; output=%s", got, stream[0])
	}

	nonStream := ConvertCodexResponseToOpenAIResponsesNonStream(context.Background(), "gpt-5.6-sol", nil, nil, upstream[len("data: "):], &param)
	if got := gjson.GetBytes(nonStream, "usage.input_tokens_details.cache_write_tokens").Int(); got != 1024 {
		t.Fatalf("non-stream cache_write_tokens = %d, want 1024; output=%s", got, nonStream)
	}
}

func TestConvertCodexResponseToOpenAIResponsesNonStreamIncomplete(t *testing.T) {
	raw := []byte(`{"type":"response.incomplete","response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`)

	out := ConvertCodexResponseToOpenAIResponsesNonStream(context.Background(), "gpt-5.5", nil, nil, raw, nil)

	if got := gjson.GetBytes(out, "status").String(); got != "incomplete" {
		t.Fatalf("status = %q, want incomplete; payload=%s", got, out)
	}
	if got := gjson.GetBytes(out, "incomplete_details.reason").String(); got != "max_output_tokens" {
		t.Fatalf("incomplete reason = %q, want max_output_tokens; payload=%s", got, out)
	}
}
