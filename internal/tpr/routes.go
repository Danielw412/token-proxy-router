// Package tpr is the root of Token Proxy Router's additions to the upstream
// CLIProxyAPI server. Keeping them under internal/tpr, with only small hooks in
// upstream files, is what keeps upstream merges cheap. See docs/upstream-sync.md.
package tpr

import (
	"io"
	"sync"

	"github.com/gin-gonic/gin"
)

// RouteFunc attaches routes to the authenticated /v8/management/tpr group.
type RouteFunc func(group *gin.RouterGroup)

var (
	routesMu sync.Mutex
	routes   []RouteFunc
)

// AddRoutes registers management routes. Feature packages call it from init(),
// so handlers must answer 503 until their feature has started.
func AddRoutes(fn RouteFunc) {
	routesMu.Lock()
	defer routesMu.Unlock()
	routes = append(routes, fn)
}

// Mount attaches every registered feature to the given group. Every route here
// inherits the management key check from the group's middleware.
func Mount(group *gin.RouterGroup) {
	routesMu.Lock()
	defer routesMu.Unlock()
	for _, fn := range routes {
		fn(group)
	}
}

// SnapshotFunc writes a consistent copy of one piece of live state.
type SnapshotFunc func(w io.Writer) error

var (
	snapshotsMu sync.Mutex
	snapshots   = map[string]SnapshotFunc{}
)

// AddSnapshot lets a feature contribute a file to backups. The name is the
// file's path inside the archive, relative to the data folder.
func AddSnapshot(name string, fn SnapshotFunc) {
	snapshotsMu.Lock()
	defer snapshotsMu.Unlock()
	snapshots[name] = fn
}

// Snapshots returns a copy of the registered snapshot functions.
func Snapshots() map[string]SnapshotFunc {
	snapshotsMu.Lock()
	defer snapshotsMu.Unlock()
	out := make(map[string]SnapshotFunc, len(snapshots))
	for name, fn := range snapshots {
		out[name] = fn
	}
	return out
}
