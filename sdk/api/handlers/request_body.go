package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

const defaultMaxRequestBodyBytes int64 = sdkconfig.DefaultAPIRequestBodyMaxBytes

// ErrRequestBodyTooLarge reports that either the encoded request body or one
// decoded Content-Encoding layer exceeded the API request body limit.
var ErrRequestBodyTooLarge = errors.New("request body too large")

// ReadRequestBody reads the incoming request body and decodes supported
// Content-Encoding values before handlers inspect JSON fields.
func ReadRequestBody(c *gin.Context) ([]byte, error) {
	return readRequestBodyWithLimit(c, defaultMaxRequestBodyBytes)
}

// ReadRequestBody reads an incoming request using this handler instance's
// hot-reloadable request body limit.
func (h *BaseAPIHandler) ReadRequestBody(c *gin.Context) ([]byte, error) {
	limit := defaultMaxRequestBodyBytes
	if h != nil {
		limit = h.Cfg.EffectiveAPIRequestBodyMaxBytes()
	}
	return readRequestBodyWithLimit(c, limit)
}

func readRequestBodyWithLimit(c *gin.Context, limit int64) ([]byte, error) {
	if c == nil || c.Request == nil || c.Request.Body == nil {
		return nil, nil
	}

	if limit <= 0 {
		limit = defaultMaxRequestBodyBytes
	}

	raw, err := readAllRequestBodyWithLimit(c.Request.Body, limit)
	if err != nil {
		return nil, err
	}

	encoding := ""
	if c != nil && c.Request != nil {
		encoding = strings.TrimSpace(c.Request.Header.Get("Content-Encoding"))
	}
	if encoding == "" || strings.EqualFold(encoding, "identity") {
		return raw, nil
	}

	decoded, err := decodeRequestBodyWithLimit(raw, encoding, limit)
	if err != nil {
		if !errors.Is(err, ErrRequestBodyTooLarge) && json.Valid(raw) {
			return raw, nil
		}
		return nil, err
	}
	return decoded, nil
}

func decodeRequestBody(raw []byte, encoding string) ([]byte, error) {
	return decodeRequestBodyWithLimit(raw, encoding, defaultMaxRequestBodyBytes)
}

func decodeRequestBodyWithLimit(raw []byte, encoding string, limit int64) ([]byte, error) {
	parts := strings.Split(encoding, ",")
	body := raw
	for i := len(parts) - 1; i >= 0; i-- {
		enc := strings.ToLower(strings.TrimSpace(parts[i]))
		switch enc {
		case "", "identity":
			continue
		case "zstd":
			decoded, err := decodeZstdRequestBodyWithLimit(body, limit)
			if err != nil {
				return nil, err
			}
			body = decoded
		default:
			return nil, fmt.Errorf("unsupported request content encoding: %s", enc)
		}
	}
	return body, nil
}

func decodeZstdRequestBody(raw []byte) ([]byte, error) {
	return decodeZstdRequestBodyWithLimit(raw, defaultMaxRequestBodyBytes)
}

func decodeZstdRequestBodyWithLimit(raw []byte, limit int64) ([]byte, error) {
	decoderMemoryLimit := limit
	if decoderMemoryLimit < 1<<20 {
		decoderMemoryLimit = 1 << 20
	}
	decoder, err := zstd.NewReader(
		bytes.NewReader(raw),
		zstd.WithDecoderConcurrency(1),
		zstd.WithDecoderLowmem(true),
		zstd.WithDecoderMaxMemory(uint64(decoderMemoryLimit)),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create zstd request decoder: %w", err)
	}
	defer decoder.Close()

	decoded, err := readAllRequestBodyWithLimit(decoder, limit)
	if err != nil {
		if errors.Is(err, ErrRequestBodyTooLarge) {
			return nil, err
		}
		return nil, fmt.Errorf("failed to decode zstd request body: %w", err)
	}
	return decoded, nil
}

func readAllRequestBodyWithLimit(reader io.Reader, limit int64) ([]byte, error) {
	if reader == nil {
		return nil, nil
	}
	if limit < 0 {
		limit = 0
	}

	readLimit := limit
	if readLimit < 1<<63-1 {
		readLimit++
	}
	body, err := io.ReadAll(io.LimitReader(reader, readLimit))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%w: limit is %d bytes", ErrRequestBodyTooLarge, limit)
	}
	return body, nil
}

// RequestBodyStatusCode maps request-body read failures to their HTTP status.
func RequestBodyStatusCode(err error) int {
	if errors.Is(err, ErrRequestBodyTooLarge) {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusBadRequest
}
