package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func serve(t *testing.T, header http.Header) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/management.html", func(c *gin.Context) {
		if !Serve(c) {
			c.String(http.StatusTeapot, "fell through")
		}
	})
	req := httptest.NewRequest(http.MethodGet, "/management.html", nil)
	for name, values := range header {
		req.Header[name] = values
	}
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)
	return rr
}

func TestServeReturnsPanelOrBuildHint(t *testing.T) {
	t.Setenv("MANAGEMENT_STATIC_PATH", "")
	rr := serve(t, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if !strings.HasPrefix(rr.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("content type = %q", rr.Header().Get("Content-Type"))
	}
	if !Built() && !strings.Contains(rr.Body.String(), "make panel") {
		t.Fatalf("fallback page should explain the fix: %s", rr.Body.String())
	}
}

func TestServeHonoursConditionalRequests(t *testing.T) {
	t.Setenv("MANAGEMENT_STATIC_PATH", "")
	first := serve(t, nil)
	tag := first.Header().Get("ETag")
	if tag == "" {
		t.Fatal("missing etag")
	}
	second := serve(t, http.Header{"If-None-Match": {tag}})
	if second.Code != http.StatusNotModified {
		t.Fatalf("status = %d", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Fatal("304 must have no body")
	}
}

func TestServeLeavesExplicitOverrideToUpstream(t *testing.T) {
	t.Setenv("MANAGEMENT_STATIC_PATH", t.TempDir())
	if rr := serve(t, nil); rr.Code != http.StatusTeapot {
		t.Fatalf("status = %d", rr.Code)
	}
}
