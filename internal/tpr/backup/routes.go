package backup

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/service"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/setup"
	log "github.com/sirupsen/logrus"
)

func init() {
	tpr.AddRoutes(func(group *gin.RouterGroup) {
		group.POST("/backup", handleBackup)
		group.POST("/restore", handleRestore)
	})
}

func liveOptions() Options {
	return Options{ConfigPath: setup.ActiveConfigPath(), Version: buildinfo.Version, LiveSnapshots: true}
}

func handleBackup(c *gin.Context) {
	var body struct {
		Passphrase string `json:"passphrase"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": `body must be JSON like {"passphrase":"..."}`})
		return
	}
	if err := CheckPassphrase(body.Passphrase); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	name := fmt.Sprintf("token-proxy-router-%s.tprbak", time.Now().UTC().Format("20060102-150405"))
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Disposition", `attachment; filename="`+name+`"`)
	c.Header("Cache-Control", "no-store")
	c.Status(http.StatusOK)
	if err := Write(c.Request.Context(), c.Writer, body.Passphrase, liveOptions()); err != nil {
		// Headers are gone by now. Cutting the connection makes the download fail
		// visibly, and the missing final chunk fails restore's integrity check anyway.
		log.WithError(err).Error("backup failed")
		c.Abort()
		if hijacker, ok := c.Writer.(http.Hijacker); ok {
			if conn, _, errHijack := hijacker.Hijack(); errHijack == nil {
				_ = conn.Close()
			}
		}
	}
}

func handleRestore(c *gin.Context) {
	passphrase := c.PostForm("passphrase")
	if passphrase == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the passphrase is required"})
		return
	}
	header, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "attach the backup as the multipart field \"file\""})
		return
	}
	file, err := header.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "could not read the upload"})
		return
	}
	defer func() { _ = file.Close() }()
	inspect, snapshot, err := Stage(c.Request.Context(), file, passphrase, liveOptions())
	if err != nil {
		status := http.StatusBadRequest
		if !errors.Is(err, ErrBadPassphrase) && !errors.Is(err, ErrCorrupt) {
			status = http.StatusUnprocessableEntity
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	restarting := false
	if errRestart := service.RequestRestart(); errRestart == nil {
		restarting = true
	}
	c.JSON(http.StatusAccepted, gin.H{
		"restarting":       restarting,
		"files":            inspect.Files,
		"snapshot":         snapshot,
		"created_at":       inspect.Manifest.CreatedAt,
		"restart_required": !restarting,
	})
}
