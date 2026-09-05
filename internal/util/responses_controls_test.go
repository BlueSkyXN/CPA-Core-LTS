package util

import (
	"bytes"
	"strings"
	"testing"
)

func TestResponsesControlsPreserveHistoryAndSelectExplicitEffort(t *testing.T) {
	body := []byte(`{"reasoning":{"effort":"medium"},"prompt_cache_key":"session-1","input":[{"type":"message","role":"user","content":"one"},{"type":"configuration_update","reasoning":{"effort":"high"}},{"type":"function_call_output","call_id":"old-1","output":"done"},{"type":"configuration_update","reasoning":{"effort":"max"}}],"tools":[{"type":"function","name":"slow","async":true}]}`)
	original := bytes.Clone(body)
	got, err := InspectResponsesControls(body)
	if err != nil || got.ConfigurationEffort != "max" || !got.HasAsyncTools || !got.HasConfigurationUpdate {
		t.Fatalf("controls = %+v, err = %v", got, err)
	}
	if !bytes.Equal(body, original) {
		t.Fatal("inspection changed the prompt prefix/history")
	}
	if err := ValidateResponsesControls(body, false); err != nil {
		t.Fatal(err)
	}
}

func TestResponsesControlsValidation(t *testing.T) {
	for _, tt := range []struct {
		name, body, errorPart string
		compact               bool
	}{
		{"escaped discriminator", `{"input":[{"type":"configuration_\u0075pdate","reasoning":{"effort":"high"}}]}`, "", false},
		{"ordinary", `{"input":"configuration_update in ordinary text"}`, "", false},
		{"message text", `{"input":[{"type":"message","role":"user","content":"configuration_update"}]}`, "", false},
		{"string async input", `{"input":"hi","tools":[{"type":"function","async":true}]}`, "", false},
		{"missing effort", `{"input":[{"type":"configuration_update"}]}`, "requires a non-empty", false},
		{"non-string effort", `{"input":[{"type":"configuration_update","reasoning":{"effort":3}}]}`, "requires a non-empty", false},
		{"adjacent", `{"input":[{"type":"configuration_update","reasoning":{"effort":"high"}},{"type":"configuration_update","reasoning":{"effort":"max"}}]}`, "adjacent", false},
		{"auto truncation", `{"truncation":"auto","input":[{"type":"configuration_update","reasoning":{"effort":"high"}}]}`, "automatic truncation", false},
		{"auto compaction", `{"context_management":[{"type":"compaction"}],"input":[{"type":"configuration_update","reasoning":{"effort":"high"}}]}`, "automatic compaction", false},
		{"manual compaction", `{"input":[{"type":"compaction_trigger"},{"type":"configuration_update","reasoning":{"effort":"high"}},{"type":"message","role":"user","content":"continue"}]}`, "", false},
		{"compact endpoint", `{"input":[{"type":"configuration_update","reasoning":{"effort":"high"}}]}`, "standalone", true},
		{"ordinary compact", `{"input":[{"type":"message","role":"user","content":"compact"}]}`, "", true},
		{"custom model effort", `{"input":[{"type":"configuration_update","reasoning":{"effort":"custom-value"}}]}`, "", false},
		{"invalid JSON", `{"input":[{"type":"configuration_update"}`, "JSON", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateResponsesControls([]byte(tt.body), tt.compact)
			if tt.errorPart == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.errorPart) {
				t.Fatalf("err = %v, want %q", err, tt.errorPart)
			}
		})
	}
}

func TestResponsesControlsDoNotGuessRemoteWindowState(t *testing.T) {
	for _, tt := range []struct{ body, want string }{
		{`{"previous_response_id":"remote","reasoning":{"effort":"medium"}}`, ""},
		{`{"input":[{"type":"configuration_update","reasoning":{"effort":"max"}},{"type":"compaction","encrypted_content":"opaque"}]}`, ""},
		{`{"input":[{"type":"compaction","encrypted_content":"opaque"},{"type":"configuration_update","reasoning":{"effort":"high"}}]}`, "high"},
	} {
		got, err := InspectResponsesControls([]byte(tt.body))
		if err != nil || got.ConfigurationEffort != tt.want {
			t.Fatalf("got %+v, err %v, want effort %q", got, err, tt.want)
		}
	}
}

func TestResponsesControlsRejectLossyChatFallback(t *testing.T) {
	for _, body := range []string{
		`{"input":[{"type":"configuration_update","reasoning":{"effort":"max"}}]}`,
		`{"input":[{"type":"compaction_\u0074rigger"}]}`,
		`{"tools":[{"type":"function","async":true}]}`,
		`{"tools":[{"type":"custom","async":true}]}`,
	} {
		if RequireChatRepresentableResponses([]byte(body)) == nil {
			t.Fatalf("lossy conversion accepted: %s", body)
		}
	}
	for _, body := range []string{`{"input":"ordinary"}`, `{"tools":[{"type":"function","async":false}]}`} {
		if err := RequireChatRepresentableResponses([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
}
