package usage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"sort"
	"sync"
	"time"
)

const analyticsCacheBytes = 16 << 20
const analyticsResultBytes = 4 << 20
const analyticsCacheEntries = 8
const analyticsConcurrentQueries = 2
const analyticsCacheTTL = 4 * time.Minute

type analyticsCacheEntry struct {
	payload       json.RawMessage
	expires, used time.Time
}
type analyticsFlight struct {
	done    chan struct{}
	cancel  context.CancelFunc
	users   int
	payload json.RawMessage
	err     error
}
type analyticsCache struct {
	mu      sync.Mutex
	entries map[[32]byte]analyticsCacheEntry
	flights map[[32]byte]*analyticsFlight
	bytes   int
	active  int
	builds  uint64
}

func (s *RequestStatistics) QueryAnalytics(ctx context.Context, q QueryRequest) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	loc, err := validateQuery(&q)
	if err != nil {
		return nil, err
	}
	if len(q.Modules) > 0 || len(q.Rules) > 0 || q.Cursor != "" || q.HourFromMS != nil {
		return nil, ErrQueryInvalid
	}
	if q.Timezone == "" {
		q.Timezone = "UTC"
	}
	s.mu.RLock()
	boundary := queryBound{s.queryGeneration, s.querySequence}
	s.mu.RUnlock()
	if q.Bound != "" {
		var supplied queryBound
		if err := queryDecode(q.Bound, &supplied); err != nil {
			return nil, err
		}
		if supplied.Generation != boundary.Generation || supplied.Sequence > boundary.Sequence {
			return nil, ErrQueryExpired
		}
		boundary = supplied
	}
	q.Bound = queryEncode(boundary)
	q.Limit = 0
	q.Filter.Identities = append([]QueryIdentity(nil), q.Filter.Identities...)
	sort.Slice(q.Filter.Identities, func(i, j int) bool {
		a, b := q.Filter.Identities[i], q.Filter.Identities[j]
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		return a.AuthIndex < b.AuthIndex
	})
	ids := q.Filter.Identities[:0]
	for _, id := range q.Filter.Identities {
		if len(ids) == 0 || id != ids[len(ids)-1] {
			ids = append(ids, id)
		}
	}
	q.Filter.Identities = ids
	// Include the reference clock because it is echoed in the immutable result.
	encoded, err := json.Marshal(q)
	if err != nil {
		return nil, ErrQueryInvalid
	}
	key := sha256.Sum256(encoded)
	c := &s.analytics
	c.mu.Lock()
	now := time.Now()
	if c.entries == nil {
		c.entries = make(map[[32]byte]analyticsCacheEntry)
		c.flights = make(map[[32]byte]*analyticsFlight)
	}
	for k, v := range c.entries {
		if !now.Before(v.expires) {
			delete(c.entries, k)
			c.bytes -= len(v.payload)
		}
	}
	if v, ok := c.entries[key]; ok {
		v.used = now
		c.entries[key] = v
		c.mu.Unlock()
		return bytes.Clone(v.payload), nil
	}
	flight := c.flights[key]
	if flight == nil {
		if c.active >= analyticsConcurrentQueries {
			c.mu.Unlock()
			return nil, ErrAnalyticsBusy
		}
		work, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		flight = &analyticsFlight{done: make(chan struct{}), cancel: cancel}
		c.flights[key] = flight
		c.active++
		c.builds++
		go func(f *analyticsFlight) {
			payload, buildErr := s.buildAnalytics(work, q, loc)
			if buildErr == nil && len(payload) > analyticsResultBytes {
				payload = nil
				buildErr = ErrAnalyticsTooLarge
			}
			if buildErr == nil {
				buildErr = work.Err()
			}
			c.mu.Lock()
			defer c.mu.Unlock()
			defer cancel()
			c.active--
			f.payload, f.err = payload, buildErr
			if c.flights[key] == f {
				delete(c.flights, key)
				if buildErr == nil {
					for len(c.entries) >= analyticsCacheEntries || c.bytes+len(payload) > analyticsCacheBytes {
						var oldest [32]byte
						var used time.Time
						for k, v := range c.entries {
							if used.IsZero() || v.used.Before(used) {
								oldest = k
								used = v.used
							}
						}
						c.bytes -= len(c.entries[oldest].payload)
						delete(c.entries, oldest)
					}
					c.entries[key] = analyticsCacheEntry{payload: payload, expires: time.Now().Add(analyticsCacheTTL), used: time.Now()}
					c.bytes += len(payload)
				}
			}
			close(f.done)
		}(flight)
	}
	flight.users++
	c.mu.Unlock()
	select {
	case <-ctx.Done():
		c.mu.Lock()
		flight.users--
		if flight.users == 0 {
			flight.cancel()
			if c.flights[key] == flight {
				delete(c.flights, key)
			}
		}
		c.mu.Unlock()
		return nil, ctx.Err()
	case <-flight.done:
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return bytes.Clone(flight.payload), flight.err
	}
}
