package cliproxy

import (
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// TPR: this file gives Token Proxy Router's startup hook in internal/cmd what it
// needs from a built service. See docs/upstream-sync.md.

// TPRAttachment returns the service's auth manager and a reader for its current
// configuration. The manager is nil when this service delegates to Home, where the
// router does not apply.
func (s *Service) TPRAttachment() (*coreauth.Manager, func() *config.Config) {
	if s == nil {
		return nil, func() *config.Config { return nil }
	}
	if cfg := s.currentConfig(); cfg != nil && cfg.Home.Enabled {
		return nil, s.currentConfig
	}
	return s.coreManager, s.currentConfig
}
