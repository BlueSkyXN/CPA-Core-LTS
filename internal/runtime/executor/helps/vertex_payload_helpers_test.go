package helps

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestStripVertexOpenAIResponsesToolCallIDs(t *testing.T) {
	payload := []byte(`{
		"contents":[
			{"role":"model","parts":[
				{"functionCall":{"id":"call-1","name":"lookup","args":{"q":"x"}}},
				{"text":"done"}
			]},
			{"role":"user","parts":[
				{"functionResponse":{"id":"call-1","name":"lookup","response":{"result":"ok"}}}
			]}
		]
	}`)

	out := StripVertexOpenAIResponsesToolCallIDs(payload, "openai-response")

	if gjson.GetBytes(out, "contents.0.parts.0.functionCall.id").Exists() {
		t.Fatalf("functionCall.id should be stripped: %s", out)
	}
	if gjson.GetBytes(out, "contents.1.parts.0.functionResponse.id").Exists() {
		t.Fatalf("functionResponse.id should be stripped: %s", out)
	}
	if got := gjson.GetBytes(out, "contents.0.parts.0.functionCall.name").String(); got != "lookup" {
		t.Fatalf("functionCall.name = %q, want lookup", got)
	}
	if got := gjson.GetBytes(out, "contents.1.parts.0.functionResponse.response.result").String(); got != "ok" {
		t.Fatalf("functionResponse.response.result = %q, want ok", got)
	}
}

func TestStripVertexOpenAIResponsesToolCallIDsKeepsOtherSourceFormats(t *testing.T) {
	payload := []byte(`{"contents":[{"parts":[{"functionCall":{"id":"call-1","name":"lookup"}}]}]}`)

	out := StripVertexOpenAIResponsesToolCallIDs(payload, "openai")

	if string(out) != string(payload) {
		t.Fatalf("payload changed for non-openai-response source: %s", out)
	}
}

func TestStripVertexToolCallIDsReusesPayloadWithoutIDs(t *testing.T) {
	input := []byte(`{"contents":[{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{"id":9007199254740993}}}]}]}`)
	output := StripVertexOpenAIResponsesToolCallIDs(input, "openai-response")
	if &output[0] != &input[0] {
		t.Fatal("payload without tool call IDs was copied")
	}
}

func TestStripVertexToolCallIDsRebuildsContentsOnce(t *testing.T) {
	input := []byte(`{"contents":[{"role":"model","parts":[{"functionCall":{"id":"call_1","name":"lookup","args":{"id":9007199254740993}}}]},{"role":"user","parts":[{"functionResponse":{"id":"call_1","name":"lookup","response":{"id":"keep"}}}]}]}`)
	output := StripVertexOpenAIResponsesToolCallIDs(input, "openai-response")
	if gjson.GetBytes(output, "contents.0.parts.0.functionCall.id").Exists() {
		t.Fatal("functionCall.id was not removed")
	}
	if gjson.GetBytes(output, "contents.1.parts.0.functionResponse.id").Exists() {
		t.Fatal("functionResponse.id was not removed")
	}
	if got := gjson.GetBytes(output, "contents.1.parts.0.functionResponse.response.id").String(); got != "keep" {
		t.Fatalf("nested response id = %q, want keep", got)
	}
	if got := gjson.GetBytes(output, "contents.0.parts.0.functionCall.args.id").Raw; got != "9007199254740993" {
		t.Fatalf("large integer = %s, want exact original value", got)
	}
}

var benchmarkVertexPayloadOutput []byte

func BenchmarkStripVertexToolCallIDsLargeNoopPayload(b *testing.B) {
	input := []byte(`{"contents":[{"role":"user","parts":[{"text":"` + strings.Repeat("x", 8<<20) + `"}]}]}`)
	b.ReportAllocs()
	b.SetBytes(int64(len(input)))
	b.ResetTimer()
	for b.Loop() {
		benchmarkVertexPayloadOutput = StripVertexOpenAIResponsesToolCallIDs(input, "openai-response")
	}
}
