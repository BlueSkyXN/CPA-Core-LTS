package usage

import coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"

const (
	cachePositive = "positive"
	cacheZero     = "zero"
	cacheUnknown  = "unknown"
)

type cacheSample struct {
	readState, writeState string
	input, read           int64
}

func observedCache(d RequestDetail) cacheSample {
	sample := cacheSample{readState: cacheUnknown, writeState: cacheUnknown}
	if d.CacheObservation == nil || d.CacheObservation.Version != 1 {
		return sample
	}
	readState, read := cacheTokenState(d.CacheObservation.Read)
	sample.readState = readState
	sample.writeState, _ = cacheTokenState(d.CacheObservation.Write)
	if input := d.Tokens.InputTokens; readState != cacheUnknown && input > 0 && read <= input {
		sample.input = input
		sample.read = read
	}
	return sample
}

func cacheTokenState(obs coreusage.CacheTokenObservation) (string, int64) {
	if obs.State != "present" || obs.Value == nil || *obs.Value < 0 {
		return cacheUnknown, 0
	}
	if *obs.Value == 0 {
		return cacheZero, 0
	}
	return cachePositive, *obs.Value
}

type CacheStateCounts struct {
	Positive int64 `json:"positive"`
	Zero     int64 `json:"zero"`
	Unknown  int64 `json:"unknown"`
}

func (c *CacheStateCounts) add(state string) {
	switch state {
	case cachePositive:
		c.Positive++
	case cacheZero:
		c.Zero++
	default:
		c.Unknown++
	}
}

// AnalyticsCacheObservation 只使用带上游读数的同一批有效样本计算分子和分母。
type AnalyticsCacheObservation struct {
	Read        CacheStateCounts `json:"read"`
	Write       CacheStateCounts `json:"write"`
	InputTokens int64            `json:"inputTokens"`
	ReadTokens  int64            `json:"readTokens"`
	Rate        *float64         `json:"rate"`
}

func (a *AnalyticsCacheObservation) add(sample cacheSample) {
	a.Read.add(sample.readState)
	a.Write.add(sample.writeState)
	a.InputTokens += sample.input
	a.ReadTokens += sample.read
}

func (a *AnalyticsCacheObservation) finish() {
	a.Rate = analyticsRatio(a.ReadTokens, a.InputTokens)
}
