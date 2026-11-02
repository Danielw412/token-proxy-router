//go:build linux

package tailscale

import (
	"net/http"
	"os"
)

// identitySupported reports whether this OS can prove who owns a connection.
var identitySupported = true

// identityUnsupportedReason is shown when identitySupported is false.
const identityUnsupportedReason = ""

// defaultPeerTrusted accepts a connection owned by root, which is how tailscaled
// runs, or by the user running this server, who can already read its keys. Any other
// local user is refused.
func defaultPeerTrusted(r *http.Request) bool {
	peer, server, ok := requestAddrs(r)
	if !ok {
		return false
	}
	self := uint32(os.Getuid())
	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if uid, found := peerUIDFromTable(string(raw), peer, server); found {
			return uid == 0 || uid == self
		}
	}
	return false
}
