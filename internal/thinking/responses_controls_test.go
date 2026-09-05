package thinking

import "testing"

func TestTranslatedEffortUsesExplicitPositionedUpdate(t *testing.T) {
	body := []byte(`{"reasoning":{"effort":"medium"},"input":[{"type":"configuration_update","reasoning":{"effort":"high"}},{"role":"user","content":"continue"}]}`)
	for _, provider := range []string{"codex", "openai-response"} {
		if got := ExtractTranslatedReasoningEffort(body, provider); got != "high" {
			t.Fatalf("%s effort = %s", provider, got)
		}
	}
	if got := ExtractReasoningEffort(body, "openai-response", "gpt-6-astra(max)"); got != "max" {
		t.Fatalf("request intent precedence changed: %s", got)
	}
}
