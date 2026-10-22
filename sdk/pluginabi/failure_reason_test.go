package pluginabi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFailureReasonIsOptionalAndCompatibleWithLegacyEnvelope(t *testing.T) {
	legacy, err := NewErrorEnvelope("upstream_error", "Upstream rejected the request", 502)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(legacy), "failure_reason") {
		t.Fatal("legacy envelope gained a diagnostic")
	}
	var envelope Envelope
	if err = json.Unmarshal(legacy, &envelope); err != nil {
		t.Fatal(err)
	}
	envelope.Error.FailureReason = "bigmodel_1301"
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Envelope
	if err = json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Error.FailureReason != "bigmodel_1301" || decoded.Error.Code != "upstream_error" || decoded.Error.HTTPStatus != 502 {
		t.Fatalf("decoded = %+v", decoded.Error)
	}
	var oldReader struct {
		OK    bool `json:"ok"`
		Error struct {
			Code       string `json:"code"`
			Message    string `json:"message"`
			HTTPStatus int    `json:"http_status"`
		} `json:"error"`
	}
	if err = json.Unmarshal(raw, &oldReader); err != nil {
		t.Fatal(err)
	}
	if oldReader.Error.Code != "upstream_error" || oldReader.Error.HTTPStatus != 502 {
		t.Fatal("legacy reader lost existing fields")
	}
}
