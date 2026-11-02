package update

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/service"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/setup"
)

const (
	checkMaxAge     = 6 * time.Hour
	serviceCacheTTL = 5 * time.Second
	// stuckAfter is longer than any healthy update takes, so a state this old means
	// the process doing the update died.
	stuckAfter = 20 * time.Minute
)

// CheckCache is the remembered result of the last release check.
type CheckCache struct {
	Latest    string `json:"latest,omitempty"`
	NotesURL  string `json:"notes_url,omitempty"`
	Notes     string `json:"notes,omitempty"`
	CheckedAt string `json:"checked_at,omitempty"`
	ETag      string `json:"etag,omitempty"`
	// LastError keeps a failed check visible without losing the last good answer.
	LastError string `json:"last_error,omitempty"`
}

func checkCachePath() string { return filepath.Join(home.DataDir(), "update-check.json") }

// LoadCheckCache returns the last release check.
func LoadCheckCache() CheckCache {
	raw, err := os.ReadFile(checkCachePath())
	if err != nil {
		return CheckCache{}
	}
	var cache CheckCache
	_ = json.Unmarshal(raw, &cache)
	return cache
}

func saveCheckCache(cache CheckCache) {
	raw, err := json.Marshal(cache)
	if err == nil {
		_ = setup.AtomicWrite(checkCachePath(), raw, 0o600)
	}
}

// Check asks the release server for the latest version. A failure such as a rate
// limit keeps the last good answer and notes the error. Unchanged data costs no
// rate limit thanks to the ETag.
func Check(ctx context.Context, source *ReleaseSource) CheckCache {
	cache := LoadCheckCache()
	result, err := source.fetch(ctx, "latest", cache.ETag)
	now := time.Now().UTC().Format(time.RFC3339)
	if err != nil {
		if errors.Is(err, ErrNoRelease) {
			cache.Latest, cache.Notes, cache.NotesURL, cache.ETag = "", "", "", ""
			cache.CheckedAt = now
			cache.LastError = ""
		} else {
			cache.LastError = err.Error()
		}
		saveCheckCache(cache)
		return cache
	}
	cache.CheckedAt = now
	cache.LastError = ""
	if !result.notModified {
		cache.Latest = result.release.Tag
		cache.NotesURL = result.release.HTMLURL
		cache.Notes = result.release.Body
		cache.ETag = result.etag
	}
	saveCheckCache(cache)
	return cache
}

// Report is the GET /update response.
type Report struct {
	Current           string `json:"current"`
	Latest            string `json:"latest"`
	Available         bool   `json:"available"`
	NotesURL          string `json:"notes_url"`
	Notes             string `json:"notes"`
	CheckedAt         string `json:"checked_at"`
	CanApply          bool   `json:"can_apply"`
	Reason            string `json:"reason"`
	RollbackAvailable bool   `json:"rollback_available"`
	State             string `json:"state"`
	Error             string `json:"error"`
}

// BuildReport combines the cached check and the current update state.
func BuildReport(current string) Report {
	cache := LoadCheckCache()
	progress := reportedProgress()
	_, rollback := LoadPreviousInfo()
	report := Report{
		Current:           current,
		Latest:            cache.Latest,
		Available:         Newer(cache.Latest, current),
		NotesURL:          cache.NotesURL,
		Notes:             cache.Notes,
		CheckedAt:         cache.CheckedAt,
		RollbackAvailable: rollback,
		State:             progress.State,
		Error:             progress.Error,
	}
	if report.Error == "" {
		report.Error = cache.LastError
	}
	report.CanApply, report.Reason = canApply(current)
	return report
}

func reportedProgress() Progress {
	progress := LoadProgress()
	if progress.State == StateIdle || progress.State == StateFailed {
		return progress
	}
	updated, err := time.Parse(time.RFC3339, progress.UpdatedAt)
	if err == nil && time.Since(updated) > stuckAfter {
		return Progress{State: StateFailed, Error: "the update was interrupted. Check `tpr version`, then try again", From: progress.From, To: progress.To}
	}
	return progress
}

func canApply(current string) (bool, string) {
	if !service.Supervised() {
		return false, "The server is not running as a service, so it cannot restart itself. Run `tpr install`, or update with `tpr update` and restart by hand."
	}
	if !IsRelease(current) {
		return false, "This is a development build. Install a release to get updates."
	}
	target, err := TargetPath()
	if err != nil {
		return false, err.Error()
	}
	probe, err := os.CreateTemp(filepath.Dir(target), ".tpr-probe-*")
	if err != nil {
		return false, "The folder " + filepath.Dir(target) + " is not writable."
	}
	_ = probe.Close()
	_ = os.Remove(probe.Name())
	return true, ""
}

var (
	startTime   = time.Now()
	applyActive atomic.Bool

	serviceMu      sync.Mutex
	serviceStatus  service.Status
	serviceChecked time.Time
)

func cachedServiceStatus() service.Status {
	serviceMu.Lock()
	defer serviceMu.Unlock()
	if time.Since(serviceChecked) < serviceCacheTTL {
		return serviceStatus
	}
	serviceStatus = service.NewManager(io.Discard).Status()
	serviceChecked = time.Now()
	return serviceStatus
}

func init() {
	tpr.AddRoutes(func(group *gin.RouterGroup) {
		group.GET("/system", handleSystem)
		group.GET("/update", handleGetUpdate)
		group.POST("/update/check", handleCheck)
		group.POST("/update/apply", handleApply)
		group.POST("/update/rollback", handleRollback)
	})
}

func handleSystem(c *gin.Context) {
	status := cachedServiceStatus()
	c.JSON(http.StatusOK, gin.H{
		"version":          buildinfo.Version,
		"commit":           buildinfo.Commit,
		"upstream_version": UpstreamVersion,
		"panel_version":    PanelVersion,
		"data_dir":         home.Dir(),
		"os":               runtime.GOOS,
		"arch":             runtime.GOARCH,
		"uptime_seconds":   int64(time.Since(startTime).Seconds()),
		"service": gin.H{
			"kind":       status.Kind,
			"installed":  status.Installed,
			"running":    status.Running,
			"supervised": service.Supervised(),
		},
		"setup": gin.H{"completed": setup.LoadState().Completed},
	})
}

func handleGetUpdate(c *gin.Context) {
	// Serve the cache right away and refresh behind the response when it is stale.
	cache := LoadCheckCache()
	if stale(cache) {
		refreshInBackground()
	}
	c.JSON(http.StatusOK, BuildReport(buildinfo.Version))
}

func stale(cache CheckCache) bool {
	checked, err := time.Parse(time.RFC3339, cache.CheckedAt)
	return err != nil || time.Since(checked) > checkMaxAge
}

var refreshActive atomic.Bool

func refreshInBackground() {
	if !refreshActive.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer refreshActive.Store(false)
		Check(context.Background(), NewReleaseSource())
	}()
}

func handleCheck(c *gin.Context) {
	Check(c.Request.Context(), NewReleaseSource())
	c.JSON(http.StatusOK, BuildReport(buildinfo.Version))
}

func handleApply(c *gin.Context) {
	var body struct {
		Version string `json:"version"`
	}
	if err := c.ShouldBindJSON(&body); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "body must be JSON like {\"version\":\"v1.2.3\"}"})
		return
	}
	version := strings.TrimSpace(body.Version)
	if version != "" && !IsRelease(version) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "version must look like vX.Y.Z"})
		return
	}
	if ok, reason := canApply(buildinfo.Version); !ok && version == "" {
		c.JSON(http.StatusConflict, gin.H{"error": reason})
		return
	}
	if !service.Supervised() {
		c.JSON(http.StatusConflict, gin.H{"error": "The server is not running as a service, so it cannot restart itself. Update with `tpr update` instead."})
		return
	}
	updater, err := New(buildinfo.Version)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !applyActive.CompareAndSwap(false, true) {
		c.JSON(http.StatusConflict, gin.H{"error": "an update is already running"})
		return
	}
	saveProgress(Progress{State: StateDownloading, From: buildinfo.Version})
	go func() {
		defer applyActive.Store(false)
		// The request has been answered; the update must outlive it.
		_, _ = updater.Apply(context.Background(), ApplyOptions{ConfigPath: setup.ActiveConfigPath(), Version: version})
	}()
	c.JSON(http.StatusAccepted, BuildReport(buildinfo.Version))
}

func handleRollback(c *gin.Context) {
	if !service.Supervised() {
		c.JSON(http.StatusConflict, gin.H{"error": "The server is not running as a service, so it cannot restart itself. Roll back with `tpr update --rollback` instead."})
		return
	}
	updater, err := New(buildinfo.Version)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !applyActive.CompareAndSwap(false, true) {
		c.JSON(http.StatusConflict, gin.H{"error": "an update is already running"})
		return
	}
	defer applyActive.Store(false)
	if _, err := updater.Rollback(); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, BuildReport(buildinfo.Version))
}
