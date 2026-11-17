// Round-trip regression tests for #7569 image output: a Gemini image part
// surfaced as an image_generation_call item must survive the next request's
// input back into Gemini as an inlineData model part, keeping image bytes,
// format, model-role turn boundaries, ordering next to text and function
// calls, and the thought signature attached to the part that carried it.
package responses

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

const testImageRoundTripSecondSignature = "EjQKMgEMOdbHO0Gd+c9Mxk4ELwPGbpCEcp2mFfYYLix2UVtBH3fL8GECc4+JITVnHF4qZDsC"

// geminiImageRoundTripOutput converts a Gemini response (streaming or not) to
// Responses output items, exactly as a client would receive them.
func geminiImageRoundTripOutput(t *testing.T, model string, geminiParts []string, stream bool) []ojson.Result {
	t.Helper()
	ctx := context.Background()
	var param any
	if stream {
		var events [][]byte
		for _, chunk := range geminiParts {
			events = append(events, ConvertGeminiResponseToOpenAIResponses(ctx, model, nil, nil, []byte(chunk), &param)...)
		}
		events = append(events, ConvertGeminiResponseToOpenAIResponses(ctx, model, nil, nil, []byte("[DONE]"), &param)...)
		for _, ev := range events {
			event, data := parseSSEEvent(t, ev)
			if event == "response.completed" {
				return data.Get("response.output").Array()
			}
		}
		t.Fatalf("no response.completed event; events: %s", string(eventsToString(events)))
		return nil
	}
	raw := []byte(`{"candidates":[{"content":{"role":"model","parts":[` + strings.Join(geminiParts, ",") + `]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":10,"totalTokenCount":13},"modelVersion":"` + model + `","responseId":"resp_rt"}`)
	result := ConvertGeminiResponseToOpenAIResponsesNonStream(ctx, model, nil, nil, raw, nil)
	return ojson.GetBytesNoCopy(result, "output").Array()
}

// roundTripBackToGemini feeds the converted output items (in order) back as
// the next request's input between two user turns, mirroring client replay.
// extraInput is appended verbatim after the output items (e.g. a function
// call output the client would pair with a replayed call).
func roundTripBackToGemini(t *testing.T, model string, outputItems []ojson.Result, stream bool, extraInput ...string) []byte {
	t.Helper()
	var sb strings.Builder
	sb.WriteString(`{"model":"` + model + `","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"draw"}]}`)
	for _, item := range outputItems {
		sb.WriteString(",")
		sb.WriteString(strings.TrimSpace(item.Raw))
	}
	for _, extra := range extraInput {
		sb.WriteString(",")
		sb.WriteString(extra)
	}
	sb.WriteString(`,{"type":"message","role":"user","content":[{"type":"input_text","text":"again"}]}]}`)
	result, errConvert := ConvertOpenAIResponsesRequestToGemini(model, []byte(sb.String()), stream)
	if errConvert != nil {
		t.Fatalf("ConvertOpenAIResponsesRequestToGemini: %v", errConvert)
	}
	return result
}

func requireContentCount(t *testing.T, result []byte, want int) {
	t.Helper()
	contents := ojson.GetBytesNoCopy(result, "contents")
	if !contents.Exists() || !contents.IsArray() {
		t.Fatalf("no contents array; result: %s", result)
	}
	if got := len(contents.Array()); got != want {
		t.Fatalf("contents count = %d, want %d; result: %s", got, want, result)
	}
}

func requireContentRole(t *testing.T, result []byte, index int, wantRole string) ojson.Result {
	t.Helper()
	content := ojson.GetBytesNoCopy(result, "contents."+itoaImageRT(index))
	if !content.Exists() {
		t.Fatalf("missing contents[%d]; result: %s", index, result)
	}
	if got := content.Get("role").String(); got != wantRole {
		t.Fatalf("contents[%d].role = %q, want %q; result: %s", index, got, wantRole, result)
	}
	return content.Get("parts")
}

func itoaImageRT(i int) string {
	return strconv.Itoa(i)
}

func requireInlineDataPart(t *testing.T, part ojson.Result, wantMime, wantData string) {
	t.Helper()
	inline := part.Get("inline_data")
	if !inline.Exists() {
		inline = part.Get("inlineData")
	}
	if !inline.Exists() {
		t.Fatalf("part has no inlineData: %s", part.Raw)
	}
	mime := inline.Get("mime_type").String()
	if mime == "" {
		mime = inline.Get("mimeType").String()
	}
	if mime != wantMime {
		t.Fatalf("inlineData mime = %q, want %q", mime, wantMime)
	}
	if got := inline.Get("data").String(); got != wantData {
		t.Fatalf("inlineData data = %q, want %q", got, wantData)
	}
}

func findRoundTripOutputItem(items []ojson.Result, itemType string) (ojson.Result, bool) {
	for _, item := range items {
		if item.Get("type").String() == itemType {
			return item, true
		}
	}
	return ojson.Result{}, false
}

func streamChunk(parts, respID string) string {
	return `data: {"candidates":[{"content":{"role":"model","parts":[` + parts + `]}}],"modelVersion":"gemini-3.1-flash-image","responseId":"` + respID + `"}`
}

// A streamed PNG image item must come back as a model-role inlineData part
// with its bytes and format intact, keeping the model turn between the user
// turns instead of dropping it and letting the user turns merge.
func TestImageRoundTripStreamPNG(t *testing.T) {
	output := geminiImageRoundTripOutput(t, "gemini-3.1-flash-image", []string{
		streamChunk(`{"inlineData":{"mimeType":"image/png","data":"UERHREFUQQ=="}}`, "resp_rt1"),
		`data: {"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":10,"totalTokenCount":13},"modelVersion":"gemini-3.1-flash-image","responseId":"resp_rt1"}`,
	}, true)
	if _, ok := findRoundTripOutputItem(output, "image_generation_call"); !ok {
		t.Fatalf("no image_generation_call in stream output")
	}
	result := roundTripBackToGemini(t, "gemini-3.1-flash-image", output, true)
	requireContentCount(t, result, 3)
	parts := requireContentRole(t, result, 1, "model")
	if got := len(parts.Array()); got != 1 {
		t.Fatalf("model parts = %d, want 1; result: %s", got, result)
	}
	requireInlineDataPart(t, parts.Array()[0], "image/png", "UERHREFUQQ==")
}

// Non-streamed JPEG twin of the streaming round trip.
func TestImageRoundTripNonStreamJPEG(t *testing.T) {
	output := geminiImageRoundTripOutput(t, "gemini-3.1-flash-image", []string{
		`{"inlineData":{"mimeType":"image/jpeg","data":"SklGRUJBVEE="}}`,
	}, false)
	if _, ok := findRoundTripOutputItem(output, "image_generation_call"); !ok {
		t.Fatalf("no image_generation_call in non-stream output")
	}
	result := roundTripBackToGemini(t, "gemini-3.1-flash-image", output, false)
	requireContentCount(t, result, 3)
	parts := requireContentRole(t, result, 1, "model")
	requireInlineDataPart(t, parts.Array()[0], "image/jpeg", "SklGRUJBVEE=")
}

// Multiple images of different formats must keep their count and order.
func TestImageRoundTripMultipleImages(t *testing.T) {
	output := geminiImageRoundTripOutput(t, "gemini-3.1-flash-image", []string{
		`{"inlineData":{"mimeType":"image/png","data":"UE5HMQ=="}}`,
		`{"inlineData":{"mimeType":"image/jpeg","data":"SklGRTE="}}`,
	}, false)
	result := roundTripBackToGemini(t, "gemini-3.1-flash-image", output, false)
	requireContentCount(t, result, 3)
	parts := requireContentRole(t, result, 1, "model")
	if got := len(parts.Array()); got != 2 {
		t.Fatalf("model parts = %d, want 2; result: %s", got, result)
	}
	requireInlineDataPart(t, parts.Array()[0], "image/png", "UE5HMQ==")
	requireInlineDataPart(t, parts.Array()[1], "image/jpeg", "SklGRTE=")
}

// Text followed by an image in one model turn must round-trip as one model
// content with the text part first and the image part second.
func TestImageRoundTripTextThenImage(t *testing.T) {
	output := geminiImageRoundTripOutput(t, "gemini-3.1-flash-image", []string{
		`{"text":"here you go"}`,
		`{"inlineData":{"mimeType":"image/png","data":"VEVYVElNRw=="}}`,
	}, false)
	result := roundTripBackToGemini(t, "gemini-3.1-flash-image", output, false)
	requireContentCount(t, result, 3)
	parts := requireContentRole(t, result, 1, "model")
	if got := len(parts.Array()); got != 2 {
		t.Fatalf("model parts = %d, want [text, inlineData]; result: %s", got, result)
	}
	if got := parts.Array()[0].Get("text").String(); got != "here you go" {
		t.Fatalf("text part = %q; result: %s", got, result)
	}
	requireInlineDataPart(t, parts.Array()[1], "image/png", "VEVYVElNRw==")
}

// An image followed by text keeps image-first ordering in the model content.
func TestImageRoundTripImageThenText(t *testing.T) {
	output := geminiImageRoundTripOutput(t, "gemini-3.1-flash-image", []string{
		`{"inlineData":{"mimeType":"image/png","data":"SU1HVEVYVA=="}}`,
		`{"text":"and a caption"}`,
	}, false)
	result := roundTripBackToGemini(t, "gemini-3.1-flash-image", output, false)
	requireContentCount(t, result, 3)
	parts := requireContentRole(t, result, 1, "model")
	if got := len(parts.Array()); got != 2 {
		t.Fatalf("model parts = %d, want [inlineData, text]; result: %s", got, result)
	}
	requireInlineDataPart(t, parts.Array()[0], "image/png", "SU1HVEVYVA==")
	if got := parts.Array()[1].Get("text").String(); got != "and a caption" {
		t.Fatalf("text part = %q; result: %s", got, result)
	}
}

// An image adjacent to a function call keeps both parts in order with their
// own signatures; the image signature must not leak onto the call and vice
// versa.
func TestImageRoundTripImageAdjacentFunctionCall(t *testing.T) {
	output := geminiImageRoundTripOutput(t, "gemini-3.1-flash-image", []string{
		`{"functionCall":{"name":"get_weather","args":{"city":"York"}}}`,
		`{"inlineData":{"mimeType":"image/png","data":"RkNBTExJTw=="}}`,
	}, false)
	call, ok := findRoundTripOutputItem(output, "function_call")
	if !ok {
		t.Fatalf("no function_call in output")
	}
	result := roundTripBackToGemini(t, "gemini-3.1-flash-image", output, false,
		`{"type":"function_call_output","call_id":"`+call.Get("call_id").String()+`","output":"{\"result\":\"12C\"}"}`)
	requireContentCount(t, result, 4)
	parts := requireContentRole(t, result, 1, "model")
	if got := len(parts.Array()); got != 2 {
		t.Fatalf("model parts = %d, want [functionCall, inlineData]; result: %s", got, result)
	}
	if name := parts.Array()[0].Get("functionCall.name").String(); name != "get_weather" {
		t.Fatalf("functionCall name = %q; result: %s", name, result)
	}
	if sig := parts.Array()[0].Get("thoughtSignature").String(); sig == testResponsesGeminiThoughtSignature {
		t.Fatalf("function call must not carry an image signature; result: %s", result)
	}
	requireInlineDataPart(t, parts.Array()[1], "image/png", "RkNBTExJTw==")
}

// A signed image next to a function call keeps the signature on the image and
// off the call.
func TestImageRoundTripSignatureDoesNotLeakToAdjacentFunctionCall(t *testing.T) {
	output := geminiImageRoundTripOutput(t, "gemini-3.1-flash-image", []string{
		`{"inlineData":{"mimeType":"image/png","data":"U0lHZkM="},"thoughtSignature":"` + testResponsesGeminiThoughtSignature + `"}`,
		`{"functionCall":{"name":"get_weather","args":{"city":"York"}}}`,
	}, false)
	call, ok := findRoundTripOutputItem(output, "function_call")
	if !ok {
		t.Fatalf("no function_call in output")
	}
	result := roundTripBackToGemini(t, "gemini-3.1-flash-image", output, false,
		`{"type":"function_call_output","call_id":"`+call.Get("call_id").String()+`","output":"{\"result\":\"12C\"}"}`)
	parts := requireContentRole(t, result, 1, "model")
	if got := len(parts.Array()); got != 2 {
		t.Fatalf("model parts = %d, want [inlineData, functionCall]; result: %s", got, result)
	}
	requireInlineDataPart(t, parts.Array()[0], "image/png", "U0lHZkM=")
	if got := parts.Array()[0].Get("thoughtSignature").String(); got != testResponsesGeminiThoughtSignature {
		t.Fatalf("image signature = %q; result: %s", got, result)
	}
	if sig := parts.Array()[1].Get("thoughtSignature").String(); sig == testResponsesGeminiThoughtSignature {
		t.Fatalf("image signature leaked onto the function call; result: %s", result)
	}
}

// A streamed image part carrying a thought signature must return the
// signature on the image's own inlineData part.
func TestImageRoundTripSignatureStream(t *testing.T) {
	output := geminiImageRoundTripOutput(t, "gemini-3.1-flash-image", []string{
		streamChunk(`{"inlineData":{"mimeType":"image/png","data":"U0lHUVNUUg=="},"thoughtSignature":"`+testResponsesGeminiThoughtSignature+`"}`, "resp_rt2"),
		`data: {"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":10,"totalTokenCount":13},"modelVersion":"gemini-3.1-flash-image","responseId":"resp_rt2"}`,
	}, true)
	result := roundTripBackToGemini(t, "gemini-3.1-flash-image", output, true)
	requireContentCount(t, result, 3)
	parts := requireContentRole(t, result, 1, "model")
	if got := len(parts.Array()); got != 1 {
		t.Fatalf("model parts = %d, want 1 signed image; result: %s", got, result)
	}
	requireInlineDataPart(t, parts.Array()[0], "image/png", "U0lHUVNUUg==")
	if got := parts.Array()[0].Get("thoughtSignature").String(); got != testResponsesGeminiThoughtSignature {
		t.Fatalf("image thoughtSignature = %q, want %q; result: %s", got, testResponsesGeminiThoughtSignature, result)
	}
}

// Non-streamed twin of the signature round trip.
func TestImageRoundTripSignatureNonStream(t *testing.T) {
	output := geminiImageRoundTripOutput(t, "gemini-3.1-flash-image", []string{
		`{"inlineData":{"mimeType":"image/jpeg","data":"TlNJRw=="},"thoughtSignature":"` + testResponsesGeminiThoughtSignature + `"}`,
	}, false)
	result := roundTripBackToGemini(t, "gemini-3.1-flash-image", output, false)
	requireContentCount(t, result, 3)
	parts := requireContentRole(t, result, 1, "model")
	requireInlineDataPart(t, parts.Array()[0], "image/jpeg", "TlNJRw==")
	if got := parts.Array()[0].Get("thoughtSignature").String(); got != testResponsesGeminiThoughtSignature {
		t.Fatalf("image thoughtSignature = %q, want %q; result: %s", got, testResponsesGeminiThoughtSignature, result)
	}
}

// The image's signature must stay on the image part even when visible text
// precedes it in the same model turn; it must not be re-attached to the text.
func TestImageRoundTripSignatureStaysOnImageNextToText(t *testing.T) {
	output := geminiImageRoundTripOutput(t, "gemini-3.1-flash-image", []string{
		`{"text":"plain intro"}`,
		`{"inlineData":{"mimeType":"image/png","data":"WFRYSU1HQQ=="},"thoughtSignature":"` + testResponsesGeminiThoughtSignature + `"}`,
	}, false)
	result := roundTripBackToGemini(t, "gemini-3.1-flash-image", output, false)
	requireContentCount(t, result, 3)
	parts := requireContentRole(t, result, 1, "model")
	if got := len(parts.Array()); got != 2 {
		t.Fatalf("model parts = %d, want [text, signed image]; result: %s", got, result)
	}
	if sig := parts.Array()[0].Get("thoughtSignature").String(); sig != "" {
		t.Fatalf("text part must not carry the image signature (got %q); result: %s", sig, result)
	}
	requireInlineDataPart(t, parts.Array()[1], "image/png", "WFRYSU1HQQ==")
	if got := parts.Array()[1].Get("thoughtSignature").String(); got != testResponsesGeminiThoughtSignature {
		t.Fatalf("image thoughtSignature = %q, want %q; result: %s", got, testResponsesGeminiThoughtSignature, result)
	}
}

// Two images with two different signatures keep each signature on its own
// part.
func TestImageRoundTripDistinctSignaturesPerImage(t *testing.T) {
	output := geminiImageRoundTripOutput(t, "gemini-3.1-flash-image", []string{
		`{"inlineData":{"mimeType":"image/png","data":"RElTVDE="},"thoughtSignature":"` + testResponsesGeminiThoughtSignature + `"}`,
		`{"inlineData":{"mimeType":"image/png","data":"RElTVDI="},"thoughtSignature":"` + testImageRoundTripSecondSignature + `"}`,
	}, false)
	result := roundTripBackToGemini(t, "gemini-3.1-flash-image", output, false)
	requireContentCount(t, result, 3)
	parts := requireContentRole(t, result, 1, "model")
	if got := len(parts.Array()); got != 2 {
		t.Fatalf("model parts = %d, want 2; result: %s", got, result)
	}
	if got := parts.Array()[0].Get("thoughtSignature").String(); got != testResponsesGeminiThoughtSignature {
		t.Fatalf("image 1 signature = %q; result: %s", got, result)
	}
	if got := parts.Array()[1].Get("thoughtSignature").String(); got != testImageRoundTripSecondSignature {
		t.Fatalf("image 2 signature = %q; result: %s", got, result)
	}
}

// An unsigned image part must not gain a thoughtSignature field.
func TestImageRoundTripUnsignedImageStaysUnsigned(t *testing.T) {
	output := geminiImageRoundTripOutput(t, "gemini-3.1-flash-image", []string{
		`{"inlineData":{"mimeType":"image/png","data":"VU5TSUdOQQ=="}}`,
	}, false)
	result := roundTripBackToGemini(t, "gemini-3.1-flash-image", output, false)
	parts := requireContentRole(t, result, 1, "model")
	if sig := parts.Array()[0].Get("thoughtSignature"); sig.Exists() {
		t.Fatalf("unsigned image gained thoughtSignature %q; result: %s", sig.String(), result)
	}
}

// Thought (draft) images stay filtered out of the output, so nothing image
// shaped flows back on replay.
func TestImageRoundTripThoughtImageStillFiltered(t *testing.T) {
	output := geminiImageRoundTripOutput(t, "gemini-3.1-flash-image", []string{
		`{"thought":true,"inlineData":{"mimeType":"image/png","data":"RFJBRlQ="}}`,
		`{"text":"done"}`,
	}, false)
	if _, ok := findRoundTripOutputItem(output, "image_generation_call"); ok {
		t.Fatalf("thought image leaked into output items")
	}
	result := roundTripBackToGemini(t, "gemini-3.1-flash-image", output, false)
	if strings.Contains(string(result), "RFJBRlQ=") {
		t.Fatalf("thought image bytes survived round trip; result: %s", result)
	}
}
