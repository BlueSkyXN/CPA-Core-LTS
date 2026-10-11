package usage

import "strings"

// RequestSizeVersionV1 identifies the byte-count contract used by RequestSize.
const RequestSizeVersionV1 uint32 = 1

const (
	// UpstreamTransportHTTP marks a provider attempt sent as an HTTP request.
	UpstreamTransportHTTP = "http"
	// UpstreamTransportWebsocket marks a provider attempt sent as a websocket message.
	UpstreamTransportWebsocket = "websocket"
)

const maxContentEncodingLength = 64

// RequestSize holds byte counts measured for a single provider request.
// Zero counts and empty strings mean "not measured"; Version is zero when no
// field was measured.
type RequestSize struct {
	// Version identifies the semantic contract of the fields below.
	Version uint32
	// ClientRequestBytes is the inbound request body size as received from the
	// client, before any Content-Encoding is decoded.
	ClientRequestBytes int64
	// ClientRequestEncoding is the inbound request Content-Encoding.
	ClientRequestEncoding string
	// UpstreamTransport is the transport used for the final provider attempt:
	// UpstreamTransportHTTP or UpstreamTransportWebsocket.
	UpstreamTransport string
	// UpstreamRequestBytes is the final provider request payload size before
	// any Content-Encoding is applied.
	UpstreamRequestBytes int64
	// UpstreamRequestWireBytes is the final provider request body size as
	// written to the connection, after Content-Encoding.
	UpstreamRequestWireBytes int64
	// UpstreamRequestEncoding is the Content-Encoding of the final provider request.
	UpstreamRequestEncoding string
	// UpstreamResponseBytes is the number of response body or message bytes
	// read from the provider for the final attempt, after transport decoding.
	UpstreamResponseBytes int64
}

// Measured reports whether any size field carries a measurement.
func (s RequestSize) Measured() bool {
	return s.ClientRequestBytes > 0 ||
		s.UpstreamTransport != "" ||
		s.UpstreamRequestBytes > 0 ||
		s.UpstreamRequestWireBytes > 0 ||
		s.UpstreamResponseBytes > 0
}

// Canonical returns the stable usage representation: negative counts are
// cleared, transport and encoding values are normalized, and Version is set
// only when a field remains measured.
func (s RequestSize) Canonical() RequestSize {
	s.ClientRequestBytes = nonNegativeSize(s.ClientRequestBytes)
	s.UpstreamRequestBytes = nonNegativeSize(s.UpstreamRequestBytes)
	s.UpstreamRequestWireBytes = nonNegativeSize(s.UpstreamRequestWireBytes)
	s.UpstreamResponseBytes = nonNegativeSize(s.UpstreamResponseBytes)
	s.UpstreamTransport = CanonicalUpstreamTransport(s.UpstreamTransport)
	s.ClientRequestEncoding = CanonicalContentEncoding(s.ClientRequestEncoding)
	s.UpstreamRequestEncoding = CanonicalContentEncoding(s.UpstreamRequestEncoding)
	if s.ClientRequestBytes == 0 {
		s.ClientRequestEncoding = ""
	}
	if s.UpstreamRequestBytes == 0 && s.UpstreamRequestWireBytes == 0 {
		s.UpstreamRequestEncoding = ""
	}
	s.Version = 0
	if s.Measured() {
		s.Version = RequestSizeVersionV1
	}
	return s
}

// CanonicalUpstreamTransport accepts only the stable transport values used by
// usage sinks; unknown values are cleared.
func CanonicalUpstreamTransport(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case UpstreamTransportHTTP:
		return UpstreamTransportHTTP
	case UpstreamTransportWebsocket:
		return UpstreamTransportWebsocket
	default:
		return ""
	}
}

// CanonicalContentEncoding lower-cases a Content-Encoding header value and
// drops identity, oversized, or malformed values.
func CanonicalContentEncoding(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || value == "identity" || len(value) > maxContentEncodingLength {
		return ""
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == ',', r == ' ':
		default:
			return ""
		}
	}
	return value
}

func nonNegativeSize(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}
