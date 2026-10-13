package usage

import (
	"container/heap"
	"context"
	"errors"
	"sort"
	"strconv"
	"sync"
	"time"
)

const QueryExportVersion = 1
const MaxQueryExportPageSize = 1000
const exportIndexRows = 100000
const exportCacheRows = 200000
const exportCacheEntries = 4
const exportCacheTTL = 4 * time.Minute

var ErrQueryExportBusy = errors.New("usage export query busy")
var errExportIndexLarge = errors.New("usage export index capacity exceeded")

// 索引只引用 append-only 存储中的位置，不复制完整明细，也不固定旧 slice backing array。
type exportRef struct {
	api, model string
	bucket     *modelStats
	offset     int
	timestamp  int64
	sequence   uint64
}

func exportBefore(a, b exportRef) bool {
	return a.timestamp > b.timestamp || a.timestamp == b.timestamp && a.sequence > b.sequence
}

type exportEntry struct {
	refs          []exportRef
	oversized     bool
	expires, used time.Time
}

type usageExportCache struct {
	mu      sync.Mutex
	entries map[string]exportEntry
	rows    int
	active  int
	scans   uint64
}

type QueryExportPage struct {
	Version    int         `json:"version"`
	Bound      string      `json:"bound"`
	Items      []QueryItem `json:"items"`
	Total      int64       `json:"total"`
	NextCursor string      `json:"next_cursor,omitempty"`
}

func (s *RequestStatistics) exportIndex(ctx context.Context, q QueryRequest, key string) (exportEntry, error) {
	c := &s.exportQueries
	c.mu.Lock()
	if c.entries == nil {
		c.entries = make(map[string]exportEntry)
	}
	now := time.Now()
	for k, entry := range c.entries {
		if !now.Before(entry.expires) {
			delete(c.entries, k)
			c.rows -= cap(entry.refs)
		}
	}
	if entry, ok := c.entries[key]; ok {
		entry.used = now
		c.entries[key] = entry
		c.mu.Unlock()
		return entry, nil
	}
	if c.active >= 2 {
		c.mu.Unlock()
		return exportEntry{}, ErrQueryExportBusy
	}
	c.active++
	c.scans++
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.active--; c.mu.Unlock() }()

	s.mu.RLock()
	capacity := min(s.totalRequests, int64(exportIndexRows))
	s.mu.RUnlock()
	refs := make([]exportRef, 0, int(capacity))
	err := s.walkQueryRows(ctx, &q, func(api, model string, bucket *modelStats, offset int, d RequestDetail) error {
		if queryTimeMatches(q, d) && queryFilterMatches(q.Filter, api, model, d) {
			if len(refs) >= exportIndexRows {
				return errExportIndexLarge
			}
			refs = append(refs, exportRef{api, model, bucket, offset, d.Timestamp.UnixMilli(), d.querySequence})
		}
		return nil
	})
	entry := exportEntry{refs: refs, oversized: errors.Is(err, errExportIndexLarge)}
	if entry.oversized {
		// 记住超限结果，后续页不再反复构建索引；有界堆路径仍返回全部匹配记录。
		entry.refs = nil
	} else if err != nil {
		return exportEntry{}, err
	} else {
		sort.Slice(refs, func(i, j int) bool { return exportBefore(refs[i], refs[j]) })
	}
	if err := ctx.Err(); err != nil {
		return exportEntry{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if previous, ok := c.entries[key]; ok {
		c.rows -= cap(previous.refs)
		delete(c.entries, key)
	}
	for len(c.entries) >= exportCacheEntries || c.rows+cap(entry.refs) > exportCacheRows {
		var oldest string
		var used time.Time
		for k, value := range c.entries {
			if used.IsZero() || value.used.Before(used) {
				oldest, used = k, value.used
			}
		}
		c.rows -= cap(c.entries[oldest].refs)
		delete(c.entries, oldest)
	}
	entry.used = time.Now()
	entry.expires = entry.used.Add(exportCacheTTL)
	c.entries[key] = entry
	c.rows += cap(entry.refs)
	return entry, nil
}

type exportHeap []exportRef

func (h exportHeap) Len() int           { return len(h) }
func (h exportHeap) Less(i, j int) bool { return exportBefore(h[j], h[i]) }
func (h exportHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *exportHeap) Push(v any)        { *h = append(*h, v.(exportRef)) }
func (h *exportHeap) Pop() any          { n := len(*h); v := (*h)[n-1]; *h = (*h)[:n-1]; return v }

func (s *RequestStatistics) QueryExport(ctx context.Context, q QueryRequest) (*QueryExportPage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limit := q.Limit
	if limit == 0 {
		limit = MaxQueryExportPageSize
	}
	if limit < 1 || limit > MaxQueryExportPageSize || q.IncludeOptions || len(q.Modules) > 0 || len(q.Rules) > 0 || q.HourFromMS != nil {
		return nil, ErrQueryInvalid
	}
	q.Limit = 1 // 共用其余字段校验；旧 details 的 200 条上限保持不变。
	if _, err := validateQuery(&q); err != nil {
		return nil, err
	}
	q.Limit = limit
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
	filterID := queryFilterID(q)
	var cursor queryCursor
	if q.Cursor != "" {
		if err := queryDecode(q.Cursor, &cursor); err != nil || cursor.Bound != q.Bound || cursor.Filter != filterID {
			return nil, ErrQueryInvalid
		}
	}
	entry, err := s.exportIndex(ctx, q, q.Bound+":"+filterID)
	if err != nil {
		return nil, err
	}
	refs := entry.refs
	total := int64(len(refs))
	after := func(ref exportRef) bool {
		return q.Cursor == "" || ref.timestamp < cursor.Timestamp || ref.timestamp == cursor.Timestamp && ref.sequence < cursor.Sequence
	}
	if entry.oversized {
		s.exportQueries.mu.Lock()
		s.exportQueries.scans++
		s.exportQueries.mu.Unlock()
		h := &exportHeap{}
		err := s.walkQueryRows(ctx, &q, func(api, model string, bucket *modelStats, offset int, d RequestDetail) error {
			if !queryTimeMatches(q, d) || !queryFilterMatches(q.Filter, api, model, d) {
				return nil
			}
			total++
			ref := exportRef{api, model, bucket, offset, d.Timestamp.UnixMilli(), d.querySequence}
			if after(ref) {
				if h.Len() < limit+1 {
					heap.Push(h, ref)
				} else if exportBefore(ref, (*h)[0]) {
					(*h)[0] = ref
					heap.Fix(h, 0)
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		refs = []exportRef(*h)
		sort.Slice(refs, func(i, j int) bool { return exportBefore(refs[i], refs[j]) })
	} else if q.Cursor != "" {
		refs = refs[sort.Search(len(refs), func(i int) bool { return after(refs[i]) }):]
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := &QueryExportPage{Version: QueryVersion, Bound: q.Bound, Total: total, Items: make([]QueryItem, min(len(refs), limit))}
	s.mu.RLock()
	for i := range result.Items {
		ref := refs[i]
		result.Items[i] = QueryItem{ID: strconv.FormatUint(ref.sequence, 10), API: ref.api, Model: ref.model, Detail: ref.bucket.Details[ref.offset]}
	}
	s.mu.RUnlock()
	if len(refs) > limit {
		last := refs[limit-1]
		result.NextCursor = queryEncode(queryCursor{q.Bound, filterID, last.timestamp, last.sequence})
	}
	return result, nil
}
