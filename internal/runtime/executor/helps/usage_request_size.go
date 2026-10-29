package helps

import (
	"context"
	"net/http"
	"sync/atomic"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// upstreamSizeAttempt holds the byte counts of one provider attempt. Response
// bytes are added by the goroutine reading the body, so they are atomic.
type upstreamSizeAttempt struct {
	transport     string
	requestBytes  int64
	wireBytes     int64
	encoding      string
	responseBytes atomic.Int64
}

// clientRequestSizeFromContext reads the inbound body size and encoding from
// the Gin request. Unknown (chunked) and empty bodies stay unmeasured, which
// also covers websocket upgrade requests.
func clientRequestSizeFromContext(ctx context.Context) (int64, string) {
	if ctx == nil {
		return 0, ""
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil || ginCtx.Request == nil || ginCtx.Request.ContentLength <= 0 {
		return 0, ""
	}
	return ginCtx.Request.ContentLength, usage.CanonicalContentEncoding(ginCtx.Request.Header.Get("Content-Encoding"))
}

type upstreamPayloadBytesKey struct{}

// WithUpstreamPayloadBytes records the unencoded size of a request body the
// caller sends with a Content-Encoding, so usage can report the payload size
// next to the wire size. Unencoded requests ignore it.
func WithUpstreamPayloadBytes(ctx context.Context, payloadBytes int) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if payloadBytes <= 0 {
		return ctx
	}
	return context.WithValue(ctx, upstreamPayloadBytesKey{}, int64(payloadBytes))
}

func upstreamPayloadBytes(ctx context.Context) int64 {
	if ctx == nil {
		return 0
	}
	n, _ := ctx.Value(upstreamPayloadBytesKey{}).(int64)
	return n
}

// beginUpstreamHTTPAttempt starts byte accounting for one HTTP round trip.
// An encoded body reports its payload size only when the caller recorded it
// with WithUpstreamPayloadBytes.
func (r *UsageReporter) beginUpstreamHTTPAttempt(req *http.Request) *upstreamSizeAttempt {
	if r == nil {
		return nil
	}
	attempt := &upstreamSizeAttempt{transport: usage.UpstreamTransportHTTP}
	if req != nil {
		if req.ContentLength > 0 {
			attempt.wireBytes = req.ContentLength
		}
		attempt.encoding = usage.CanonicalContentEncoding(req.Header.Get("Content-Encoding"))
		if attempt.encoding == "" {
			attempt.requestBytes = attempt.wireBytes
		} else {
			attempt.requestBytes = upstreamPayloadBytes(req.Context())
		}
	}
	r.setUpstreamAttempt(attempt)
	return attempt
}

// ObserveUpstreamWebsocketRequest starts byte accounting for a provider
// attempt sent as one websocket message. Outbound websocket messages are
// written uncompressed, so payload and wire sizes match.
func (r *UsageReporter) ObserveUpstreamWebsocketRequest(payloadBytes int) {
	if r == nil {
		return
	}
	attempt := &upstreamSizeAttempt{transport: usage.UpstreamTransportWebsocket}
	if payloadBytes > 0 {
		attempt.requestBytes = int64(payloadBytes)
		attempt.wireBytes = int64(payloadBytes)
	}
	r.setUpstreamAttempt(attempt)
}

// ObserveUpstreamWebsocketTransport marks the current attempt as websocket
// when the request message size is not attributable to this reporter.
func (r *UsageReporter) ObserveUpstreamWebsocketTransport() {
	if r == nil {
		return
	}
	r.setUpstreamAttempt(&upstreamSizeAttempt{transport: usage.UpstreamTransportWebsocket})
}

type usageReporterContextKey struct{}

// WithUsageReporter attaches the reporter that should receive request-size
// measurements for this execution. A nil reporter leaves the context unchanged.
func WithUsageReporter(ctx context.Context, reporter *UsageReporter) context.Context {
	if reporter == nil {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, usageReporterContextKey{}, reporter)
}

// UsageReporterFromContext returns the reporter attached by WithUsageReporter.
func UsageReporterFromContext(ctx context.Context) *UsageReporter {
	if ctx == nil {
		return nil
	}
	reporter, _ := ctx.Value(usageReporterContextKey{}).(*UsageReporter)
	return reporter
}

// ObserveUpstreamHTTPRequest records byte counts for one provider HTTP
// attempt without changing response timing. The returned function adds
// response bytes to this attempt only. A later attempt replaces the usage
// snapshot; bytes already added to an earlier attempt stay there.
func (r *UsageReporter) ObserveUpstreamHTTPRequest(req *http.Request) func(int) {
	attempt := r.beginUpstreamHTTPAttempt(req)
	if attempt == nil {
		return func(int) {}
	}
	return func(n int) {
		if n > 0 {
			attempt.responseBytes.Add(int64(n))
		}
	}
}

// ObserveUpstreamResponseBytes adds bytes read from the provider response
// body or websocket messages to the current attempt.
func (r *UsageReporter) ObserveUpstreamResponseBytes(n int) {
	if r == nil || n <= 0 {
		return
	}
	r.ensureUpstreamAttempt().responseBytes.Add(int64(n))
}

func (r *UsageReporter) setUpstreamAttempt(attempt *upstreamSizeAttempt) {
	r.sizeMu.Lock()
	r.upstreamAttempt = attempt
	r.sizeMu.Unlock()
}

func (r *UsageReporter) ensureUpstreamAttempt() *upstreamSizeAttempt {
	r.sizeMu.Lock()
	defer r.sizeMu.Unlock()
	if r.upstreamAttempt == nil {
		r.upstreamAttempt = &upstreamSizeAttempt{}
	}
	return r.upstreamAttempt
}

func (r *UsageReporter) requestSizeSnapshot() usage.RequestSize {
	if r == nil {
		return usage.RequestSize{}
	}
	size := usage.RequestSize{
		ClientRequestBytes:    r.clientRequestBytes,
		ClientRequestEncoding: r.clientRequestEncoding,
	}
	r.sizeMu.Lock()
	attempt := r.upstreamAttempt
	r.sizeMu.Unlock()
	if attempt != nil {
		size.UpstreamTransport = attempt.transport
		size.UpstreamRequestBytes = attempt.requestBytes
		size.UpstreamRequestWireBytes = attempt.wireBytes
		size.UpstreamRequestEncoding = attempt.encoding
		size.UpstreamResponseBytes = attempt.responseBytes.Load()
	}
	return size.Canonical()
}
