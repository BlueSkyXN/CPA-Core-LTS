package helps

import (
	"context"
	"net/http"
	"sync/atomic"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
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

// beginUpstreamHTTPAttempt starts byte accounting for one HTTP round trip.
// The payload size is only known here when the body is sent unencoded.
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
