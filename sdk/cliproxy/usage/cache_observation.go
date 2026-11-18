package usage

// CacheObservation keeps field presence separate from the legacy scalar totals.
// nil means that an older producer did not record this evidence.
type CacheObservation struct {
	Version uint32                `json:"version"`
	Read    CacheTokenObservation `json:"read"`
	Write   CacheTokenObservation `json:"write"`
}

type CacheTokenObservation struct {
	// State is present, absent, or invalid. An absent CacheObservation is unknown.
	State string `json:"state"`
	Value *int64 `json:"value,omitempty"`
	// Source identifies the original upstream field, never a credential or body.
	Source string `json:"source,omitempty"`
}

func (c *CacheObservation) Clone() *CacheObservation {
	if c == nil {
		return nil
	}
	out := *c
	out.Read.Value = cloneInt64Ptr(c.Read.Value)
	out.Write.Value = cloneInt64Ptr(c.Write.Value)
	return &out
}

// Valid rejects contradictory imported evidence without changing legacy totals.
func (c *CacheObservation) Valid() bool {
	if c == nil {
		return true
	}
	if c.Version != 1 {
		return false
	}
	for _, field := range []CacheTokenObservation{c.Read, c.Write} {
		switch field.State {
		case "present":
			if field.Value == nil || *field.Value < 0 {
				return false
			}
		case "absent", "invalid":
			if field.Value != nil {
				return false
			}
		default:
			return false
		}
		if len(field.Source) > 96 {
			return false
		}
	}
	return true
}
