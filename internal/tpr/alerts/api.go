package alerts

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr"
)

func init() {
	tpr.AddRoutes(registerRoutes)
}

func registerRoutes(group *gin.RouterGroup) {
	g := group.Group("/alerts")
	g.GET("", handleGet)
	g.PUT("", handlePut)
	g.POST("/test", handleTest)
	g.GET("/events", handleEvents)
}

func running(c *gin.Context) *Service {
	s := Current()
	if s == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "alerts are not running"})
	}
	return s
}

func handleGet(c *gin.Context) {
	if s := running(c); s != nil {
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, s.View())
	}
}

func handlePut(c *gin.Context) {
	s := running(c)
	if s == nil {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, 64<<10))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "could not read the request body"})
		return
	}
	next := DefaultSettings()
	if errDecode := json.Unmarshal(raw, &next); errDecode != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON body"})
		return
	}
	view, errUpdate := s.Update(next)
	if errUpdate != nil {
		if errors.Is(errUpdate, ErrInvalidSettings) {
			c.JSON(http.StatusBadRequest, gin.H{"error": errUpdate.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not save the settings"})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, view)
}

func handleTest(c *gin.Context) {
	if s := running(c); s != nil {
		c.JSON(http.StatusOK, gin.H{"results": s.SendTest(c.Request.Context())})
	}
}

func handleEvents(c *gin.Context) {
	s := running(c)
	if s == nil {
		return
	}
	limit := 50
	if v, err := strconv.Atoi(c.Query("limit")); err == nil && v > 0 {
		limit = v
	}
	if limit > maxEventsRead {
		limit = maxEventsRead
	}
	events, err := s.Events(limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read the events"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"events": events, "active": s.View().Active})
}
