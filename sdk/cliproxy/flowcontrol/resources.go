package flowcontrol

import (
	"runtime"
	"runtime/metrics"
	"time"
)

// These are process/runtime counters, NOT memory attributed to flow control.
// No directory walking, log history, disk writes or background polling is used.
type ResourceSample struct {
	SampledAt           time.Time `json:"sampled-at"`
	HeapObjects         uint64    `json:"heap-object-bytes"`
	GoManaged           uint64    `json:"go-managed-bytes"`
	Goroutines          int       `json:"goroutines"`
	FilesystemFree      *uint64   `json:"filesystem-free-bytes"`
	FilesystemSampledAt time.Time `json:"filesystem-sampled-at"`
	FilesystemScope     string    `json:"filesystem-scope"`
}

func (e *Engine) sampleResources() ResourceSample {
	e.resourceMu.Lock()
	defer e.resourceMu.Unlock()
	now := time.Now()
	if now.Before(e.resourceExpires) {
		return e.resources
	}
	samples := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}, {Name: "/memory/classes/total:bytes"}, {Name: "/memory/classes/heap/released:bytes"}}
	metrics.Read(samples)
	value := func(i int) uint64 {
		if samples[i].Value.Kind() == metrics.KindUint64 {
			return samples[i].Value.Uint64()
		}
		return 0
	}
	e.resources.SampledAt = now
	e.resources.HeapObjects = value(0)
	e.resources.GoManaged = value(1) - value(2)
	e.resources.Goroutines = runtime.NumGoroutine()
	if !now.Before(e.diskExpires) {
		e.resources.FilesystemFree = filesystemFree()
		e.resources.FilesystemSampledAt = now
		e.resources.FilesystemScope = "process-working-directory-filesystem"
		e.diskExpires = now.Add(30 * time.Second)
	}
	e.resourceExpires = now.Add(5 * time.Second)
	return e.resources
}
