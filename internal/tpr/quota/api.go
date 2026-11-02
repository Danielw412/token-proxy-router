package quota

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr"
)

const (
	defaultSpan = 7 * 24 * time.Hour
	maxSpan     = 366 * 24 * time.Hour
)

func init() {
	tpr.AddRoutes(registerRoutes)
}

func registerRoutes(group *gin.RouterGroup) {
	g := group.Group("/quota")
	g.GET("/pools", handlePools)
	// A wildcard rather than :id, because an auth ID may contain a slash.
	g.GET("/accounts/*rest", handleHistory)
	g.POST("/refresh", handleRefresh)
}

func running(c *gin.Context) *Service {
	s := Current()
	if s == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "quota history is not running"})
	}
	return s
}

func badRequest(c *gin.Context, msg string) {
	c.JSON(http.StatusBadRequest, gin.H{"error": msg})
}

// parseRange reads from and to, defaulting to a week either side of now for a
// chart, and rejecting spans too long to draw.
func parseRange(c *gin.Context, now time.Time) (from, to time.Time, ok bool) {
	from, to = now.Add(-defaultSpan), now.Add(defaultSpan)
	if raw := strings.TrimSpace(c.Query("from")); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			badRequest(c, "from must be an RFC 3339 time")
			return from, to, false
		}
		from = parsed
	}
	if raw := strings.TrimSpace(c.Query("to")); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			badRequest(c, "to must be an RFC 3339 time")
			return from, to, false
		}
		to = parsed
	}
	if !to.After(from) {
		badRequest(c, "to must be after from")
		return from, to, false
	}
	if to.Sub(from) > maxSpan {
		badRequest(c, "the range can be at most 366 days")
		return from, to, false
	}
	return from, to, true
}

func handlePools(c *gin.Context) {
	s := running(c)
	if s == nil {
		return
	}
	now := s.now()
	from, to, ok := parseRange(c, now)
	if !ok {
		return
	}
	burn := 3 * time.Hour
	switch strings.ToLower(strings.TrimSpace(c.Query("burn"))) {
	case "", "3h":
	case "1h":
		burn = time.Hour
	case "6h":
		burn = 6 * time.Hour
	default:
		badRequest(c, "burn must be 1h, 3h or 6h")
		return
	}
	window, okWindow := normalizeWindow(c.Query("window"))
	if !okWindow {
		badRequest(c, "window must be weekly or session")
		return
	}
	future := strings.ToLower(strings.TrimSpace(c.Query("future")))
	switch future {
	case "":
		future = FutureSame
	case FutureSame, FutureIdle, FutureDouble:
	default:
		badRequest(c, "future must be same, idle or double")
		return
	}
	pools, at, err := s.Pools(Query{From: from, To: to, Burn: burn, Window: window, Future: future})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read quota history"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"now": at, "from": from, "to": to, "pools": pools})
}

func handleHistory(c *gin.Context) {
	s := running(c)
	if s == nil {
		return
	}
	rest := strings.TrimPrefix(c.Param("rest"), "/")
	id, found := strings.CutSuffix(rest, "/history")
	if !found || id == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	now := s.now()
	from, to, ok := parseRange(c, now)
	if !ok {
		return
	}
	if c.Query("to") == "" {
		// A history ends at now unless asked otherwise, unlike a chart's range.
		to = now
	}
	window, okWindow := normalizeWindow(c.Query("window"))
	if !okWindow {
		badRequest(c, "window must be weekly or session")
		return
	}
	history, err := s.History(id, window, from, to)
	if errors.Is(err, ErrUnknownAccount) {
		c.JSON(http.StatusNotFound, gin.H{"error": "unknown account"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read quota history"})
		return
	}
	c.JSON(http.StatusOK, history)
}

func handleRefresh(c *gin.Context) {
	s := running(c)
	if s == nil {
		return
	}
	if err := s.Refresh(); err != nil {
		var soon ErrRefreshTooSoon
		if errors.As(err, &soon) {
			c.Header("Retry-After", strconv.Itoa(int(soon.RetryAfter.Seconds())+1))
			c.JSON(http.StatusTooManyRequests, gin.H{"error": soon.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not start a refresh"})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"requested": true})
}
