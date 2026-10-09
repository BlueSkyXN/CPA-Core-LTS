package usage

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf16"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

const AnalyticsVersion = 1
const MaxAnalyticsRows = 250000
const maxAnalyticsGroups = 8192
const maxAnalyticsBuckets = 4096
const maxPaddedAnalyticsBuckets = 2161

var ErrAnalyticsTooLarge = errors.New("usage analytics capacity exceeded")
var ErrAnalyticsBusy = errors.New("usage analytics busy")

type AnalyticsPercentiles struct {
	P50         *float64 `json:"p50"`
	P90         *float64 `json:"p90"`
	P95         *float64 `json:"p95"`
	P99         *float64 `json:"p99"`
	Average     *float64 `json:"average"`
	SampleCount int      `json:"sampleCount"`
}
type AnalyticsTimings struct {
	Latency      AnalyticsPercentiles `json:"latency"`
	TTFT         AnalyticsPercentiles `json:"ttft"`
	TTFA         AnalyticsPercentiles `json:"ttfa"`
	FirstContent AnalyticsPercentiles `json:"firstContent"`
}
type AnalyticsHistogram struct {
	Counts      []int `json:"counts"`
	SampleCount int   `json:"sampleCount"`
}
type AnalyticsLatencySeries struct {
	Times        []int64    `json:"times"`
	SampleCounts []int      `json:"sampleCounts"`
	P50          []*float64 `json:"p50"`
	P95          []*float64 `json:"p95"`
	P99          []*float64 `json:"p99"`
	Average      []*float64 `json:"average"`
}

// AnalyticsThroughputPoint is one bucket of the throughput trend. Each series
// keeps the summed numerator and denominator of the same ratio QueryMetrics
// reports, so a bucket's rate is tokens per second once scaled by 1000.
// A zero denominator means the bucket has no measurable sample.
type AnalyticsThroughputPoint struct {
	TimestampMS          int64   `json:"timestampMs"`
	OutputTokens         float64 `json:"outputTokens"`
	DecodeDurationMS     float64 `json:"decodeDurationMs"`
	OutputSamples        int64   `json:"outputSamples"`
	AverageTokens        float64 `json:"averageTokens"`
	AverageDurationMS    float64 `json:"averageDurationMs"`
	AverageSamples       int64   `json:"averageSamples"`
	VisibleTokens        float64 `json:"visibleTokens"`
	VisibleDurationMS    float64 `json:"visibleDurationMs"`
	VisibleSamples       int64   `json:"visibleSamples"`
	ReasoningTokens      float64 `json:"reasoningTokens"`
	ReasoningDenominator float64 `json:"reasoningDenominator"`
	ReasoningSamples     int64   `json:"reasoningSamples"`
}
type AnalyticsCachePoint struct {
	TimestampMS      int64    `json:"timestampMs"`
	Requests         int64    `json:"requests"`
	CachedRequests   int64    `json:"cachedRequests"`
	InputTokens      int64    `json:"inputTokens"`
	CacheReadTokens  int64    `json:"cacheReadTokens"`
	CacheWriteTokens int64    `json:"cacheWriteTokens"`
	CacheRate        *float64 `json:"cacheRate"`
}
type AnalyticsCacheSummary struct {
	Requests             int64    `json:"requests"`
	CachedRequests       int64    `json:"cachedRequests"`
	InputTokens          int64    `json:"inputTokens"`
	CacheReadTokens      int64    `json:"cacheReadTokens"`
	CacheWriteTokens     int64    `json:"cacheWriteTokens"`
	CacheReadRate        *float64 `json:"cacheReadRate"`
	CacheHitRequestRatio *float64 `json:"cacheHitRequestRatio"`
}
type AnalyticsFailurePoint struct {
	TimestampMS int64    `json:"timestampMs"`
	Requests    int64    `json:"requests"`
	Failures    int64    `json:"failures"`
	FailureRate *float64 `json:"failureRate"`
}
type AnalyticsErrorStatus struct {
	Status *int    `json:"status"`
	Family string  `json:"family"`
	Count  int64   `json:"count"`
	Share  float64 `json:"share"`
}
type AnalyticsErrorReason struct {
	Reason string `json:"reason"`
	Count  int64  `json:"count"`
}
type AnalyticsErrors struct {
	TotalRequests  int64                  `json:"totalRequests"`
	FailedRequests int64                  `json:"failedRequests"`
	FailureRate    *float64               `json:"failureRate"`
	ByStatus       []AnalyticsErrorStatus `json:"byStatus"`
	TopReasons     []AnalyticsErrorReason `json:"topReasons"`
}
type AnalyticsGrain struct {
	Latency    AnalyticsLatencySeries     `json:"latency"`
	Cache      []AnalyticsCachePoint      `json:"cache"`
	Errors     []AnalyticsFailurePoint    `json:"errors"`
	Throughput []AnalyticsThroughputPoint `json:"throughput"`
}
type AnalyticsData struct {
	Timings   AnalyticsTimings      `json:"timings"`
	Histogram AnalyticsHistogram    `json:"histogram"`
	Cache     AnalyticsCacheSummary `json:"cache"`
	Errors    AnalyticsErrors       `json:"errors"`
	Hour      AnalyticsGrain        `json:"hour"`
	Day       AnalyticsGrain        `json:"day"`
}
type AnalyticsOptions struct {
	Models     []string        `json:"models"`
	Identities []QueryIdentity `json:"identities"`
}
type QueryAnalyticsResult struct {
	Version          int               `json:"version"`
	AnalyticsVersion int               `json:"analytics_version"`
	Bound            string            `json:"bound"`
	NowMS            int64             `json:"now_ms"`
	FromMS           *int64            `json:"from_ms"`
	ToMS             *int64            `json:"to_ms"`
	Timezone         string            `json:"timezone"`
	Total            int64             `json:"total"`
	Analyzed         int64             `json:"analyzed"`
	Data             AnalyticsData     `json:"data"`
	Options          *AnalyticsOptions `json:"options,omitempty"`
}
type analyticsBucket struct {
	cache      AnalyticsCachePoint
	errors     AnalyticsFailurePoint
	throughput AnalyticsThroughputPoint
	samples    []int64
}
type analyticsSample struct{ latency, ttft, ttfa int64 }

var analyticsEdges = []int64{0, 100, 250, 500, 1000, 2500, 5000, 10000}

// analyticsAddThroughput accumulates one request into a bucket using the same
// eligibility rules as QueryMetrics: successful requests with output tokens and
// a positive latency contribute, the decode-window series additionally needs a
// measured first byte that arrives before completion, and the visible series
// needs reasoning tokens that do not exceed the reported output.
func analyticsAddThroughput(p *AnalyticsThroughputPoint, d RequestDetail) {
	t := d.Tokens
	if t.OutputTokens <= 0 || d.LatencyMs <= 0 {
		return
	}
	output, latency := float64(t.OutputTokens), float64(d.LatencyMs)
	p.AverageTokens += output
	p.AverageDurationMS += latency
	p.AverageSamples++
	if d.timingFieldPresent(timingTTFBPresent) && d.TTFBMs >= 0 && d.LatencyMs > d.TTFBMs {
		p.OutputTokens += output
		p.DecodeDurationMS += latency - float64(d.TTFBMs)
		p.OutputSamples++
	}
	if t.ReasoningTokens <= t.OutputTokens {
		p.VisibleTokens += output - float64(t.ReasoningTokens)
		p.VisibleDurationMS += latency
		p.VisibleSamples++
		p.ReasoningTokens += float64(t.ReasoningTokens)
		p.ReasoningDenominator += output
		p.ReasoningSamples++
	}
}

func analyticsRatio(n, d int64) *float64 {
	if d == 0 {
		return nil
	}
	v := float64(n) / float64(d)
	return &v
}
func analyticsPercentiles(values []int64) AnalyticsPercentiles {
	r := AnalyticsPercentiles{SampleCount: len(values)}
	if len(values) == 0 {
		return r
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	rank := func(p int) *float64 { v := float64(values[(p*len(values)+99)/100-1]); return &v }
	var sum float64
	for _, v := range values {
		sum += float64(v)
	}
	average := sum / float64(len(values))
	r.P50 = rank(50)
	r.P90 = rank(90)
	r.P95 = rank(95)
	r.P99 = rank(99)
	r.Average = &average
	return r
}

// JavaScript Date chooses the earlier offset in overlaps and advances through gaps.
func analyticsWall(y int, m time.Month, d, h, min int, loc *time.Location) time.Time {
	wall := time.Date(y, m, d, h, min, 0, 0, time.UTC)
	offsets := map[int]bool{}
	for delta := -36; delta <= 36; delta += 6 {
		_, o := wall.Add(time.Duration(delta) * time.Hour).In(loc).Zone()
		offsets[o] = true
	}
	var exact, forward time.Time
	best := int64(math.MaxInt64)
	for offset := range offsets {
		candidate := wall.Add(-time.Duration(offset) * time.Second).In(loc)
		cy, cm, cd := candidate.Date()
		ch, cmin, _ := candidate.Clock()
		normalized := time.Date(cy, cm, cd, ch, cmin, 0, 0, time.UTC)
		diff := normalized.UnixMilli() - wall.UnixMilli()
		if diff == 0 && (exact.IsZero() || candidate.Before(exact)) {
			exact = candidate
		}
		if diff > 0 && (diff < best || diff == best && candidate.Before(forward)) {
			best = diff
			forward = candidate
		}
	}
	if !exact.IsZero() {
		return exact
	}
	if !forward.IsZero() {
		return forward
	}
	return time.Date(y, m, d, h, min, 0, 0, loc)
}
func analyticsFloor(ms int64, day bool, loc *time.Location) int64 {
	t := time.UnixMilli(ms).In(loc)
	y, m, d := t.Date()
	h := t.Hour()
	if day {
		h = 0
	}
	return analyticsWall(y, m, d, h, 0, loc).UnixMilli()
}
func analyticsNext(ms int64, day bool, loc *time.Location) int64 {
	t := time.UnixMilli(ms).In(loc)
	y, m, d := t.Date()
	h, min, _ := t.Clock()
	if day {
		d++
	} else {
		h++
	}
	return analyticsFloor(analyticsWall(y, m, d, h, min, loc).UnixMilli(), day, loc)
}
func analyticsTimes(buckets map[int64]*analyticsBucket, first, last int64, q QueryRequest, day bool, loc *time.Location) []int64 {
	if q.FromMS != nil {
		first = *q.FromMS
	}
	if q.ToMS != nil {
		last = *q.ToMS
	}
	if first == math.MaxInt64 || last == 0 || first > last {
		return []int64{}
	}
	start, end := analyticsFloor(first, day, loc), analyticsFloor(last, day, loc)
	times := []int64{}
	for cursor := start; cursor <= end && len(times) <= maxPaddedAnalyticsBuckets; {
		times = append(times, cursor)
		next := analyticsNext(cursor, day, loc)
		if next <= cursor {
			break
		}
		cursor = next
	}
	if len(times) > maxPaddedAnalyticsBuckets {
		observed := map[int64]bool{start: true, end: true}
		for key := range buckets {
			observed[key] = true
		}
		times = make([]int64, 0, len(observed))
		for key := range observed {
			times = append(times, key)
		}
		sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	}
	return times
}
func finishAnalyticsGrain(ctx context.Context, buckets map[int64]*analyticsBucket, times []int64) (AnalyticsGrain, error) {
	r := AnalyticsGrain{Latency: AnalyticsLatencySeries{Times: times, SampleCounts: []int{}, P50: []*float64{}, P95: []*float64{}, P99: []*float64{}, Average: []*float64{}}, Cache: []AnalyticsCachePoint{}, Errors: []AnalyticsFailurePoint{}, Throughput: []AnalyticsThroughputPoint{}}
	for _, t := range times {
		if err := ctx.Err(); err != nil {
			return r, err
		}
		b := buckets[t]
		if b == nil {
			b = &analyticsBucket{}
		}
		p := analyticsPercentiles(b.samples)
		r.Latency.SampleCounts = append(r.Latency.SampleCounts, p.SampleCount)
		r.Latency.P50 = append(r.Latency.P50, p.P50)
		r.Latency.P95 = append(r.Latency.P95, p.P95)
		r.Latency.P99 = append(r.Latency.P99, p.P99)
		r.Latency.Average = append(r.Latency.Average, p.Average)
		b.cache.TimestampMS = t
		b.cache.CacheRate = analyticsRatio(b.cache.CacheReadTokens, b.cache.InputTokens)
		r.Cache = append(r.Cache, b.cache)
		b.errors.TimestampMS = t
		b.errors.FailureRate = analyticsRatio(b.errors.Failures, b.errors.Requests)
		r.Errors = append(r.Errors, b.errors)
		b.throughput.TimestampMS = t
		r.Throughput = append(r.Throughput, b.throughput)
	}
	return r, nil
}
func analyticsReason(s string) string {
	units := utf16.Encode([]rune(strings.TrimSpace(s)))
	if len(units) > 200 {
		units = units[:200]
	}
	return string(utf16.Decode(units))
}
func (s *RequestStatistics) buildAnalytics(ctx context.Context, q QueryRequest, loc *time.Location) (json.RawMessage, error) {
	r := QueryAnalyticsResult{Version: QueryVersion, AnalyticsVersion: AnalyticsVersion, Bound: q.Bound, NowMS: q.NowMS, FromMS: q.FromMS, ToMS: q.ToMS, Timezone: q.Timezone}
	r.Data.Histogram.Counts = make([]int, len(analyticsEdges))
	models := map[string]bool{}
	identities := map[QueryIdentity]bool{}
	statuses := map[int]int64{}
	reasons := map[string]int64{}
	hours, days := map[int64]*analyticsBucket{}, map[int64]*analyticsBucket{}
	// Minute keys keep half-hour timezone transitions distinct.
	floors := map[int64][2]int64{}
	samples := []analyticsSample{}
	first, last := int64(math.MaxInt64), int64(0)
	selected := map[QueryIdentity]bool{}
	for _, id := range q.Filter.Identities {
		selected[id] = true
	}
	filter := q.Filter
	filter.Identities = nil
	err := s.walkQuery(ctx, &q, func(api, model string, d RequestDetail) error {
		if !queryTimeMatches(q, d) {
			return nil
		}
		if q.IncludeOptions {
			models[model] = true
			identities[QueryIdentity{d.Source, d.AuthIndex}] = true
			if len(models)+len(identities) > maxAnalyticsGroups {
				return ErrAnalyticsTooLarge
			}
		}
		if len(selected) > 0 && !selected[QueryIdentity{d.Source, d.AuthIndex}] || !queryFilterMatches(filter, api, model, d) {
			return nil
		}
		r.Total++
		if r.Total > MaxAnalyticsRows {
			return ErrAnalyticsTooLarge
		}
		ms := d.Timestamp.UnixMilli()
		if ms <= 0 || ms > 8640000000000000 {
			return nil
		}
		r.Analyzed++
		first = min(first, ms)
		last = max(last, ms)
		utcMinute := ms / 60000
		floor, ok := floors[utcMinute]
		if !ok {
			floor = [2]int64{analyticsFloor(ms, false, loc), analyticsFloor(ms, true, loc)}
			if len(floors) < maxAnalyticsGroups {
				floors[utcMinute] = floor
			}
		}
		for i, buckets := range []map[int64]*analyticsBucket{hours, days} {
			b := buckets[floor[i]]
			if b == nil {
				if len(buckets) >= maxAnalyticsBuckets {
					return ErrAnalyticsTooLarge
				}
				b = &analyticsBucket{}
				buckets[floor[i]] = b
			}
			b.cache.Requests++
			b.cache.InputTokens += d.Tokens.InputTokens
			b.cache.CacheReadTokens += d.Tokens.CacheReadTokens
			b.cache.CacheWriteTokens += d.Tokens.CacheCreationTokens
			if d.Tokens.CacheReadTokens > 0 {
				b.cache.CachedRequests++
			}
			b.errors.Requests++
			if d.Failed {
				b.errors.Failures++
			} else if d.LatencyMs >= 0 {
				b.samples = append(b.samples, d.LatencyMs)
			}
			analyticsAddThroughput(&b.throughput, d)
		}
		c := &r.Data.Cache
		c.Requests++
		c.InputTokens += d.Tokens.InputTokens
		c.CacheReadTokens += d.Tokens.CacheReadTokens
		c.CacheWriteTokens += d.Tokens.CacheCreationTokens
		if d.Tokens.CacheReadTokens > 0 {
			c.CachedRequests++
		}
		e := &r.Data.Errors
		e.TotalRequests++
		if d.Failed {
			e.FailedRequests++
			status := d.FailureStatus
			if status < 100 || status > 599 {
				status = 0
			}
			statuses[status]++
			reason := analyticsReason(d.FailureReason)
			if reason != "" {
				reasons[reason]++
				if len(reasons) > maxAnalyticsGroups {
					return ErrAnalyticsTooLarge
				}
			}
			return nil
		}
		v := analyticsSample{latency: d.LatencyMs, ttft: -1, ttfa: -1}
		if d.TimingVersion == 1 && d.timingFieldPresent(timingTTFBPresent) {
			if d.timingFieldPresent(timingTTFTPresent) && d.TTFTMs >= d.TTFBMs && d.TTFTMs <= d.LatencyMs {
				v.ttft = d.TTFTMs
			}
			if d.timingFieldPresent(timingTTFAPresent) && d.TTFAMs >= d.TTFBMs && d.TTFAMs <= d.LatencyMs {
				v.ttfa = d.TTFAMs
			}
		}
		samples = append(samples, v)
		if v.latency >= 0 {
			for i := len(analyticsEdges) - 1; i >= 0; i-- {
				if v.latency >= analyticsEdges[i] {
					r.Data.Histogram.Counts[i]++
					r.Data.Histogram.SampleCount++
					break
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	c := &r.Data.Cache
	c.CacheReadRate = analyticsRatio(c.CacheReadTokens, c.InputTokens)
	c.CacheHitRequestRatio = analyticsRatio(c.CachedRequests, c.Requests)
	e := &r.Data.Errors
	e.FailureRate = analyticsRatio(e.FailedRequests, e.TotalRequests)
	e.ByStatus = []AnalyticsErrorStatus{}
	e.TopReasons = []AnalyticsErrorReason{}
	for status, count := range statuses {
		group := AnalyticsErrorStatus{Count: count, Family: "other", Share: float64(count) / float64(e.FailedRequests)}
		if status != 0 {
			v := status
			group.Status = &v
		}
		if status == 429 {
			group.Family = "429"
		} else if status >= 400 && status < 500 {
			group.Family = "4xx"
		} else if status >= 500 {
			group.Family = "5xx"
		}
		e.ByStatus = append(e.ByStatus, group)
	}
	sort.Slice(e.ByStatus, func(i, j int) bool {
		a, b := e.ByStatus[i], e.ByStatus[j]
		if a.Status == nil {
			return false
		}
		if b.Status == nil {
			return true
		}
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		return *a.Status < *b.Status
	})
	for reason, count := range reasons {
		e.TopReasons = append(e.TopReasons, AnalyticsErrorReason{reason, count})
	}
	order := collate.New(language.English)
	sort.Slice(e.TopReasons, func(i, j int) bool {
		a, b := e.TopReasons[i], e.TopReasons[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		return order.CompareString(a.Reason, b.Reason) < 0
	})
	if len(e.TopReasons) > 5 {
		e.TopReasons = e.TopReasons[:5]
	}
	values := make([]int64, 0, len(samples))
	summaries := []*AnalyticsPercentiles{&r.Data.Timings.Latency, &r.Data.Timings.TTFT, &r.Data.Timings.TTFA, &r.Data.Timings.FirstContent}
	for field, target := range summaries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		values = values[:0]
		for _, v := range samples {
			n := v.latency
			switch field {
			case 1:
				n = v.ttft
			case 2:
				n = v.ttfa
			case 3:
				n = v.ttft
				if n < 0 || v.ttfa >= 0 && v.ttfa < n {
					n = v.ttfa
				}
			}
			if n >= 0 {
				values = append(values, n)
			}
		}
		*target = analyticsPercentiles(values)
	}
	r.Data.Hour, err = finishAnalyticsGrain(ctx, hours, analyticsTimes(hours, first, last, q, false, loc))
	if err != nil {
		return nil, err
	}
	r.Data.Day, err = finishAnalyticsGrain(ctx, days, analyticsTimes(days, first, last, q, true, loc))
	if err != nil {
		return nil, err
	}
	if q.IncludeOptions {
		r.Options = &AnalyticsOptions{Models: []string{}, Identities: []QueryIdentity{}}
		for model := range models {
			r.Options.Models = append(r.Options.Models, model)
		}
		sort.Strings(r.Options.Models)
		for id := range identities {
			r.Options.Identities = append(r.Options.Identities, id)
		}
		sort.Slice(r.Options.Identities, func(i, j int) bool {
			a, b := r.Options.Identities[i], r.Options.Identities[j]
			if a.Source != b.Source {
				return a.Source < b.Source
			}
			return a.AuthIndex < b.AuthIndex
		})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return json.Marshal(r)
}
