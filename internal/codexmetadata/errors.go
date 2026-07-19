package codexmetadata

import "net/http"

const invalidRequestMessage = `{"error":{"message":"invalid Codex client_metadata","type":"invalid_request_error","code":"invalid_client_metadata"}}`

type invalidRequestError struct{}

func (invalidRequestError) Error() string         { return invalidRequestMessage }
func (invalidRequestError) StatusCode() int       { return http.StatusBadRequest }
func (invalidRequestError) IsRequestScoped() bool { return true }

// InvalidRequestError returns a safe request-scoped 400 without echoing
// untrusted metadata values.
func InvalidRequestError() error {
	return invalidRequestError{}
}
