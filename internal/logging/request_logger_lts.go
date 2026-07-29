package logging

// DeferredAPIRequestSource provides a concurrency-safe snapshot of deferred upstream requests.
type DeferredAPIRequestSource interface {
	SnapshotDeferredAPIRequests() []DeferredAPIRequest
}
