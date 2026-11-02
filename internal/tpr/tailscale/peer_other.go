//go:build !linux

package tailscale

import "net/http"

// identitySupported reports whether this OS can prove who owns a connection.
var identitySupported = false

// identityUnsupportedReason is shown when identitySupported is false.
const identityUnsupportedReason = "Identity sign-in is only available on Linux, where the server can check which user owns a connection."

// defaultPeerTrusted fails closed where a connection's owner can't be checked.
func defaultPeerTrusted(*http.Request) bool { return false }
