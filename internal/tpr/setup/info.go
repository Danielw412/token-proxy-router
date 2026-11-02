package setup

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"gopkg.in/yaml.v3"
)

// ServerInfo is the part of config.yaml that Token Proxy Router features read.
// It comes from the file on disk, so hot-reloaded edits are always reflected.
type ServerInfo struct {
	Host         string
	Port         int
	TLS          bool
	AuthDir      string
	APIKeys      []string
	HasMgmtKey   bool
	AllowRemote  bool
	ControlPanel bool
}

// LoadServerInfo reads a config file without changing it.
func LoadServerInfo(path string) (ServerInfo, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ServerInfo{}, err
	}
	cfg, err := config.ParseConfigBytes(raw)
	if err != nil {
		return ServerInfo{}, err
	}
	return ServerInfo{
		Host:         cfg.Host,
		Port:         cfg.Port,
		TLS:          cfg.TLS.Enable,
		AuthDir:      cfg.AuthDir,
		APIKeys:      append([]string(nil), cfg.APIKeys...),
		HasMgmtKey:   cfg.RemoteManagement.SecretKey != "",
		AllowRemote:  cfg.RemoteManagement.AllowRemote,
		ControlPanel: !cfg.RemoteManagement.DisableControlPanel,
	}, nil
}

// LocalURL returns the address a process on this machine uses to reach the server.
func (s ServerInfo) LocalURL() string {
	scheme := "http"
	if s.TLS {
		scheme = "https"
	}
	host := strings.TrimSpace(s.Host)
	switch {
	case host == "", host == "0.0.0.0", host == "::", host == "[::]":
		host = "127.0.0.1"
	case strings.Contains(host, ":") && !strings.HasPrefix(host, "["):
		host = "[" + host + "]"
	}
	return fmt.Sprintf("%s://%s:%d", scheme, host, s.Port)
}

// PanelURL returns the local management panel address.
func (s ServerInfo) PanelURL() string { return s.LocalURL() + "/management.html" }

// IsLoopbackHost reports whether a server bind address only listens on this
// machine. An empty host binds every interface, so it is not loopback.
func IsLoopbackHost(host string) bool {
	host = strings.TrimSpace(host)
	if strings.EqualFold(host, "localhost") {
		return true
	}
	addr, err := netip.ParseAddr(strings.Trim(host, "[]"))
	return err == nil && addr.Unmap().IsLoopback()
}

// PortOpen reports whether something accepts TCP connections on the loopback port.
func PortOpen(port int) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), dialProbeTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// AuthDirKeyPath returns where a config file keeps auth-dir: the v8 layout nests
// it under oauth, the legacy layout has it at the top.
func AuthDirKeyPath(raw []byte) []string {
	var parsed struct {
		OAuth struct {
			AuthDir *string `yaml:"auth-dir"`
		} `yaml:"oauth"`
		AuthDir *string `yaml:"auth-dir"`
	}
	if yaml.Unmarshal(raw, &parsed) == nil && parsed.OAuth.AuthDir == nil && parsed.AuthDir != nil {
		return []string{"auth-dir"}
	}
	if len(raw) > 0 && parsed.OAuth.AuthDir == nil && !strings.Contains(string(raw), "config-version") {
		return []string{"auth-dir"}
	}
	return []string{"oauth", "auth-dir"}
}
