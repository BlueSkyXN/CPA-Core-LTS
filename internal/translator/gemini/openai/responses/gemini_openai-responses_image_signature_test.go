// Regression tests for image part signature ownership across the Gemini
// Responses round trip: equal signature values on distinct images or parts
// are each preserved, a text signature never absorbs an image signature, and
// a signature arriving late on an already-emitted image frame stays bound to
// that image instead of the following caption. Built on the round-trip
// helpers in gemini_openai-responses_image_roundtrip_test.go.
package responses

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
)

// signedImagePart builds an inlineData part carrying the given signature.
func signedImagePart(data string) string {
	return `{"inlineData":{"mimeType":"image/png","data":"` + data + `"},"thoughtSignature":"` + testResponsesGeminiThoughtSignature + `"}`
}

// requireSignedImagePart asserts the part is an inlineData part with the
// expected data and signature.
func requireSignedImagePart(t *testing.T, part ojson.Result, wantData, wantSignature string) {
	t.Helper()
	requireInlineDataPart(t, part, "image/png", wantData)
	if got := part.Get("thoughtSignature").String(); got != wantSignature {
		t.Fatalf("image thoughtSignature = %q, want %q; part: %s", got, wantSignature, part.Raw)
	}
}

// Two distinct images carrying the same signature value must each keep it:
// signature deduplication is owned by the image frame, not the value.
func TestImageRoundTripSameSignatureTwoImages(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(streamName(stream), func(t *testing.T) {
			var input []string
			if stream {
				input = []string{
					streamChunk(signedImagePart("QUFB"), "rt_same_sig"),
					streamChunk(signedImagePart("QkJC"), "rt_same_sig"),
					`data: {"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"modelVersion":"gemini-3.1-flash-image","responseId":"rt_same_sig"}`,
				}
			} else {
				input = []string{signedImagePart("QUFB"), signedImagePart("QkJC")}
			}
			output := geminiImageRoundTripOutput(t, "gemini-3.1-flash-image", input, stream)
			result := roundTripBackToGemini(t, "gemini-3.1-flash-image", output, stream)
			requireContentCount(t, result, 3)
			parts := requireContentRole(t, result, 1, "model")
			if got := len(parts.Array()); got != 2 {
				t.Fatalf("model parts = %d, want 2 images; result: %s", got, result)
			}
			requireSignedImagePart(t, parts.Array()[0], "QUFB", testResponsesGeminiThoughtSignature)
			requireSignedImagePart(t, parts.Array()[1], "QkJC", testResponsesGeminiThoughtSignature)
		})
	}
}

// A signed text part followed by an image with the same signature value:
// the text keeps its signature and the image keeps its own copy.
func TestImageRoundTripSameSignatureTextThenImage(t *testing.T) {
	signedText := `{"text":"caption","thoughtSignature":"` + testResponsesGeminiThoughtSignature + `"}`
	for _, stream := range []bool{false, true} {
		t.Run(streamName(stream), func(t *testing.T) {
			var input []string
			if stream {
				input = []string{
					streamChunk(signedText, "rt_text_img"),
					streamChunk(signedImagePart("QUFB"), "rt_text_img"),
					`data: {"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"modelVersion":"gemini-3.1-flash-image","responseId":"rt_text_img"}`,
				}
			} else {
				input = []string{signedText, signedImagePart("QUFB")}
			}
			output := geminiImageRoundTripOutput(t, "gemini-3.1-flash-image", input, stream)
			result := roundTripBackToGemini(t, "gemini-3.1-flash-image", output, stream)
			requireContentCount(t, result, 3)
			parts := requireContentRole(t, result, 1, "model")
			if got := len(parts.Array()); got != 2 {
				t.Fatalf("model parts = %d, want [text, image]; result: %s", got, result)
			}
			if got := parts.Array()[0].Get("text").String(); got != "caption" {
				t.Fatalf("text part = %q; result: %s", got, result)
			}
			if got := parts.Array()[0].Get("thoughtSignature").String(); got != testResponsesGeminiThoughtSignature {
				t.Fatalf("text thoughtSignature = %q; result: %s", got, result)
			}
			requireSignedImagePart(t, parts.Array()[1], "QUFB", testResponsesGeminiThoughtSignature)
		})
	}
}

// A signature arriving late on an image frame already emitted must stay
// bound to that image: the image keeps the signature and the following
// caption does not gain it.
func TestImageRoundTripLateSignatureStaysOnImage(t *testing.T) {
	output := geminiImageRoundTripOutput(t, "gemini-3.1-flash-image", []string{
		streamChunk(`{"partIndex":0,"inlineData":{"mimeType":"image/png","data":"QUFB"}}`, "rt_late"),
		streamChunk(`{"partIndex":0,"inlineData":{"mimeType":"image/png","data":"QUFB"},"thoughtSignature":"`+testResponsesGeminiThoughtSignature+`"}`, "rt_late"),
		streamChunk(`{"partIndex":1,"text":"caption"}`, "rt_late"),
		`data: {"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"modelVersion":"gemini-3.1-flash-image","responseId":"rt_late"}`,
	}, true)
	result := roundTripBackToGemini(t, "gemini-3.1-flash-image", output, true)
	requireContentCount(t, result, 3)
	parts := requireContentRole(t, result, 1, "model")
	if got := len(parts.Array()); got != 2 {
		t.Fatalf("model parts = %d, want [image, text]; result: %s", got, result)
	}
	requireSignedImagePart(t, parts.Array()[0], "QUFB", testResponsesGeminiThoughtSignature)
	if got := parts.Array()[1].Get("text").String(); got != "caption" {
		t.Fatalf("text part = %q; result: %s", got, result)
	}
	if sig := parts.Array()[1].Get("thoughtSignature").String(); sig == testResponsesGeminiThoughtSignature {
		t.Fatalf("late image signature leaked onto caption; result: %s", result)
	}
}

// Duplicate events of the same image part (same part index and bytes) must
// not emit a second image or duplicate signature carriers.
func TestImageRoundTripDuplicatePartEventNoDoubleCarrier(t *testing.T) {
	output := geminiImageRoundTripOutput(t, "gemini-3.1-flash-image", []string{
		streamChunk(`{"partIndex":0,"inlineData":{"mimeType":"image/png","data":"QUFB"},"thoughtSignature":"`+testResponsesGeminiThoughtSignature+`"}`, "rt_dup"),
		streamChunk(`{"partIndex":0,"inlineData":{"mimeType":"image/png","data":"QUFB"},"thoughtSignature":"`+testResponsesGeminiThoughtSignature+`"}`, "rt_dup"),
		`data: {"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"modelVersion":"gemini-3.1-flash-image","responseId":"rt_dup"}`,
	}, true)
	result := roundTripBackToGemini(t, "gemini-3.1-flash-image", output, true)
	requireContentCount(t, result, 3)
	parts := requireContentRole(t, result, 1, "model")
	if got := len(parts.Array()); got != 1 {
		t.Fatalf("model parts = %d, want 1 image; result: %s", got, result)
	}
	requireSignedImagePart(t, parts.Array()[0], "QUFB", testResponsesGeminiThoughtSignature)
}

// Distinct images on distinct parts carrying different signatures keep each
// signature on its own image (complements the equal-value case above).
func TestImageRoundTripDifferentPartsDifferentSignaturesStream(t *testing.T) {
	output := geminiImageRoundTripOutput(t, "gemini-3.1-flash-image", []string{
		streamChunk(`{"partIndex":0,"inlineData":{"mimeType":"image/png","data":"RElTVDE="},"thoughtSignature":"`+testResponsesGeminiThoughtSignature+`"}`, "rt_parts"),
		streamChunk(`{"partIndex":1,"inlineData":{"mimeType":"image/png","data":"RElTVDI="},"thoughtSignature":"`+testImageRoundTripSecondSignature+`"}`, "rt_parts"),
		`data: {"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"modelVersion":"gemini-3.1-flash-image","responseId":"rt_parts"}`,
	}, true)
	result := roundTripBackToGemini(t, "gemini-3.1-flash-image", output, true)
	requireContentCount(t, result, 3)
	parts := requireContentRole(t, result, 1, "model")
	if got := len(parts.Array()); got != 2 {
		t.Fatalf("model parts = %d, want 2 images; result: %s", got, result)
	}
	requireSignedImagePart(t, parts.Array()[0], "RElTVDE=", testResponsesGeminiThoughtSignature)
	requireSignedImagePart(t, parts.Array()[1], "RElTVDI=", testImageRoundTripSecondSignature)
}

func streamName(stream bool) string {
	if stream {
		return "stream"
	}
	return "non_stream"
}
