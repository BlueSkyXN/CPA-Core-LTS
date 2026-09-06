package flowcontrol

import "fmt"

// Observation is independent of execution admission. Zero values disable
// realtime/resources; manual summary, details and explanation remain available.
type ObservationConfig struct {
	Realtime     bool  `yaml:"realtime" json:"realtime"`
	IntervalMS   int64 `yaml:"interval-ms,omitempty" json:"interval-ms"`
	MaxObservers int   `yaml:"max-observers,omitempty" json:"max-observers"`
	Resources    bool  `yaml:"resources" json:"resources"`
}

func (o ObservationConfig) Effective() ObservationConfig {
	if o.IntervalMS == 0 {
		o.IntervalMS = 2000
	}
	if o.MaxObservers == 0 {
		o.MaxObservers = 4
	}
	return o
}
func (o ObservationConfig) Validate() error {
	if o.IntervalMS < 500 || o.IntervalMS > 30000 || o.MaxObservers < 1 || o.MaxObservers > 16 {
		return fmt.Errorf("flow-control: observation interval must be 500..30000 ms; observers 1..16")
	}
	return nil
}
