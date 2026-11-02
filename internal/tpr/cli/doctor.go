package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/backup"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/service"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/setup"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/tailscale"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/update"
)

const (
	checkOK = iota
	checkWarn
	checkFail
)

const (
	skewWarning  = 2 * time.Minute
	oldUnit      = "cliproxyapi.service"
	oldRootLabel = "CLI Proxy API Server"
	oldPort      = 8317
)

// doctor gathers checks. Its inputs are injected, so the tests touch nothing real.
type doctor struct {
	out     io.Writer
	config  string
	version string
	goos    string
	manager *service.Manager
	ts      *tailscale.Client
	run     setup.Runner
	http    *http.Client
	source  *update.ReleaseSource
	// clockURL is compared with the local clock.
	clockURL string
	// oldPorts are probed for a running CLIProxyAPI server.
	oldPorts []int

	failures int
	warnings int
}

func (d *doctor) report(level int, label, detail, next string) {
	tag := "ok"
	switch level {
	case checkWarn:
		tag = "warn"
		d.warnings++
	case checkFail:
		tag = "FAIL"
		d.failures++
	}
	fmt.Fprintf(d.out, "[%-4s] %s", tag, label)
	if detail != "" {
		fmt.Fprintf(d.out, ": %s", detail)
	}
	fmt.Fprintln(d.out)
	if next != "" {
		fmt.Fprintf(d.out, "       Next: %s\n", next)
	}
}

func cmdDoctor(c *context_, args []string) error {
	flags := newFlags(c, "doctor")
	configPath := configFlag(flags)
	if err := flags.Parse(args); err != nil {
		return err
	}
	d := &doctor{
		out:      c.out,
		config:   *configPath,
		version:  c.build.Version,
		goos:     runtime.GOOS,
		manager:  c.manager(),
		ts:       c.tailscale(),
		run:      c.run(),
		http:     &http.Client{Timeout: 8 * time.Second, Transport: insecureLoopback()},
		source:   update.NewReleaseSource(),
		clockURL: "https://api.github.com",
		oldPorts: []int{oldPort},
	}
	d.manager.Out = io.Discard
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	d.run_(ctx)
	if d.failures > 0 {
		return fmt.Errorf("%d problem(s) found", d.failures)
	}
	if d.warnings > 0 {
		fmt.Fprintf(c.out, "No failures, %d warning(s).\n", d.warnings)
	} else {
		fmt.Fprintln(c.out, "Everything looks good.")
	}
	return nil
}

func (d *doctor) run_(ctx context.Context) {
	fmt.Fprintf(d.out, "Token Proxy Router %s, data folder %s\n", d.version, home.Dir())
	info, errInfo := setup.LoadServerInfo(d.config)
	configOK := errInfo == nil
	switch {
	case errors.Is(errInfo, os.ErrNotExist):
		d.report(checkFail, "config", "none at "+d.config, "run `tpr` or `tpr install`, or on a client-only machine ignore this and use `tpr connect`")
	case errInfo != nil:
		d.report(checkFail, "config", "not valid: "+errInfo.Error(), "fix "+d.config+", or restore a copy from "+home.BackupsDir())
	default:
		d.report(checkOK, "config", d.config, "")
	}
	d.checkService()
	serverIsUp := false
	if configOK {
		serverIsUp = d.checkPort(ctx, info)
		d.checkKeys(info)
		d.checkAccounts(ctx, info, serverIsUp)
		d.checkTailscale(ctx, info)
	}
	d.checkClock(ctx)
	d.checkUpdate(ctx)
	d.checkOldInstall(ctx, info, configOK)
	if backup.PendingRestore() {
		d.report(checkWarn, "restore", "a backup is staged and will apply on the next start", "run `tpr restart`")
	}
}

func (d *doctor) checkService() {
	status := d.manager.Status()
	switch {
	case !status.Installed && !status.Running:
		d.report(checkWarn, "service", "not installed", "run `tpr install` to keep the server running")
	case !status.Running:
		d.report(checkFail, "service", string(status.Kind)+" is installed but not running", "run `tpr start`, then `tpr logs` if it stops again")
	default:
		detail := string(status.Kind) + " is running"
		if status.Kind == service.KindNone {
			detail = "running in the background, but it will not start again after a reboot"
		}
		d.report(checkOK, "service", detail, "")
	}
}

func (d *doctor) get(ctx context.Context, target string, header map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	for name, value := range header {
		req.Header.Set(name, value)
	}
	return d.http.Do(req)
}

func (d *doctor) checkPort(ctx context.Context, info setup.ServerInfo) bool {
	resp, err := d.get(ctx, info.LocalURL()+"/healthz", nil)
	if err == nil {
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			d.report(checkOK, "port", fmt.Sprintf("the server answers on %s", info.LocalURL()), "")
			return true
		}
	}
	if setup.PortOpen(info.Port) {
		d.report(checkFail, "port", fmt.Sprintf("something else is using port %d", info.Port), "stop it, or change server.port in "+d.config)
		return false
	}
	d.report(checkWarn, "port", fmt.Sprintf("nothing is listening on port %d", info.Port), "run `tpr start`")
	return false
}

func (d *doctor) checkKeys(info setup.ServerInfo) {
	if !info.HasMgmtKey {
		d.report(checkFail, "management key", "none is set, so the panel and management API are off", "set management.secret-key in "+d.config)
	} else if setup.ManagementKey() == "" {
		d.report(checkOK, "management key", "set (no plaintext copy here, which is fine)", "")
	} else {
		d.report(checkOK, "management key", "set", "")
	}
	if len(info.APIKeys) == 0 {
		d.report(checkFail, "API key", "no client API keys, so no tool can use this server", "add one in the panel")
	} else {
		d.report(checkOK, "API key", fmt.Sprintf("%d configured", len(info.APIKeys)), "")
	}
}

// accountSummary counts auth files by provider without reading anything secret.
func accountSummary(authDir string) (counts map[string]int, empty []string) {
	counts = map[string]int{}
	_ = filepath.WalkDir(authDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			if entry.Name() == "logs" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".json") {
			return nil
		}
		raw, errRead := os.ReadFile(path)
		if errRead != nil {
			return nil
		}
		if len(raw) == 0 {
			empty = append(empty, entry.Name())
			return nil
		}
		var parsed struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &parsed) != nil || parsed.Type == "" {
			parsed.Type = "other"
		}
		counts[parsed.Type]++
		return nil
	})
	return counts, empty
}

func describeCounts(counts map[string]int) string {
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%d %s", counts[name], name))
	}
	return strings.Join(parts, ", ")
}

func (d *doctor) checkAccounts(ctx context.Context, info setup.ServerInfo, serverIsUp bool) {
	counts, empty := accountSummary(info.AuthDir)
	if len(empty) > 0 {
		d.report(checkWarn, "accounts", "empty auth files: "+strings.Join(empty, ", "), "delete them; they hold no account")
	}
	if len(counts) == 0 {
		d.report(checkWarn, "accounts", "none yet", "run `tpr login claude` or `tpr login codex`")
		return
	}
	d.report(checkOK, "accounts", describeCounts(counts), "")
	key := setup.ManagementKey()
	if !serverIsUp || key == "" {
		return
	}
	resp, err := d.get(ctx, info.LocalURL()+managementBase+"/health/accounts", map[string]string{"Authorization": "Bearer " + key})
	if err != nil {
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return
	}
	var parsed struct {
		Accounts []struct {
			Label  string `json:"label"`
			ID     string `json:"id"`
			Status string `json:"status"`
			Reason string `json:"reason"`
		} `json:"accounts"`
	}
	if json.NewDecoder(resp.Body).Decode(&parsed) != nil {
		return
	}
	for _, account := range parsed.Accounts {
		name := account.Label
		if name == "" {
			name = account.ID
		}
		switch account.Status {
		case "expired", "failing":
			d.report(checkFail, "account "+name, account.Status+": "+account.Reason, "open the panel and log in again")
		case "expiring", "cooling":
			d.report(checkWarn, "account "+name, account.Status+": "+account.Reason, "")
		}
	}
}

func (d *doctor) checkTailscale(ctx context.Context, info setup.ServerInfo) {
	report := d.ts.BuildReport(ctx, info, tailscale.LoadState())
	switch {
	case !report.Installed:
		d.report(checkWarn, "tailscale", "not installed, so only this machine can reach the server", "install Tailscale to use other machines: https://tailscale.com/download")
	case !report.LoggedIn:
		d.report(checkWarn, "tailscale", report.Hint, "")
	case !report.Exposed:
		d.report(checkWarn, "tailscale", "signed in as "+report.DNSName+", but the server is not published", "run `tpr expose`")
	default:
		d.report(checkOK, "tailscale", "published at "+report.URL, "")
	}
}

func (d *doctor) checkClock(ctx context.Context) {
	resp, err := d.get(ctx, d.clockURL, nil)
	if err != nil {
		d.report(checkWarn, "clock", "could not compare with the internet, so skipped", "")
		return
	}
	_ = resp.Body.Close()
	server, err := http.ParseTime(resp.Header.Get("Date"))
	if err != nil {
		d.report(checkWarn, "clock", "could not compare with the internet, so skipped", "")
		return
	}
	skew := time.Since(server).Round(time.Second)
	if skew < 0 {
		skew = -skew
	}
	if skew > skewWarning {
		d.report(checkFail, "clock", fmt.Sprintf("off by %s, which makes OAuth logins and quota resets fail", skew), "turn on automatic time sync")
		return
	}
	d.report(checkOK, "clock", "in sync", "")
}

func (d *doctor) checkUpdate(ctx context.Context) {
	cache := update.Check(ctx, d.source)
	switch {
	case cache.LastError != "":
		d.report(checkWarn, "update", "could not check: "+cache.LastError, "")
	case update.Newer(cache.Latest, d.version):
		d.report(checkWarn, "update", fmt.Sprintf("%s is available (you have %s)", cache.Latest, d.version), "run `tpr update`")
	case cache.Latest == "":
		d.report(checkOK, "update", "no releases published yet", "")
	default:
		d.report(checkOK, "update", "you have the latest", "")
	}
}

// checkOldInstall looks for a CLIProxyAPI server that would fight over accounts.
func (d *doctor) checkOldInstall(ctx context.Context, info setup.ServerInfo, configOK bool) {
	found := ""
	if d.goos == "windows" {
		probe, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if stdout, _, err := d.run(probe, "tasklist", "/FI", "IMAGENAME eq cli-proxy-api.exe", "/NH"); err == nil && strings.Contains(strings.ToLower(string(stdout)), "cli-proxy-api.exe") {
			found = "the cli-proxy-api.exe process is running"
		}
	} else {
		probe, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if stdout, _, err := d.run(probe, "systemctl", "--user", "is-active", oldUnit); err == nil && strings.TrimSpace(string(stdout)) == "active" {
			found = "the " + oldUnit + " service is running"
		}
	}
	if found == "" {
		ports := append([]int{}, d.oldPorts...)
		if configOK && !slices.Contains(ports, info.Port) {
			ports = append(ports, info.Port)
		}
		for _, port := range ports {
			if d.oldServerOnPort(ctx, port) {
				found = fmt.Sprintf("a CLIProxyAPI server is answering on port %d", port)
				break
			}
		}
	}
	if found == "" {
		d.report(checkOK, "old install", "no CLIProxyAPI server is running", "")
		return
	}
	next := "stop it before starting this one"
	if d.goos != "windows" {
		next = "if this install has your accounts, run `systemctl --user disable --now " + oldUnit + "`"
	}
	d.report(checkFail, "old install", found+". If both use the same accounts, rotating refresh tokens will log them out of each other", next)
}

func (d *doctor) oldServerOnPort(ctx context.Context, port int) bool {
	resp, err := d.get(ctx, fmt.Sprintf("http://127.0.0.1:%d/", port), nil)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return strings.Contains(string(body), oldRootLabel)
}

func cmdStatus(c *context_, args []string) error {
	flags := newFlags(c, "status")
	configPath := configFlag(flags)
	if err := flags.Parse(args); err != nil {
		return err
	}
	manager := c.manager()
	manager.Out = io.Discard
	status := manager.Status()
	fmt.Fprintf(c.out, "Version:  %s\n", c.build.Version)
	state := "stopped"
	if status.Running {
		state = "running"
	}
	if !status.Installed && status.Kind == service.KindNone {
		fmt.Fprintf(c.out, "Service:  not installed, %s\n", state)
	} else {
		fmt.Fprintf(c.out, "Service:  %s, %s\n", status.Kind, state)
	}
	fmt.Fprintf(c.out, "Data:     %s\n", home.Dir())
	info, err := setup.LoadServerInfo(*configPath)
	if err != nil {
		fmt.Fprintln(c.out, "Server:   no config yet. Run `tpr` or `tpr install`.")
		return nil
	}
	reach := "not answering"
	if serverUp(info.LocalURL()) {
		reach = "answering"
	}
	fmt.Fprintf(c.out, "Server:   %s (%s)\n", info.LocalURL(), reach)
	fmt.Fprintf(c.out, "Panel:    %s\n", info.PanelURL())
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	report := c.tailscale().BuildReport(ctx, info, tailscale.LoadState())
	if report.Exposed {
		fmt.Fprintf(c.out, "Tailnet:  %s\n", report.URL)
	} else {
		fmt.Fprintln(c.out, "Tailnet:  not published (`tpr expose`)")
	}
	counts, _ := accountSummary(info.AuthDir)
	if len(counts) == 0 {
		fmt.Fprintln(c.out, "Accounts: none yet (`tpr login claude`, `tpr login codex`)")
	} else {
		fmt.Fprintf(c.out, "Accounts: %s\n", describeCounts(counts))
	}
	return nil
}
