// Package webui serves the management panel compiled into the binary.
//
// `make panel` builds panel/ and copies the result to dist/management.html. A
// build without it still works and answers with a short page saying so. Nothing
// here ever fetches a panel at runtime.
package webui

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

const fileName = "management.html"

//go:embed all:dist
var dist embed.FS

const notBuiltPage = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Token Proxy Router</title>
<style>body{font:16px/1.5 system-ui,sans-serif;max-width:36rem;margin:4rem auto;padding:0 1rem}code{background:#8882;padding:.1em .3em;border-radius:3px}</style>
</head><body>
<h1>Token Proxy Router</h1>
<p>The server is running, but this binary was built without the management panel.</p>
<p>From a source checkout, run <code>make panel</code> and rebuild with <code>make build</code>. Release binaries already include it.</p>
</body></html>
`

var (
	loadOnce sync.Once
	page     []byte
	built    bool
	etag     string
)

func load() {
	loadOnce.Do(func() {
		raw, err := dist.ReadFile("dist/" + fileName)
		if err == nil && len(raw) > 0 {
			page, built = raw, true
		} else {
			page = []byte(notBuiltPage)
		}
		sum := sha256.Sum256(page)
		etag = `"` + hex.EncodeToString(sum[:8]) + `"`
	})
}

// Built reports whether the real panel is compiled in.
func Built() bool {
	load()
	return built
}

// Serve answers a panel request. It returns false only when the operator set
// MANAGEMENT_STATIC_PATH, upstream's explicit "serve this file" override, so a
// panel developer can test a local build. That file is never overwritten.
func Serve(c *gin.Context) bool {
	if strings.TrimSpace(os.Getenv("MANAGEMENT_STATIC_PATH")) != "" {
		return false
	}
	load()
	c.Header("Cache-Control", "no-cache")
	c.Header("ETag", etag)
	if c.GetHeader("If-None-Match") == etag {
		c.Status(http.StatusNotModified)
		c.Writer.WriteHeaderNow()
		return true
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", page)
	return true
}
