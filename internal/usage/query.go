package usage

import (
	"container/heap"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
)

const QueryVersion = 1
const MaxQueryPageSize = 200

var ErrQueryInvalid = errors.New("invalid usage query")
var ErrQueryExpired = errors.New("usage query boundary expired")

func newQueryGeneration() string { return rand.Text() }

type QueryIdentity struct {
	Source    string `json:"source"`
	AuthIndex string `json:"auth_index"`
}

type QueryFilter struct {
	Model      *string         `json:"model,omitempty"`
	API        *string         `json:"api,omitempty"`
	AuthIndex  *string         `json:"auth_index,omitempty"`
	Identities []QueryIdentity `json:"identities,omitempty"`
	Tier       string          `json:"tier,omitempty"`
	Effort     *string         `json:"effort,omitempty"`
	Failed     *bool           `json:"failed,omitempty"`
	Cached     *bool           `json:"cached,omitempty"`
	Metric     string          `json:"metric,omitempty"`
	Minimum    *float64        `json:"minimum,omitempty"`
	Maximum    *float64        `json:"maximum,omitempty"`
}

// 规则由 Panel 当前价格配置生成；Core 只分类，不存储单价或历史金额。
type QueryPriceRule struct {
	LongThreshold *int64 `json:"long_threshold,omitempty"`
}

type QueryRequest struct {
	Bound          string                    `json:"bound,omitempty"`
	FromMS         *int64                    `json:"from_ms,omitempty"`
	ToMS           *int64                    `json:"to_ms,omitempty"`
	NowMS          int64                     `json:"now_ms,omitempty"`
	Timezone       string                    `json:"timezone,omitempty"`
	Modules        []string                  `json:"modules,omitempty"`
	Filter         QueryFilter               `json:"filter,omitempty"`
	Limit          int                       `json:"limit,omitempty"`
	Cursor         string                    `json:"cursor,omitempty"`
	IncludeOptions bool                      `json:"include_options,omitempty"`
	Rules          map[string]QueryPriceRule `json:"rules,omitempty"`
	HourFromMS     *int64                    `json:"hour_from_ms,omitempty"`
}

type queryBound struct {
	Generation string `json:"g"`
	Sequence   uint64 `json:"s"`
}
type queryCursor struct {
	Bound     string `json:"b"`
	Filter    string `json:"f"`
	Timestamp int64  `json:"t"`
	Sequence  uint64 `json:"s"`
}

type QueryCapabilities struct {
	Version     int      `json:"version"`
	Bound       string   `json:"bound"`
	NowMS       int64    `json:"now_ms"`
	Models      []string `json:"models"`
	MaxPageSize int      `json:"max_page_size"`
}

func queryEncode(value any) string {
	b, _ := json.Marshal(value)
	return base64.RawURLEncoding.EncodeToString(b)
}
func queryDecode(value string, target any) error {
	if len(value) > 4096 {
		return ErrQueryInvalid
	}
	b, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return ErrQueryInvalid
	}
	if json.Unmarshal(b, target) != nil {
		return ErrQueryInvalid
	}
	return nil
}

func (s *RequestStatistics) QueryCapabilities() QueryCapabilities {
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := map[string]bool{}
	for _, api := range s.apis {
		for name := range api.Models {
			names[name] = true
		}
	}
	models := make([]string, 0, len(names))
	for name := range names {
		models = append(models, name)
	}
	sort.Strings(models)
	return QueryCapabilities{QueryVersion, queryEncode(queryBound{s.queryGeneration, s.querySequence}), time.Now().UnixMilli(), models, MaxQueryPageSize}
}

type QueryPriceGroup struct {
	Model      string `json:"model"`
	Tier       string `json:"tier"`
	Evidence   string `json:"evidence"`
	Band       string `json:"band"`
	Requests   int64  `json:"requests"`
	Tokens     int64  `json:"tokens"`
	Prompt     int64  `json:"prompt"`
	CacheRead  int64  `json:"cache_read"`
	CacheWrite int64  `json:"cache_write"`
	Output     int64  `json:"output"`
}
type queryPriceKey struct{ model, tier, evidence, band string }

type QueryRatio struct {
	Numerator   float64 `json:"numerator"`
	Denominator float64 `json:"denominator"`
	Samples     int64   `json:"samples"`
}

type QueryMetrics struct {
	Requests       int64             `json:"requests"`
	Success        int64             `json:"success"`
	Failure        int64             `json:"failure"`
	Tokens         int64             `json:"tokens"`
	Input          int64             `json:"input"`
	Output         int64             `json:"output"`
	Reasoning      int64             `json:"reasoning"`
	CacheRead      int64             `json:"cache_read"`
	CacheWrite     int64             `json:"cache_write"`
	Prompt         int64             `json:"prompt"`
	LatencyMS      float64           `json:"latency_ms"`
	LatencySamples int64             `json:"latency_samples"`
	TTFBSamples    int64             `json:"ttfb_samples"`
	OutputTPS      QueryRatio        `json:"output_tps"`
	AverageTPS     QueryRatio        `json:"average_tps"`
	VisibleTPS     QueryRatio        `json:"visible_tps"`
	ReasoningRatio QueryRatio        `json:"reasoning_ratio"`
	Prices         []QueryPriceGroup `json:"prices,omitempty"`
	prices         map[queryPriceKey]*QueryPriceGroup
}

func addQueryRatio(r *QueryRatio, numerator, denominator float64) {
	r.Numerator += numerator
	r.Denominator += denominator
	r.Samples++
}

func (m *QueryMetrics) add(d RequestDetail, model string, rule QueryPriceRule, prices bool) {
	t := d.Tokens
	m.Requests++
	if d.Failed {
		m.Failure++
	} else {
		m.Success++
	}
	m.Tokens += t.TotalTokens
	m.Input += t.InputTokens
	m.Output += t.OutputTokens
	m.Reasoning += t.ReasoningTokens
	m.CacheRead += t.CacheReadTokens
	m.CacheWrite += t.CacheCreationTokens
	prompt := max(t.InputTokens-t.CacheReadTokens-t.CacheCreationTokens, 0)
	m.Prompt += prompt
	if d.LatencyMs >= 0 {
		m.LatencyMS += float64(d.LatencyMs)
		m.LatencySamples++
	}
	hasTTFB := d.TTFBMs >= 0 && (d.TTFBMs != 0 || d.timingFieldPresent(timingTTFBPresent))
	if hasTTFB {
		m.TTFBSamples++
	}
	if t.OutputTokens > 0 && d.LatencyMs > 0 {
		output, latency := float64(t.OutputTokens), float64(d.LatencyMs)
		addQueryRatio(&m.AverageTPS, output, latency)
		if hasTTFB && d.LatencyMs > d.TTFBMs {
			addQueryRatio(&m.OutputTPS, output, latency-float64(d.TTFBMs))
		}
		if t.ReasoningTokens <= t.OutputTokens {
			addQueryRatio(&m.VisibleTPS, output-float64(t.ReasoningTokens), latency)
			addQueryRatio(&m.ReasoningRatio, float64(t.ReasoningTokens), output)
		}
	}
	if !prices {
		return
	}
	tier, evidence := queryServiceTier(d)
	band := "short"
	if rule.LongThreshold != nil && t.InputTokens >= *rule.LongThreshold {
		band = "long"
	}
	key := queryPriceKey{model, tier, evidence, band}
	if m.prices == nil {
		m.prices = map[queryPriceKey]*QueryPriceGroup{}
	}
	p := m.prices[key]
	if p == nil {
		p = &QueryPriceGroup{Model: model, Tier: tier, Evidence: evidence, Band: band}
		m.prices[key] = p
	}
	p.Requests++
	p.Tokens += t.TotalTokens
	p.Prompt += prompt
	p.CacheRead += t.CacheReadTokens
	p.CacheWrite += t.CacheCreationTokens
	p.Output += t.OutputTokens
}

func (m *QueryMetrics) finish() {
	for _, p := range m.prices {
		m.Prices = append(m.Prices, *p)
	}
	sort.Slice(m.Prices, func(i, j int) bool {
		a, b := m.Prices[i], m.Prices[j]
		if a.Model != b.Model {
			return a.Model < b.Model
		}
		if a.Tier != b.Tier {
			return a.Tier < b.Tier
		}
		if a.Evidence != b.Evidence {
			return a.Evidence < b.Evidence
		}
		return a.Band < b.Band
	})
	m.prices = nil
}

// 与 Panel serviceTier.ts 相同：未知的高优先级证据阻止向低优先级 Fast 回退。
func queryServiceTier(d RequestDetail) (string, string) {
	classify := func(s string) string {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "priority", "fast":
			return "fast"
		case "standard", "default":
			return "std"
		}
		return ""
	}
	effective, response, outbound, request := strings.TrimSpace(d.EffectiveServiceTier), strings.TrimSpace(d.ResponseServiceTier), strings.TrimSpace(d.OutboundServiceTier), strings.TrimSpace(d.RequestServiceTier)
	if request == "" {
		request = strings.TrimSpace(d.ServiceTier)
	}
	if tier := classify(effective); tier != "" {
		if classify(response) == tier {
			return tier, "response"
		}
		if response == "" && classify(outbound) == tier {
			return tier, "outbound"
		}
		return tier, "effective"
	}
	if effective != "" {
		return "std", "assumed"
	}
	for _, item := range [][2]string{{response, "response"}, {outbound, "outbound"}, {request, "request"}} {
		if tier := classify(item[0]); tier != "" {
			return tier, item[1]
		}
		if item[0] != "" {
			return "std", "assumed"
		}
	}
	return "std", "assumed"
}

type QueryGroup struct {
	API       string       `json:"api,omitempty"`
	Model     string       `json:"model,omitempty"`
	Source    string       `json:"source,omitempty"`
	AuthIndex string       `json:"auth_index,omitempty"`
	StartMS   int64        `json:"start_ms,omitempty"`
	Metrics   QueryMetrics `json:"metrics"`
}
type QuerySummary struct {
	Version int                     `json:"version"`
	Bound   string                  `json:"bound"`
	NowMS   int64                   `json:"now_ms"`
	Totals  QueryMetrics            `json:"totals"`
	Groups  map[string][]QueryGroup `json:"groups"`
}
type queryGroupKey struct {
	module, api, model, source, auth string
	start                            int64
}

func validateQuery(q *QueryRequest) (*time.Location, error) {
	if q.NowMS == 0 {
		q.NowMS = time.Now().UnixMilli()
	}
	if q.NowMS < 0 || q.NowMS > 8640000000000000 || (q.FromMS != nil && *q.FromMS < 0) || (q.ToMS != nil && *q.ToMS > 8640000000000000) || (q.FromMS != nil && q.ToMS != nil && *q.FromMS > *q.ToMS) {
		return nil, ErrQueryInvalid
	}
	if q.Limit == 0 {
		q.Limit = 100
	}
	if q.Limit < 1 || q.Limit > MaxQueryPageSize || len(q.Rules) > 4096 || len(q.Filter.Identities) > 4096 {
		return nil, ErrQueryInvalid
	}
	if q.Filter.Minimum != nil && *q.Filter.Minimum < 0 || q.Filter.Maximum != nil && *q.Filter.Maximum < 0 || q.Filter.Minimum != nil && q.Filter.Maximum != nil && *q.Filter.Minimum > *q.Filter.Maximum {
		return nil, ErrQueryInvalid
	}
	switch q.Filter.Metric {
	case "", "totalInputTokens", "nonCacheReadInputTokens", "totalOutputTokens", "displayedOutputTokens", "reasoningTokens", "cacheReadTokens", "cacheWriteTokens", "totalTokens":
	default:
		return nil, ErrQueryInvalid
	}
	if q.Filter.Tier != "" && q.Filter.Tier != "fast" && q.Filter.Tier != "std" {
		return nil, ErrQueryInvalid
	}
	for _, rule := range q.Rules {
		if rule.LongThreshold != nil && *rule.LongThreshold <= 0 {
			return nil, ErrQueryInvalid
		}
	}
	for _, module := range q.Modules {
		switch module {
		case "models", "api_models", "credentials", "hours", "days", "minutes", "rates", "status", "health":
		default:
			return nil, ErrQueryInvalid
		}
	}
	if q.Timezone == "" {
		q.Timezone = "UTC"
	}
	loc, err := time.LoadLocation(q.Timezone)
	if err != nil {
		return nil, ErrQueryInvalid
	}
	return loc, nil
}

func queryTimeMatches(q QueryRequest, d RequestDetail) bool {
	ms := d.Timestamp.UnixMilli()
	if q.FromMS != nil && (ms <= 0 || ms < *q.FromMS) {
		return false
	}
	return q.ToMS == nil || ms > 0 && ms <= *q.ToMS
}
func queryFilterMatches(f QueryFilter, api, model string, d RequestDetail) bool {
	if f.Model != nil && *f.Model != model || f.API != nil && *f.API != api || f.AuthIndex != nil && *f.AuthIndex != d.AuthIndex || f.Failed != nil && *f.Failed != d.Failed {
		return false
	}
	if len(f.Identities) > 0 {
		found := false
		for _, id := range f.Identities {
			if id.Source == d.Source && id.AuthIndex == d.AuthIndex {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if f.Tier != "" {
		tier, _ := queryServiceTier(d)
		if tier != f.Tier {
			return false
		}
	}
	if f.Effort != nil && strings.ToLower(strings.TrimSpace(d.ReasoningEffort)) != *f.Effort {
		return false
	}
	t := d.Tokens
	if f.Cached != nil && *f.Cached != (t.CacheReadTokens > 0 || t.CacheCreationTokens > 0) {
		return false
	}
	var n int64
	switch f.Metric {
	case "totalInputTokens":
		n = t.InputTokens
	case "nonCacheReadInputTokens":
		n = max(t.InputTokens-t.CacheReadTokens, 0)
	case "totalOutputTokens":
		n = t.OutputTokens
	case "displayedOutputTokens":
		n = max(t.OutputTokens-t.ReasoningTokens, 0)
	case "reasoningTokens":
		n = t.ReasoningTokens
	case "cacheReadTokens":
		n = t.CacheReadTokens
	case "cacheWriteTokens":
		n = t.CacheCreationTokens
	case "totalTokens":
		n = t.TotalTokens
	default:
		return true
	}
	return (f.Minimum == nil || float64(n) >= *f.Minimum) && (f.Maximum == nil || float64(n) <= *f.Maximum)
}

// 在锁内只固定桶和长度，分块复制常量大小的只读值；聚合/编码不持统计锁。
// 既有记录不可变，追加或历史导入由内部序号排除，不建立第二份完整快照。
func (s *RequestStatistics) walkQuery(ctx context.Context, q *QueryRequest, visit func(string, string, RequestDetail) error) error {
	type span struct {
		api, model string
		bucket     *modelStats
		length     int
	}
	s.mu.RLock()
	bound := queryBound{s.queryGeneration, s.querySequence}
	if q.Bound != "" {
		if err := queryDecode(q.Bound, &bound); err != nil {
			s.mu.RUnlock()
			return err
		}
		if bound.Generation != s.queryGeneration || bound.Sequence > s.querySequence {
			s.mu.RUnlock()
			return ErrQueryExpired
		}
	} else {
		q.Bound = queryEncode(bound)
	}
	spans := make([]span, 0)
	for api, a := range s.apis {
		for model, b := range a.Models {
			spans = append(spans, span{api, model, b, len(b.Details)})
		}
	}
	s.mu.RUnlock()
	var block [256]RequestDetail
	for _, p := range spans {
		for offset := 0; offset < p.length; offset += len(block) {
			if err := ctx.Err(); err != nil {
				return err
			}
			s.mu.RLock()
			n := copy(block[:], p.bucket.Details[offset:min(offset+len(block), p.length)])
			s.mu.RUnlock()
			for _, d := range block[:n] {
				if d.querySequence <= bound.Sequence {
					if err := visit(p.api, p.model, d); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func (s *RequestStatistics) QuerySummary(ctx context.Context, q QueryRequest, prices bool) (*QuerySummary, error) {
	loc, err := validateQuery(&q)
	if err != nil {
		return nil, err
	}
	result := &QuerySummary{Version: QueryVersion, NowMS: q.NowMS, Groups: map[string][]QueryGroup{}}
	groups := map[queryGroupKey]*QueryGroup{}
	modules := map[string]bool{}
	for _, m := range q.Modules {
		modules[m] = true
		result.Groups[m] = []QueryGroup{}
	}
	add := func(key queryGroupKey, d RequestDetail, model string) error {
		g := groups[key]
		if g == nil {
			if len(groups) >= 100000 {
				return fmt.Errorf("%w: narrow the query range or modules", ErrQueryInvalid)
			}
			g = &QueryGroup{API: key.api, Model: key.model, Source: key.source, AuthIndex: key.auth, StartMS: key.start}
			groups[key] = g
		}
		g.Metrics.add(d, model, q.Rules[model], prices)
		return nil
	}
	err = s.walkQuery(ctx, &q, func(api, model string, d RequestDetail) error {
		ms := d.Timestamp.UnixMilli()
		// 健康网格始终使用完整会话的最近七天，不受概览的独立时间选择影响。
		if modules["health"] && ms > 0 && ms <= q.NowMS {
			idx := int64(671) - (q.NowMS-ms)/900000
			if idx >= 0 {
				if err := add(queryGroupKey{module: "health", start: q.NowMS - 672*900000 + idx*900000}, d, model); err != nil {
					return err
				}
			}
		}
		if !queryTimeMatches(q, d) || !queryFilterMatches(q.Filter, api, model, d) {
			return nil
		}
		result.Totals.add(d, model, q.Rules[model], prices)
		keys := make([]queryGroupKey, 0, 8)
		if modules["models"] {
			keys = append(keys, queryGroupKey{module: "models", model: model})
		}
		if modules["api_models"] {
			keys = append(keys, queryGroupKey{module: "api_models", api: api, model: model})
		}
		if modules["credentials"] {
			keys = append(keys, queryGroupKey{module: "credentials", source: d.Source, auth: d.AuthIndex})
		}
		if ms > 0 {
			t := d.Timestamp.In(loc)
			if modules["days"] {
				keys = append(keys, queryGroupKey{module: "days", model: model, start: time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc).UnixMilli()})
			}
			hourFrom := q.NowMS - 24*3600000
			if q.HourFromMS != nil {
				hourFrom = *q.HourFromMS
			}
			if modules["hours"] && ms >= hourFrom {
				keys = append(keys, queryGroupKey{module: "hours", model: model, start: time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, loc).UnixMilli()})
			}
			if modules["minutes"] && ms >= q.NowMS-3600000 && ms <= q.NowMS {
				idx := min((ms-(q.NowMS-3600000))/60000, 59)
				keys = append(keys, queryGroupKey{module: "minutes", start: q.NowMS - 3600000 + idx*60000})
			}
			if modules["rates"] && ms >= q.NowMS-1800000 && ms <= q.NowMS {
				keys = append(keys, queryGroupKey{module: "rates"})
			}
			if modules["status"] && ms <= q.NowMS {
				idx := int64(19) - (q.NowMS-ms)/600000
				if idx >= 0 {
					keys = append(keys, queryGroupKey{module: "status", source: d.Source, auth: d.AuthIndex, start: q.NowMS - 12000000 + idx*600000})
				}
			}
		}
		for _, key := range keys {
			if err := add(key, d, model); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	result.Bound = q.Bound
	result.Totals.finish()
	for key, g := range groups {
		g.Metrics.finish()
		result.Groups[key.module] = append(result.Groups[key.module], *g)
	}
	for module := range result.Groups {
		sort.Slice(result.Groups[module], func(i, j int) bool {
			a, b := result.Groups[module][i], result.Groups[module][j]
			if a.StartMS != b.StartMS {
				return a.StartMS < b.StartMS
			}
			if a.API != b.API {
				return a.API < b.API
			}
			if a.Model != b.Model {
				return a.Model < b.Model
			}
			if a.Source != b.Source {
				return a.Source < b.Source
			}
			return a.AuthIndex < b.AuthIndex
		})
	}
	return result, nil
}

type QueryItem struct {
	ID     string        `json:"id"`
	API    string        `json:"api"`
	Model  string        `json:"model"`
	Detail RequestDetail `json:"detail"`
}
type QueryOptions struct {
	Models      []string        `json:"models"`
	APIs        []string        `json:"apis"`
	Identities  []QueryIdentity `json:"identities"`
	AuthIndices []string        `json:"auth_indices"`
	Efforts     []string        `json:"efforts"`
	Metrics     QueryMetrics    `json:"metrics"`
}
type QueryDetails struct {
	Version    int           `json:"version"`
	Bound      string        `json:"bound"`
	Items      []QueryItem   `json:"items"`
	Total      int64         `json:"total"`
	NextCursor string        `json:"next_cursor,omitempty"`
	Metrics    QueryMetrics  `json:"metrics"`
	Options    *QueryOptions `json:"options,omitempty"`
}
type queryHeap []QueryItem

func (h queryHeap) Len() int { return len(h) }
func queryItemBefore(a, b QueryItem) bool {
	am, bm := a.Detail.Timestamp.UnixMilli(), b.Detail.Timestamp.UnixMilli()
	if am != bm {
		return am > bm
	}
	return a.Detail.querySequence > b.Detail.querySequence
}
func (h queryHeap) Less(i, j int) bool { return queryItemBefore(h[j], h[i]) }
func (h queryHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *queryHeap) Push(v any)        { *h = append(*h, v.(QueryItem)) }
func (h *queryHeap) Pop() any          { n := len(*h); v := (*h)[n-1]; *h = (*h)[:n-1]; return v }

func (s *RequestStatistics) QueryDetails(ctx context.Context, q QueryRequest) (*QueryDetails, error) {
	if _, err := validateQuery(&q); err != nil {
		return nil, err
	}
	fingerprint := sha256.Sum256([]byte(queryEncode(struct {
		From, To *int64
		Filter   QueryFilter
	}{q.FromMS, q.ToMS, q.Filter})))
	filterID := hex.EncodeToString(fingerprint[:])
	var cursor queryCursor
	if q.Cursor != "" {
		if err := queryDecode(q.Cursor, &cursor); err != nil {
			return nil, err
		}
		if cursor.Bound != q.Bound || cursor.Filter != filterID {
			return nil, ErrQueryInvalid
		}
	}
	r := &QueryDetails{Version: QueryVersion, Items: []QueryItem{}}
	models, apis, auths, efforts := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	identities := map[QueryIdentity]bool{}
	if q.IncludeOptions {
		r.Options = &QueryOptions{Models: []string{}, APIs: []string{}, Identities: []QueryIdentity{}, AuthIndices: []string{}, Efforts: []string{}}
	}
	h := &queryHeap{}
	err := s.walkQuery(ctx, &q, func(api, model string, d RequestDetail) error {
		if !queryTimeMatches(q, d) {
			return nil
		}
		if r.Options != nil {
			models[model] = true
			apis[api] = true
			auths[d.AuthIndex] = true
			efforts[strings.TrimSpace(d.ReasoningEffort)] = true
			identities[QueryIdentity{d.Source, d.AuthIndex}] = true
			r.Options.Metrics.add(d, model, QueryPriceRule{}, false)
		}
		if !queryFilterMatches(q.Filter, api, model, d) {
			return nil
		}
		r.Total++
		r.Metrics.add(d, model, QueryPriceRule{}, false)
		if q.Cursor != "" {
			ms := d.Timestamp.UnixMilli()
			if ms > cursor.Timestamp || ms == cursor.Timestamp && d.querySequence >= cursor.Sequence {
				return nil
			}
		}
		item := QueryItem{API: api, Model: model, Detail: d}
		if h.Len() < q.Limit+1 {
			heap.Push(h, item)
		} else if queryItemBefore(item, (*h)[0]) {
			(*h)[0] = item
			heap.Fix(h, 0)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	r.Bound = q.Bound
	r.Items = []QueryItem(*h)
	sort.Slice(r.Items, func(i, j int) bool { return queryItemBefore(r.Items[i], r.Items[j]) })
	if len(r.Items) > q.Limit {
		r.Items = r.Items[:q.Limit]
		last := r.Items[len(r.Items)-1]
		r.NextCursor = queryEncode(queryCursor{q.Bound, filterID, last.Detail.Timestamp.UnixMilli(), last.Detail.querySequence})
	}
	for i := range r.Items {
		r.Items[i].ID = strconv.FormatUint(r.Items[i].Detail.querySequence, 10)
	}
	if r.Options != nil {
		for v := range models {
			r.Options.Models = append(r.Options.Models, v)
		}
		sort.Strings(r.Options.Models)
		for v := range apis {
			r.Options.APIs = append(r.Options.APIs, v)
		}
		sort.Strings(r.Options.APIs)
		for v := range auths {
			r.Options.AuthIndices = append(r.Options.AuthIndices, v)
		}
		sort.Strings(r.Options.AuthIndices)
		for v := range efforts {
			r.Options.Efforts = append(r.Options.Efforts, v)
		}
		sort.Strings(r.Options.Efforts)
		for v := range identities {
			r.Options.Identities = append(r.Options.Identities, v)
		}
		sort.Slice(r.Options.Identities, func(i, j int) bool {
			a, b := r.Options.Identities[i], r.Options.Identities[j]
			if a.Source != b.Source {
				return a.Source < b.Source
			}
			return a.AuthIndex < b.AuthIndex
		})
	}
	return r, nil
}
