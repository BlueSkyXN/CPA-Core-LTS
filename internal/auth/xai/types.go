// Package xai provides OAuth2 authentication helpers for xAI Grok.
package xai

import (
	"fmt"
	"runtime"
	"time"
)

const (
	// DefaultAPIBaseURL is the default official xAI API base URL.
	// Used for OAuth credential defaults, websocket,
	// and HTTP chat/media when auth using_api is true or non-OAuth.
	DefaultAPIBaseURL = "https://api.x.ai/v1"
	// CLIChatProxyBaseURL is the Grok CLI chat-proxy base URL for HTTP chat
	// and media (image/video) when auth using_api is false, including the OAuth default.
	CLIChatProxyBaseURL = "https://cli-chat-proxy.grok.com/v1"
	// Issuer is xAI's OAuth issuer.
	Issuer = "https://auth.x.ai"
	// DiscoveryURL is the OIDC discovery endpoint used to resolve OAuth endpoints.
	DiscoveryURL = Issuer + "/.well-known/openid-configuration"
	// ClientID is the public xAI Grok CLI OAuth client ID.
	ClientID = "b1a00492-073a-47ea-816f-4c329264a828"
	// Scope is the OAuth scope set required for xAI API access.
	Scope = "openid profile email offline_access grok-cli:access api:access"
	// DeviceCodeGrantType is the OAuth2 device authorization grant type (RFC 8628).
	DeviceCodeGrantType = "urn:ietf:params:oauth:grant-type:device_code"
	// defaultPollInterval is used when the device endpoint omits interval.
	defaultPollInterval = 5 * time.Second
	// httpClientTimeout bounds credential-acquisition HTTP calls (device/token/refresh).
	httpClientTimeout = 30 * time.Second
	// MaxPollDuration is the upper bound for waiting on user authorization.
	MaxPollDuration = 30 * time.Minute
	// UserEndpoint is the official Grok CLI identity endpoint. Its response
	// contains the billing user_id; the OIDC sub claim is not interchangeable.
	UserEndpoint = CLIChatProxyBaseURL + "/user"
	// UserEndpointTokenAuthValue identifies the official Grok CLI OAuth caller.
	UserEndpointTokenAuthValue = "xai-grok-cli"
	// UserEndpointClientVersion matches the current official Grok Shell request
	// identity (it is independent of chat-proxy's API version).
	UserEndpointClientVersion = "1.0.3"
	// UserEndpointClientMode selects the interactive OAuth identity contract.
	UserEndpointClientMode = "interactive"
)

var refreshLead = 5 * time.Minute

// RefreshLead returns the refresh lead time for xAI OAuth credentials.
func RefreshLead() time.Duration {
	return refreshLead
}

// UserEndpointUserAgent returns the current official interactive Grok client
// identity with the host platform rendered using Grok Shell's wire labels.
func UserEndpointUserAgent() string {
	return userEndpointUserAgent(runtime.GOOS, runtime.GOARCH)
}

func userEndpointUserAgent(goos, goarch string) string {
	osName := goos
	if osName == "darwin" {
		osName = "macos"
	}
	archName := goarch
	switch archName {
	case "arm64":
		archName = "aarch64"
	case "amd64":
		archName = "x86_64"
	}
	return fmt.Sprintf(
		"grok-pager/%s grok-shell/%s (%s; %s)",
		UserEndpointClientVersion,
		UserEndpointClientVersion,
		osName,
		archName,
	)
}

// Discovery contains OAuth endpoints resolved from xAI OIDC discovery.
type Discovery struct {
	DeviceAuthorizationEndpoint string `json:"device_authorization_endpoint"`
	TokenEndpoint               string `json:"token_endpoint"`
}

// DeviceCodeResponse represents xAI's device authorization response.
type DeviceCodeResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
	TokenEndpoint           string `json:"-"`
}

// TokenData holds xAI OAuth token data.
type TokenData struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
	ExpiresIn    int    `json:"expires_in,omitempty"`
	Expire       string `json:"expired,omitempty"`
	Email        string `json:"email,omitempty"`
	Subject      string `json:"sub,omitempty"`
	// UserID is the billing identity returned by UserEndpoint. It is kept
	// separate from Subject because the OIDC sub claim is not a billing ID.
	UserID string `json:"user_id,omitempty"`
}

// AuthBundle aggregates token data and OAuth metadata for persistence.
type AuthBundle struct {
	TokenData     TokenData
	LastRefresh   string
	BaseURL       string
	RedirectURI   string
	TokenEndpoint string
}
