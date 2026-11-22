package pluginabi

import (
	"encoding/json"
	"testing"
)

func TestOptionalExecutorPartialUsageCompatibility(t *testing.T) {
	const old = `{"ok":false,"error":{"code":"stream_truncated","message":"truncated","http_status":502}}`
	var msg Envelope
	if err := json.Unmarshal([]byte(old), &msg); err != nil || msg.Error == nil || len(msg.Error.PartialUsage) != 0 {
		t.Fatal("legacy error did not decode")
	}
	msg.Error.PartialUsage = []byte(`{"total_tokens":0}`)
	wire, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	var legacy struct {
		OK    bool `json:"ok"`
		Error struct {
			Code       string `json:"code"`
			Message    string `json:"message"`
			HTTPStatus int    `json:"http_status"`
		} `json:"error"`
	}
	if err := json.Unmarshal(wire, &legacy); err != nil || legacy.OK || legacy.Error.Code != "stream_truncated" || legacy.Error.Message != "truncated" || legacy.Error.HTTPStatus != 502 {
		t.Fatal("legacy reader lost error semantics")
	}
	var current Envelope
	if err := json.Unmarshal(wire, &current); err != nil || current.Error == nil || string(current.Error.PartialUsage) != `{"total_tokens":0}` {
		t.Fatal("explicit zero lost on round trip")
	}
}
