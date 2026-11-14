package usage

func validOutputTimingDetail(d RequestDetail) bool {
	t := d.OutputTiming
	if t == nil {
		return true
	}
	if !t.Valid() {
		return false
	}
	if t.DurationMS == nil {
		return true
	}
	return d.timingFieldPresent(timingTTFBPresent) && d.TTFBMs >= 0 && d.LatencyMs >= d.TTFBMs && *t.DurationMS <= d.LatencyMs-d.TTFBMs
}

// outputDurationMS uses the measured window when available. A v1
// output_timing block without duration_ms (single-read streams, heartbeat-only
// preambles, or producers whose semantic observer never engaged) still has a
// recoverable TTFB estimate, so fall back to it instead of dropping the
// sample's OutputTPS denominator entirely. Only records without output_timing
// retain the historical estimate path unchanged.
func outputDurationMS(d RequestDetail) (float64, bool) {
	t := d.OutputTiming
	if t != nil && t.DurationMS != nil {
		if !validOutputTimingDetail(d) || *t.DurationMS <= 0 {
			return 0, false
		}
		return float64(*t.DurationMS), true
	}
	if !d.timingFieldPresent(timingTTFBPresent) || d.TTFBMs < 0 || d.LatencyMs <= d.TTFBMs {
		return 0, false
	}
	return float64(d.LatencyMs - d.TTFBMs), true
}
