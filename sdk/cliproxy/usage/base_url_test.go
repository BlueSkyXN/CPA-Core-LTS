package usage

import "testing"

func TestSafeBaseURL(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"https://user:password@example.com/v1?key=private#private", "https://example.com/v1"},
		{" https://example.com/v1 ", "https://example.com/v1"},
		{"https://example.com/v1?", "https://example.com/v1"},
		{"https://%zz", ""},
		{"relative/private", ""},
		{"file:///private", ""},
		{"", ""},
	} {
		if got := SafeBaseURL(tc.input); got != tc.want {
			t.Fatal("unexpected sanitized base URL")
		}
	}
}
