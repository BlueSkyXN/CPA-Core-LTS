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

// outputDurationMS uses the measured window when available. Only records from
// older producers without output_timing retain the historical estimate.
func outputDurationMS(d RequestDetail) (float64, bool) {
	if t := d.OutputTiming; t != nil {
		if !validOutputTimingDetail(d) || t.DurationMS == nil || *t.DurationMS <= 0 {
			return 0, false
		}
		return float64(*t.DurationMS), true
	}
	if !d.timingFieldPresent(timingTTFBPresent) || d.TTFBMs < 0 || d.LatencyMs <= d.TTFBMs {
		return 0, false
	}
	return float64(d.LatencyMs - d.TTFBMs), true
}
