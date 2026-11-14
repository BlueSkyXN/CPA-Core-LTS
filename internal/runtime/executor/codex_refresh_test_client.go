package executor

import "net/http"

// codexRefreshTestHTTPClient is installed by codex refresh tests to route
// OAuth token traffic to local mock servers through an explicit transport
// instead of mutating the process-global http.DefaultTransport. Swapping the
// global raced with other executors being constructed concurrently (they clone
// DefaultTransport at construction time), producing intermittent
// refresh_outcome_unknown transport failures in CI. Production always sees nil.
var codexRefreshTestHTTPClient = func() *http.Client { return nil }
