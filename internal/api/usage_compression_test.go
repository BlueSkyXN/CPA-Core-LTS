package api

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
)

func TestUsageEncoding(t *testing.T) {
	for _, tt := range []struct {
		header, want string
		identity     bool
	}{
		{"", "", true}, {"identity", "", true}, {"gzip, br, zstd", "br", true},
		{"br;q=0.2,zstd;q=0.8,gzip;q=0.7", "zstd", true},
		{"br;q=0,zstd;q=0,gzip;q=1", "gzip", true},
		{"*;q=0", "", false}, {"*;q=1,br;q=0", "zstd", true},
		{"gzip;q=0,identity;q=0", "", false}, {"gzip;q=0.3,identity;q=1", "", true},
		{"gzip,identity;q=0", "gzip", false}, {"br;q=NaN,gzip;q=0.5", "gzip", true},
		{"gzip;q=0,gzip;q=1", "", true}, {"br;q=2,gzip", "gzip", true},
	} {
		t.Run(tt.header, func(t *testing.T) {
			got, identity := usageEncoding(tt.header)
			if got != tt.want || identity != tt.identity {
				t.Fatalf("got %q/%v want %q/%v", got, identity, tt.want, tt.identity)
			}
		})
	}
}

func decodeUsage(t *testing.T, encoding string, data []byte) []byte {
	t.Helper()
	var reader io.Reader = bytes.NewReader(data)
	switch encoding {
	case "br":
		reader = brotli.NewReader(reader)
	case "gzip":
		r, err := gzip.NewReader(reader)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		reader = r
	case "zstd":
		r, err := zstd.NewReader(reader)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		reader = r
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestUsageCompression(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, encoding := range []string{"br", "zstd", "gzip", "identity"} {
		for _, size := range []int{10, 1023, 1024, 128 * 1024} {
			for _, chunked := range []bool{false, true} {
				t.Run(encoding+"/"+strconv.Itoa(size)+"/"+strconv.FormatBool(chunked), func(t *testing.T) {
					body := []byte(`{"data":"` + strings.Repeat("x", size) + `"}`)
					r := gin.New()
					r.POST("/usage", usageCompression(), func(c *gin.Context) {
						input, _ := io.ReadAll(c.Request.Body)
						if string(input) != "test-input" || c.Query("q") != "kept" {
							t.Error("request changed")
						}
						c.Header("Content-Type", "application/json")
						c.Header("Content-Length", strconv.Itoa(len(body)))
						c.Status(201)
						if chunked {
							for _, b := range body {
								_, _ = c.Writer.Write([]byte{b})
							}
						} else {
							_, _ = c.Writer.Write(body)
						}
					})
					req := httptest.NewRequest("POST", "/usage?q=kept", strings.NewReader("test-input"))
					req.Header.Set("Accept-Encoding", encoding)
					w := httptest.NewRecorder()
					r.ServeHTTP(w, req)
					wantEncoding := encoding
					if len(body) < usageCompressionThreshold || encoding == "identity" {
						wantEncoding = ""
					}
					if w.Code != 201 || w.Header().Get("Content-Encoding") != wantEncoding {
						t.Fatalf("response %d %v", w.Code, w.Header())
					}
					if wantEncoding != "" && w.Header().Get("Content-Length") != "" {
						t.Fatal("stale Content-Length")
					}
					if !bytes.Equal(decodeUsage(t, wantEncoding, w.Body.Bytes()), body) {
						t.Fatal("JSON differs")
					}
					if !strings.Contains(w.Header().Get("Vary"), "Accept-Encoding") {
						t.Fatal("missing Vary")
					}
				})
			}
		}
	}
}

func TestUsageCompressionBoundaries(t *testing.T) {
	for _, tt := range []struct {
		name, accept, contentType, preencoded string
		status                                int
		body                                  string
		want                                  string
	}{
		{"small-required", "gzip,identity;q=0", "application/json", "", 200, "{}", "gzip"},
		{"error", "br,identity;q=0", "application/json", "", 400, `{"error":"invalid"}`, "br"},
		{"preencoded", "br", "application/json", "gzip", 200, strings.Repeat("x", 2048), "gzip"},
		{"stream", "gzip", "text/event-stream", "", 200, strings.Repeat("x", 2048), ""},
		{"empty", "gzip,identity;q=0", "application/json", "", 204, "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := gin.New()
			r.GET("/usage", usageCompression(), func(c *gin.Context) {
				c.Header("Content-Type", tt.contentType)
				c.Header("Content-Encoding", tt.preencoded)
				c.Status(tt.status)
				_, _ = c.Writer.WriteString(tt.body)
			})
			req := httptest.NewRequest("GET", "/usage", nil)
			req.Header.Set("Accept-Encoding", tt.accept)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tt.status || w.Header().Get("Content-Encoding") != tt.want {
				t.Fatalf("bad response %d %v", w.Code, w.Header())
			}
			if tt.preencoded == "" && tt.status != 204 && string(decodeUsage(t, tt.want, w.Body.Bytes())) != tt.body {
				t.Fatal("body changed")
			}
		})
	}
	t.Run("unacceptable-before-mutation", func(t *testing.T) {
		r := gin.New()
		r.POST("/usage/import", usageCompression(), func(c *gin.Context) { t.Error("handler must not run") })
		req := httptest.NewRequest("POST", "/usage/import", nil)
		req.Header.Set("Accept-Encoding", "*;q=0")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusNotAcceptable {
			t.Fatal(w.Code)
		}
	})
}
