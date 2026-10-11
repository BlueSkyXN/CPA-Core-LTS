package logging

import (
	"context"
	"strconv"
	"strings"
	"sync"
)

type upstreamRequestSizeKey struct{}

// UpstreamRequestSize is the byte-count summary of one provider attempt,
// shared with failure logs. Zero values mean "not measured".
type UpstreamRequestSize struct {
	Transport     string
	RequestBytes  int64
	WireBytes     int64
	Encoding      string
	ResponseBytes int64
}

type upstreamRequestSizeHolder struct {
	mu   sync.RWMutex
	size UpstreamRequestSize
	set  bool
}

// WithFreshUpstreamRequestSizeHolder starts an isolated holder for one
// upstream attempt so a retry cannot report the previous attempt's sizes.
func WithFreshUpstreamRequestSizeHolder(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, upstreamRequestSizeKey{}, &upstreamRequestSizeHolder{})
}

// SetUpstreamRequestSize stores the attempt size when ctx carries a holder.
func SetUpstreamRequestSize(ctx context.Context, size UpstreamRequestSize) {
	if ctx == nil {
		return
	}
	holder, ok := ctx.Value(upstreamRequestSizeKey{}).(*upstreamRequestSizeHolder)
	if !ok || holder == nil {
		return
	}
	holder.mu.Lock()
	holder.size = size
	holder.set = true
	holder.mu.Unlock()
}

// GetUpstreamRequestSize returns the stored attempt size, if any.
func GetUpstreamRequestSize(ctx context.Context) (UpstreamRequestSize, bool) {
	if ctx == nil {
		return UpstreamRequestSize{}, false
	}
	holder, ok := ctx.Value(upstreamRequestSizeKey{}).(*upstreamRequestSizeHolder)
	if !ok || holder == nil {
		return UpstreamRequestSize{}, false
	}
	holder.mu.RLock()
	defer holder.mu.RUnlock()
	return holder.size, holder.set
}

// LogFields renders the measured fields as space-separated key=value pairs.
// Wire bytes are only shown when they differ from the payload size.
func (s UpstreamRequestSize) LogFields() string {
	var parts []string
	if s.Transport != "" {
		parts = append(parts, "transport="+s.Transport)
	}
	if s.RequestBytes > 0 {
		parts = append(parts, "req_bytes="+strconv.FormatInt(s.RequestBytes, 10))
	}
	if s.WireBytes > 0 && s.WireBytes != s.RequestBytes {
		parts = append(parts, "wire_bytes="+strconv.FormatInt(s.WireBytes, 10))
	}
	if s.Encoding != "" {
		parts = append(parts, "encoding="+s.Encoding)
	}
	if s.ResponseBytes > 0 {
		parts = append(parts, "resp_bytes="+strconv.FormatInt(s.ResponseBytes, 10))
	}
	return strings.Join(parts, " ")
}
