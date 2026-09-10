package api

import (
	"compress/gzip"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/andybalholm/brotli"
	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
)

const usageCompressionThreshold = 1024

// 同权重优先 br、zstd、gzip；identity 未显式指定时作为兼容回退。
func usageEncoding(header string) (encoding string, identity bool) {
	weights := map[string]float64{}
	for _, item := range strings.Split(header, ",") {
		parts := strings.Split(item, ";")
		name := strings.ToLower(strings.TrimSpace(parts[0]))
		if name == "" {
			continue
		}
		q := 1.0
		for _, parameter := range parts[1:] {
			key, value, ok := strings.Cut(strings.TrimSpace(parameter), "=")
			if !ok || !strings.EqualFold(key, "q") {
				q = 0
				break
			}
			parsed, err := strconv.ParseFloat(value, 64)
			if err != nil || math.IsNaN(parsed) || parsed < 0 || parsed > 1 {
				q = 0
				break
			}
			q = parsed
		}
		// 重复项从严处理，不能覆盖明确的 q=0。
		if old, ok := weights[name]; !ok || q < old {
			weights[name] = q
		}
	}
	identityQ, explicitIdentity := weights["identity"]
	if !explicitIdentity {
		identityQ = 1
		if wildcard, ok := weights["*"]; ok && wildcard == 0 {
			identityQ = 0
		}
	}
	identity = identityQ > 0
	best := 0.0
	for _, name := range []string{"br", "zstd", "gzip"} {
		q, ok := weights[name]
		if !ok {
			q = weights["*"]
		}
		if q > best {
			best, encoding = q, name
		}
	}
	if explicitIdentity && identityQ > best {
		encoding = ""
	}
	return encoding, identity
}

// 只挂到 usage JSON 路由，鉴权先执行；不涉及 queue、SSE 或 WebSocket。
func usageCompression() gin.HandlerFunc {
	return func(c *gin.Context) {
		encoding, identity := usageEncoding(strings.Join(c.Request.Header.Values("Accept-Encoding"), ","))
		c.Header("Vary", appendUsageVary(c.Writer.Header().Values("Vary")))
		if encoding == "" {
			if !identity {
				c.AbortWithStatus(http.StatusNotAcceptable)
				return
			}
			c.Next()
			return
		}
		original := c.Writer
		writer := &usageCompressionWriter{ResponseWriter: original, encoding: encoding, identity: identity}
		c.Writer = writer
		defer func() { c.Writer = original }()
		c.Next()
		writer.start(false)
		if writer.compressor != nil {
			if err := writer.compressor.Close(); err != nil {
				_ = c.Error(err)
			}
		}
	}
}

func appendUsageVary(values []string) string {
	for _, value := range values {
		for _, field := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(field), "Accept-Encoding") || strings.TrimSpace(field) == "*" {
				return strings.Join(values, ", ")
			}
		}
	}
	return strings.Join(append(values, "Accept-Encoding"), ", ")
}

type usageCompressionWriter struct {
	gin.ResponseWriter
	encoding   string
	identity   bool
	pending    []byte
	started    bool
	compressor io.WriteCloser
	err        error
}

func (w *usageCompressionWriter) start(large bool) {
	if w.started {
		return
	}
	w.started = true
	status := w.Status()
	jsonResponse := strings.HasPrefix(strings.ToLower(w.Header().Get("Content-Type")), "application/json")
	if jsonResponse && status >= 200 && status != 204 && status != 304 && w.Header().Get("Content-Encoding") == "" && (large || !w.identity) {
		switch w.encoding {
		case "br":
			w.compressor = brotli.NewWriterLevel(w.ResponseWriter, 4)
		case "zstd":
			w.compressor, w.err = zstd.NewWriter(w.ResponseWriter, zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(1)), zstd.WithEncoderConcurrency(1))
		case "gzip":
			w.compressor, w.err = gzip.NewWriterLevel(w.ResponseWriter, 3)
		}
		if w.compressor != nil {
			w.Header().Del("Content-Length")
			w.Header().Set("Content-Encoding", w.encoding)
		}
	}
	if len(w.pending) > 0 {
		_, w.err = w.write(w.pending)
		w.pending = nil
	}
}

func (w *usageCompressionWriter) write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if w.compressor != nil {
		return w.compressor.Write(p)
	}
	return w.ResponseWriter.Write(p)
}

func (w *usageCompressionWriter) Write(p []byte) (int, error) {
	if !w.started {
		if len(w.pending)+len(p) < usageCompressionThreshold {
			w.pending = append(w.pending, p...)
			return len(p), nil
		}
		w.start(true)
	}
	return w.write(p)
}

func (w *usageCompressionWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }
func (w *usageCompressionWriter) WriteHeaderNow()                   { w.start(false); w.ResponseWriter.WriteHeaderNow() }
func (w *usageCompressionWriter) Flush() {
	w.start(false)
	if flusher, ok := w.compressor.(interface{ Flush() error }); ok {
		if err := flusher.Flush(); err != nil {
			w.err = err
		}
	}
	w.ResponseWriter.Flush()
}
