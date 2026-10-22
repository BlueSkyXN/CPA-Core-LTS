package helps

import (
	"fmt"
	"strings"
	"testing"
)

type usageDiagnosticError struct {
	usageResponseBodyError
	reason string
}

func (e usageDiagnosticError) FailureReason() string { return e.reason }

func TestFailFromErrorsPrefersSafeDiagnosticWithoutChangingStatus(t *testing.T) {
	for _, reason := range []string{"bigmodel_1301", "zai_1301", "bigmodel_1399", "", "private prompt", "api_key=secret", strings.Repeat("x", 129)} {
		base := usageResponseBodyError{status: 502, message: "upstream_error: Content safety check blocked the request", body: []byte(`{"error":{"type":"upstream_error"}}`)}
		err := fmt.Errorf("plugin execution: %w", usageDiagnosticError{usageResponseBodyError: base, reason: reason})
		fail := failFromErrors(err)
		want := string(base.body)
		if strings.HasPrefix(reason, "bigmodel_") || strings.HasPrefix(reason, "zai_") {
			want = reason
		}
		if fail.StatusCode != 502 || fail.Body != want {
			t.Fatalf("reason %q: %+v", reason, fail)
		}
	}
}
