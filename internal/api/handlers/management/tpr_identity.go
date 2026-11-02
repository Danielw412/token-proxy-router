package management

import (
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/tailscale"
)

// tprIdentityAllowed is the TPR hook used by Middleware. See internal/tpr/tailscale.
func (h *Handler) tprIdentityAllowed(c *gin.Context) bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	host := ""
	if h.cfg != nil {
		host = h.cfg.Host
	}
	h.mu.Unlock()
	return tailscale.CheckIdentity(c.Request, host)
}
