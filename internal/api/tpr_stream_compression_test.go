package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/httpwire"
)

// shippedCompressEventStreams records the value TPR ships, before TestMain changes it.
var shippedCompressEventStreams = tprCompressEventStreams

// TestMain lets upstream's compression tests keep exercising compressed event streams.
func TestMain(m *testing.M) {
	tprCompressEventStreams = true
	os.Exit(m.Run())
}

func TestTPREventStreamsAreNotCompressed(t *testing.T) {
	if shippedCompressEventStreams {
		t.Fatal("TPR must ship with event stream compression off")
	}
	tprCompressEventStreams = false
	defer func() { tprCompressEventStreams = true }()

	stream := strings.Repeat("event: content_block_delta\ndata: {\"type\":\"content_block_delta\"}\n\n", 64)
	json := "{\"message\":\"" + strings.Repeat("compressible text ", 128) + "\"}"
	engine := gin.New()
	engine.Use(responseCompressionMiddleware(&config.Config{}))
	engine.GET("/stream", func(c *gin.Context) {
		c.Header("Content-Type", "text/event-stream; charset=utf-8")
		for _, event := range strings.SplitAfter(stream, "\n\n") {
			if _, errWrite := io.WriteString(c.Writer, event); errWrite != nil {
				return
			}
			if errFlush := httpwire.FlushResponse(c.Writer); errFlush != nil {
				return
			}
		}
	})
	engine.GET("/json", func(c *gin.Context) {
		c.Header("Content-Type", "application/json")
		_, _ = io.WriteString(c.Writer, json)
	})

	// Bun's fetch sends this header, and the stalled Claude Code streams negotiated zstd.
	const bunAccept = "gzip, deflate, br, zstd"
	for _, tc := range []struct {
		path, encoding, body string
	}{
		{path: "/stream", encoding: "", body: stream},
		{path: "/json", encoding: "zstd", body: json},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		req.Header.Set("Accept-Encoding", bunAccept)
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, req)
		if got := response.Header().Get("Content-Encoding"); got != tc.encoding {
			t.Fatalf("%s: Content-Encoding = %q, want %q", tc.path, got, tc.encoding)
		}
		if got := string(compressionTestBody(t, tc.encoding, response.Body.Bytes())); got != tc.body {
			t.Fatalf("%s: body differs: got %d bytes, want %d", tc.path, len(got), len(tc.body))
		}
		if !response.Flushed && tc.path == "/stream" {
			t.Fatal("/stream: events were not flushed")
		}
	}
}
