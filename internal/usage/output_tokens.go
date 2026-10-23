package usage

import coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"

func validTokenBreakdownDetail(d RequestDetail) bool {
	b := d.TokenBreakdown
	if b == nil {
		return true
	}
	if !b.Valid() || b.TotalTokens != d.Tokens.TotalTokens {
		return false
	}
	if b.Quality != coreusage.TokenAccountingQualityComplete {
		return true
	}
	// Raw output may include reasoning (OpenAI) or exclude it (Gemini).
	return b.Input.TotalTokens == d.Tokens.InputTokens &&
		b.Input.CacheReadTokens == d.Tokens.CacheReadTokens &&
		b.Input.CacheWriteTokens == d.Tokens.CacheCreationTokens &&
		b.Output.ReasoningTokens == d.Tokens.ReasoningTokens &&
		(d.Tokens.OutputTokens == b.Output.TotalTokens || d.Tokens.OutputTokens == b.Output.NonReasoningTokens)
}

// performanceOutputTokens normalizes rate numerators without changing the
// original counters, aggregate token totals, pricing inputs, or dedup identity.
func performanceOutputTokens(d RequestDetail) (output, reasoning int64, known bool) {
	if b := d.TokenBreakdown; b != nil {
		if !validTokenBreakdownDetail(d) || b.Quality != coreusage.TokenAccountingQualityComplete {
			return 0, 0, false
		}
		return b.Output.TotalTokens, b.Output.ReasoningTokens, true
	}
	// Historical records have no recoverable provider accounting semantics.
	return d.Tokens.OutputTokens, d.Tokens.ReasoningTokens, true
}
