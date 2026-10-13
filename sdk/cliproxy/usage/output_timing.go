package usage

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// OutputTiming describes an observed upstream streaming window. Its presence
// distinguishes a new, unmeasurable request from a legacy timing estimate.
// DurationMS is first body/payload to last observed body/payload, within one
// attempt. It is not model decode time or downstream delivery latency.
type OutputTiming struct {
	Version    uint32 `json:"version"`
	Streamed   bool   `json:"streamed"`
	DurationMS *int64 `json:"duration_ms,omitempty"`
}

func (t *OutputTiming) Valid() bool {
	return t != nil && t.Version == 1 && (t.DurationMS == nil || (t.Streamed && *t.DurationMS >= 0))
}

func (t *OutputTiming) Clone() *OutputTiming {
	if t == nil {
		return nil
	}
	copy := *t
	if t.DurationMS != nil {
		value := *t.DurationMS
		copy.DurationMS = &value
	}
	return &copy
}

func (t *OutputTiming) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	var value OutputTiming
	if len(fields["version"]) == 0 || len(fields["streamed"]) == 0 || bytes.Equal(bytes.TrimSpace(fields["streamed"]), []byte("null")) {
		return fmt.Errorf("output_timing requires version and streamed")
	}
	if err := json.Unmarshal(fields["version"], &value.Version); err != nil {
		return err
	}
	if err := json.Unmarshal(fields["streamed"], &value.Streamed); err != nil {
		return err
	}
	if raw, ok := fields["duration_ms"]; ok {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return fmt.Errorf("output duration must be an integer")
		}
		var duration int64
		if err := json.Unmarshal(raw, &duration); err != nil {
			return err
		}
		value.DurationMS = &duration
	}
	if !value.Valid() {
		return fmt.Errorf("invalid output_timing")
	}
	*t = value
	return nil
}
