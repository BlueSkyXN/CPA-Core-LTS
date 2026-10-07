package executor

import (
	"errors"
	"testing"
)

func TestValidateRequestPathOverride(t *testing.T) {
	cases := []struct {
		current, requested string
		ok                 bool
	}{
		{"/v1/images/edits", "/v1/images/generations", true},
		{"/v1/images/generations", "/v1/images/edits?x=1", true},
		{"/v1/responses", "/v1/responses/", true},
		{"/v1/responses", "", true},
		{"/v1/responses", "/v1/images/generations", false},
		{"/v1/responses", "/v1/responses/compact", false},
		{"/v1/chat/completions", "/v1/messages", false},
		{"", "/v1/images/generations", false},
	}
	for _, tc := range cases {
		err := ValidateRequestPathOverride(tc.current, tc.requested)
		if tc.ok != (err == nil) {
			t.Fatalf("%q -> %q: err=%v, want ok=%t", tc.current, tc.requested, err, tc.ok)
		}
		if err != nil {
			var pathErr *RequestPathOverrideError
			if !errors.As(err, &pathErr) || !pathErr.IsRequestScoped() || pathErr.StatusCode() != 500 {
				t.Fatalf("unexpected error shape: %#v", err)
			}
		}
	}
}
