package connect

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/setup"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/tailscale"
)

const (
	installBase    = "https://github.com/Danielw412/token-proxy-router/releases/latest/download"
	installLinux   = "curl -fsSL " + installBase + "/install.sh | sh"
	installWindows = "irm " + installBase + "/install.ps1 | iex"
)

// HubURL is one address clients can use.
type HubURL struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// KeyInfo describes a client key without revealing it.
type KeyInfo struct {
	KeyID  string `json:"key_id"`
	Name   string `json:"name"`
	Masked string `json:"masked"`
}

// Snippets are the ready-to-paste setup instructions for one key.
type Snippets struct {
	Linux            string `json:"linux"`
	Windows          string `json:"windows"`
	CodexTOML        string `json:"codex_toml"`
	ClaudeEnv        string `json:"claude_env"`
	ClaudeEnvWindows string `json:"claude_env_windows"`
	T3               string `json:"t3"`
}

// KeyID is the stable public identifier of a client key.
func KeyID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:12]
}

// MaskKey shows just enough of a key to recognise it.
func MaskKey(key string) string {
	if len(key) <= 8 {
		return "sk-...****"
	}
	return "sk-..." + key[len(key)-4:]
}

// BuildSnippets returns the commands and config for a hub and key.
func BuildSnippets(hub, key string) (Snippets, error) {
	hub, err := NormalizeHub(hub)
	if err != nil {
		return Snippets{}, err
	}
	if err := ValidateKey(key); err != nil {
		return Snippets{}, err
	}
	// Both values match safeToken, so they need no further quoting in a shell.
	args := "connect --hub " + hub + " --key " + key
	return Snippets{
		Linux:            installLinux + " -s -- " + args,
		Windows:          "$env:TPR_INSTALL_ARGS='" + args + "'; " + installWindows,
		CodexTOML:        CodexTOML(hub),
		ClaudeEnv:        fmt.Sprintf("export %s=%s\nexport %s=%s\n", claudeBaseURL, hub, claudeToken, key),
		ClaudeEnvWindows: fmt.Sprintf("$env:%s='%s'\n$env:%s='%s'\n", claudeBaseURL, hub, claudeToken, key),
		T3:               "tpr connect --hub " + hub + " --key " + key + " --t3",
	}, nil
}

func hubURLs(ctx context.Context, info setup.ServerInfo) []HubURL {
	urls := []HubURL{}
	if tailnet := tailscale.HubURL(ctx, info); tailnet != "" {
		urls = append(urls, HubURL{Label: "Tailnet", URL: tailnet})
	}
	return append(urls, HubURL{Label: "This machine", URL: info.LocalURL()})
}

func keyInfos(keys []string) []KeyInfo {
	infos := make([]KeyInfo, 0, len(keys))
	for i, key := range keys {
		infos = append(infos, KeyInfo{KeyID: KeyID(key), Name: fmt.Sprintf("Key %d", i+1), Masked: MaskKey(key)})
	}
	return infos
}

func modelIDs(provider string) []string {
	ids := []string{}
	for _, model := range registry.GetGlobalRegistry().GetAvailableModelsByProvider(provider) {
		if model != nil && model.ID != "" {
			ids = append(ids, model.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

func init() {
	tpr.AddRoutes(func(group *gin.RouterGroup) {
		group.GET("/connect", handleConnect)
		group.POST("/connect/snippets", handleSnippets)
	})
}

func handleConnect(c *gin.Context) {
	info, err := setup.LoadServerInfo(setup.ActiveConfigPath())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read the config: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"hub_urls": hubURLs(c.Request.Context(), info),
		"keys":     keyInfos(info.APIKeys),
		"models":   gin.H{"codex": modelIDs("codex"), "claude": modelIDs("claude")},
		"install":  gin.H{"linux": installLinux, "windows": installWindows},
	})
}

func handleSnippets(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	var body struct {
		KeyID  string `json:"key_id"`
		HubURL string `json:"hub_url"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": `body must be JSON like {"key_id":"...","hub_url":"..."}`})
		return
	}
	info, err := setup.LoadServerInfo(setup.ActiveConfigPath())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read the config: " + err.Error()})
		return
	}
	key := ""
	for _, candidate := range info.APIKeys {
		if KeyID(candidate) == body.KeyID {
			key = candidate
			break
		}
	}
	if key == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "no client key has that key_id"})
		return
	}
	hub := body.HubURL
	if hub == "" {
		hub = hubURLs(c.Request.Context(), info)[0].URL
	}
	snippets, err := BuildSnippets(hub, key)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, snippets)
}
