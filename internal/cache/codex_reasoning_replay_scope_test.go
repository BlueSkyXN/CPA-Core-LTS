package cache

import "testing"

func TestResolveReasoningReplaySessionKeySupportsOpenAIResponseWithoutWideningCodexGate(t *testing.T) {
	input := CodexReasoningReplaySessionInput{
		SourceFormat:      "openai-response",
		TranslatedPayload: []byte(`{"prompt_cache_key":"shared-session"}`),
	}

	if got := ResolveReasoningReplaySessionKey(input); got != "prompt-cache:shared-session" {
		t.Fatalf("ResolveReasoningReplaySessionKey() = %q, want %q", got, "prompt-cache:shared-session")
	}
	if got := ResolveCodexReasoningReplaySessionKey(input); got != "" {
		t.Fatalf("ResolveCodexReasoningReplaySessionKey() = %q, want empty for non-Claude source", got)
	}
}

func TestResolveReasoningReplaySessionKeyUsesExecutionSessionAcrossSources(t *testing.T) {
	input := CodexReasoningReplaySessionInput{
		SourceFormat:           "openai-response",
		OptionExecutionSession: "trusted-session",
	}

	if got := ResolveReasoningReplaySessionKey(input); got != "execution:trusted-session" {
		t.Fatalf("ResolveReasoningReplaySessionKey() = %q, want %q", got, "execution:trusted-session")
	}
}
