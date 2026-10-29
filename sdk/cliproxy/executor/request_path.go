package executor

import (
	"fmt"
	"net/http"
	"strings"
)

// RequestPathOverrideError reports a request interceptor Path override that
// would change the protocol operation of an already routed request. Executors
// only consult the request path for a few operation variants, so accepting
// other overrides would silently run the original operation instead.
type RequestPathOverrideError struct {
	Current   string
	Requested string
}

func (e *RequestPathOverrideError) Error() string {
	return fmt.Sprintf("request interceptor path override %q is not supported for request path %q", e.Requested, e.Current)
}

// StatusCode reports a server-side plugin configuration failure.
func (e *RequestPathOverrideError) StatusCode() int { return http.StatusInternalServerError }

// IsRequestScoped keeps the failure from penalizing or rotating credentials.
func (e *RequestPathOverrideError) IsRequestScoped() bool { return true }

var requestPathOverrideFamilies = [][]string{
	{"/v1/images/generations", "/v1/images/edits"},
}

func normalizeRequestPath(path string) string {
	path = strings.TrimSpace(path)
	if idx := strings.IndexAny(path, "?#"); idx >= 0 {
		path = path[:idx]
	}
	if len(path) > 1 {
		path = strings.TrimRight(path, "/")
	}
	return path
}

// ValidateRequestPathOverride accepts a Path override only when it keeps the
// current operation or switches between variants that executors honor (image
// generations and edits). current is the request path already recorded in
// Options.Metadata[RequestPathMetadataKey].
func ValidateRequestPathOverride(current, requested string) error {
	requested = normalizeRequestPath(requested)
	if requested == "" {
		return nil
	}
	current = normalizeRequestPath(current)
	if current == requested {
		return nil
	}
	for _, family := range requestPathOverrideFamilies {
		inCurrent, inRequested := false, false
		for _, path := range family {
			inCurrent = inCurrent || path == current
			inRequested = inRequested || path == requested
		}
		if inCurrent && inRequested {
			return nil
		}
	}
	return &RequestPathOverrideError{Current: current, Requested: requested}
}

// RequestPathFromMetadata returns the recorded inbound request path.
func RequestPathFromMetadata(metadata map[string]any) string {
	if metadata == nil {
		return ""
	}
	path, _ := metadata[RequestPathMetadataKey].(string)
	return path
}
