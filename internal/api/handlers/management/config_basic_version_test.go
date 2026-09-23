package management

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

type latestVersionRoundTripper func(*http.Request) (*http.Response, error)

func (fn latestVersionRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestGetLatestVersionUsesLTSRelease(t *testing.T) {
	previousTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previousTransport })
	requested := false
	http.DefaultTransport = latestVersionRoundTripper(func(req *http.Request) (*http.Response, error) {
		requested = true
		if got := req.URL.String(); got != "https://api.github.com/repos/BlueSkyXN/CPA-Core-LTS/releases/latest" {
			t.Errorf("release URL = %q, want CPA-Core-LTS release source", got)
		}
		if got := req.Header.Get("User-Agent"); got != "sky-cpa-core-lts" {
			t.Errorf("User-Agent = %q, want sky-cpa-core-lts", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"tag_name":"v1-lts-0.0.99","name":"Core LTS fixture"}`)),
			Request:    req,
		}, nil
	})

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/latest-version", nil)
	(&Handler{}).GetLatestVersion(ctx)
	if !requested {
		t.Fatal("latest release was not requested")
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	var response map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response) != 1 || response["latest-version"] != "v1-lts-0.0.99" {
		t.Fatalf("response = %v, want the unchanged latest-version contract", response)
	}
}

func TestSetLatestReleaseRequestHeaders(t *testing.T) {
	tests := []struct {
		name              string
		githubToken       string
		wantAuthorization string
	}{
		{
			name:              "sets GitHub authorization",
			githubToken:       "release-token",
			wantAuthorization: "Bearer release-token",
		},
		{
			name: "omits authorization without token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GITHUB_TOKEN", tt.githubToken)
			if tt.githubToken == "" {
				t.Setenv("github_token", "")
			}
			t.Setenv("GITSTORE_GIT_TOKEN", "")
			t.Setenv("GITSTORE_GIT_URL", "")

			req := httptest.NewRequest(http.MethodGet, latestReleaseURL, nil)
			setLatestReleaseRequestHeaders(req)

			if got := req.Header.Get("Authorization"); got != tt.wantAuthorization {
				t.Fatalf("Authorization = %q, want %q", got, tt.wantAuthorization)
			}
			if got := req.Header.Get("Accept"); got != "application/vnd.github+json" {
				t.Fatalf("Accept = %q, want GitHub JSON media type", got)
			}
			if got := req.Header.Get("User-Agent"); got != latestReleaseUserAgent {
				t.Fatalf("User-Agent = %q, want %q", got, latestReleaseUserAgent)
			}
		})
	}
}
