package helps

import (
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
