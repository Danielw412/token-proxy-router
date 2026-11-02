// Package cli implements the `tpr` subcommands. main() calls Dispatch first; with
// no recognized subcommand, arguments fall through to upstream's flag handling.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/backup"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/connect"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/migrate"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/service"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/setup"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/tailscale"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/update"
	log "github.com/sirupsen/logrus"
)

// Build is the version metadata stamped into the binary.
type Build struct {
	Version string
	Commit  string
	Date    string
}

const usage = `Token Proxy Router: one API for your Claude and ChatGPT subscriptions.

  tpr                         Run the server (first run: generate config, print the panel URL)
  tpr install [--no-service]  Copy to bin/, add to PATH, install and start the service
  tpr uninstall [--purge]     Remove the service; --purge also deletes the data folder
  tpr start|stop|restart      Control the service
  tpr status                  Service, version, port, tailnet URL, account summary
  tpr logs [-f]               Show server logs
  tpr open                    Open the panel in a browser
  tpr creds [--forget]        Show first-run keys / delete the plaintext copy
  tpr login <claude|codex>    Add an account (--no-browser for remote machines)
  tpr expose [--port 8443]    Publish on the tailnet with tailscale serve
  tpr unexpose                Remove the tailnet mapping
  tpr connect --hub URL --key KEY [--codex] [--claude] [--t3] [--default] [--dry-run]
  tpr connect --undo          Put client settings back
  tpr update [--check] [--rollback] [--version vX.Y.Z]
  tpr backup [FILE]           Write an encrypted backup
  tpr restore FILE            Restore an encrypted backup
  tpr migrate [--from DIR] [--dry-run] [--force]   Import an existing CLIProxyAPI install
  tpr doctor                  Diagnose common problems
  tpr version

The data folder is ~/.token-proxy-router (set TPR_HOME to change it).
Upstream flags such as -config, -claude-login and -tui still work.
`

// DefaultConfigPath moves the default config into the data folder. A build-time
// default stays in charge if a packager set one.
func DefaultConfigPath(current string) string {
	if strings.TrimSpace(current) != "" {
		return current
	}
	return home.ConfigPath()
}

// BeforeConfigLoad runs just before the server reads its local config file. It
// finishes a staged restore and handles first run, so a missing config never stops
// the server from starting.
func BeforeConfigLoad(configPath string) {
	if os.Getenv("DEPLOY") == "cloud" {
		return
	}
	setup.SetActiveConfigPath(configPath)
	if err := home.Ensure(); err != nil {
		log.Errorf("create data folder: %v", err)
		return
	}
	// One data folder: logs and other runtime files go here whatever the working
	// directory is.
	if os.Getenv("WRITABLE_PATH") == "" {
		if err := os.Setenv("WRITABLE_PATH", home.Dir()); err != nil {
			log.Warnf("set runtime folder: %v", err)
		}
	}
	if applied, err := backup.ApplyPending(configPath); err != nil {
		log.Errorf("finish restore: %v. The backup stays staged and will be tried again on the next start.", err)
	} else if applied {
		log.Info("restored from backup")
	}
	first, err := setup.Bootstrap(configPath)
	if err != nil {
		log.Errorf("first-run setup: %v", err)
		return
	}
	if first != nil {
		first.Print(os.Stdout)
		if isTerminal(os.Stdout) && !argPresent("no-browser") {
			openBrowserLater(first.PanelURL())
		}
	}
}

func argPresent(name string) bool {
	for _, arg := range os.Args[1:] {
		if strings.TrimLeft(arg, "-") == name {
			return true
		}
	}
	return false
}

func openBrowserLater(target string) {
	go func() {
		time.Sleep(1500 * time.Millisecond)
		if err := openBrowser(target); err != nil {
			log.Debugf("open browser: %v", err)
		}
	}()
}

func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func openBrowser(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	case "darwin":
		cmd = exec.Command("open", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// Dispatch runs a subcommand and exits. It returns false when the arguments are
// for upstream, possibly after rewriting them (`tpr login`).
func Dispatch(build Build) bool {
	args := os.Args[1:]
	if len(args) == 0 {
		return false
	}
	run, ok := commands[args[0]]
	if !ok {
		return false
	}
	c := &context_{build: build, out: os.Stdout, errOut: os.Stderr, in: os.Stdin}
	err := run(c, args[1:])
	if errors.Is(err, errFallThrough) {
		return false
	}
	if err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintf(os.Stderr, "tpr: %v\n", err)
		}
		os.Exit(1)
	}
	os.Exit(0)
	return true
}

var errFallThrough = errors.New("fall through to upstream")

// context_ carries what a command needs. Tests provide their own writers.
type context_ struct {
	build  Build
	out    io.Writer
	errOut io.Writer
	in     io.Reader
	runner setup.Runner
}

func (c *context_) run() setup.Runner {
	if c.runner != nil {
		return c.runner
	}
	return setup.ExecRunner
}

func (c *context_) manager() *service.Manager {
	return &service.Manager{Run: c.run(), Out: c.out}
}

func (c *context_) tailscale() *tailscale.Client {
	return &tailscale.Client{Run: c.run(), Binary: tailscale.FindBinary}
}

type commandFunc func(c *context_, args []string) error

var commands map[string]commandFunc

func init() {
	commands = map[string]commandFunc{
		"install":     cmdInstall,
		"uninstall":   cmdUninstall,
		"start":       cmdStart,
		"stop":        cmdStop,
		"restart":     cmdRestart,
		"status":      cmdStatus,
		"logs":        cmdLogs,
		"open":        cmdOpen,
		"creds":       cmdCreds,
		"login":       cmdLogin,
		"expose":      cmdExpose,
		"unexpose":    cmdUnexpose,
		"connect":     cmdConnect,
		"update":      cmdUpdate,
		"backup":      cmdBackup,
		"restore":     cmdRestore,
		"migrate":     cmdMigrate,
		"doctor":      cmdDoctor,
		"version":     cmdVersion,
		"help":        cmdHelp,
		"service-run": cmdServiceRun,
	}
}

func cmdHelp(c *context_, _ []string) error {
	fmt.Fprint(c.out, usage)
	return nil
}

func cmdVersion(c *context_, _ []string) error {
	fmt.Fprintf(c.out, "tpr %s\ncommit %s\nbuilt %s\nupstream server %s\npanel %s\n%s/%s\n",
		c.build.Version, c.build.Commit, c.build.Date, update.UpstreamVersion, update.PanelVersion, runtime.GOOS, runtime.GOARCH)
	return nil
}

// newFlags makes a flag set that reports problems by returning them.
func newFlags(c *context_, name string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(c.errOut)
	return flags
}

// parseAnywhere parses flags that may come before or after positional arguments,
// and returns the positional ones.
func parseAnywhere(flags *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := flags.Parse(args); err != nil {
			return nil, err
		}
		args = flags.Args()
		if len(args) == 0 {
			return positional, nil
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
}

func configFlag(flags *flag.FlagSet) *string {
	return flags.String("config", home.ConfigPath(), "config file path")
}

func loadInfo(configPath string) (setup.ServerInfo, error) {
	info, err := setup.LoadServerInfo(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return info, fmt.Errorf("no config at %s yet. Run `tpr` or `tpr install` first", configPath)
	}
	if err != nil {
		return info, fmt.Errorf("the config at %s is not valid: %w", configPath, err)
	}
	return info, nil
}

func cmdStart(c *context_, args []string) error {
	flags := newFlags(c, "start")
	if err := flags.Parse(args); err != nil {
		return err
	}
	exe, err := update.TargetPath()
	if err != nil {
		return err
	}
	if err := c.manager().Start(exe); err != nil {
		return err
	}
	fmt.Fprintln(c.out, "Started. Check it with `tpr status`.")
	return nil
}

func cmdStop(c *context_, args []string) error {
	flags := newFlags(c, "stop")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := c.manager().Stop(); err != nil {
		if errors.Is(err, service.ErrNotRunning) {
			fmt.Fprintln(c.out, "The service is not running.")
			return nil
		}
		return err
	}
	fmt.Fprintln(c.out, "Stopped.")
	return nil
}

func cmdRestart(c *context_, args []string) error {
	flags := newFlags(c, "restart")
	if err := flags.Parse(args); err != nil {
		return err
	}
	exe, err := update.TargetPath()
	if err != nil {
		return err
	}
	if err := c.manager().Restart(exe); err != nil {
		return err
	}
	fmt.Fprintln(c.out, "Restarting. Check it with `tpr status`.")
	return nil
}

func cmdLogs(c *context_, args []string) error {
	flags := newFlags(c, "logs")
	follow := flags.Bool("f", false, "keep following the log")
	lines := flags.Int("n", 200, "lines to show")
	if err := flags.Parse(args); err != nil {
		return err
	}
	path := service.LogPath()
	if _, err := os.Stat(path); err != nil {
		fallback := filepath.Join(home.LogsDir(), "main.log")
		if _, errFallback := os.Stat(fallback); errFallback != nil {
			return fmt.Errorf("no logs yet at %s. Logs appear once the server runs as a service (`tpr install` or `tpr start`); `tpr` in a terminal prints them to that terminal", path)
		}
		path = fallback
	}
	ctx, cancel := signalContext()
	defer cancel()
	return service.Tail(ctx, path, *lines, *follow, c.out)
}

func cmdOpen(c *context_, args []string) error {
	flags := newFlags(c, "open")
	configPath := configFlag(flags)
	if err := flags.Parse(args); err != nil {
		return err
	}
	info, err := loadInfo(*configPath)
	if err != nil {
		return err
	}
	target := info.PanelURL()
	fmt.Fprintln(c.out, target)
	if err := openBrowser(target); err != nil {
		return fmt.Errorf("could not open a browser. Open this address yourself: %s", target)
	}
	return nil
}

func cmdCreds(c *context_, args []string) error {
	flags := newFlags(c, "creds")
	forget := flags.Bool("forget", false, "delete the plaintext copy of the keys")
	if err := flags.Parse(args); err != nil {
		return err
	}
	path := home.CredentialsPath()
	if *forget {
		err := os.Remove(path)
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(c.out, "Nothing to delete: there is no credentials file.")
			return nil
		}
		if err != nil {
			return fmt.Errorf("delete %s: %w", path, err)
		}
		fmt.Fprintf(c.out, "Deleted %s. The keys keep working, but this was the only plaintext copy of the management key, so keep it somewhere safe.\n", path)
		return nil
	}
	keys, err := setup.ReadCredentials()
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("no saved keys at %s. They were deleted, or this install was migrated, in which case your old keys still work", path)
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	fmt.Fprintf(c.out, "Client API key:  %s\nManagement key:  %s\n\nThese come from %s. Delete that file with `tpr creds --forget`.\n", keys.API, keys.Management, path)
	return nil
}

// cmdLogin rewrites its arguments into upstream's login flags and falls through.
func cmdLogin(c *context_, args []string) error {
	flags := newFlags(c, "login")
	noBrowser := flags.Bool("no-browser", false, "print the login URL instead of opening a browser")
	device := flags.Bool("device", false, "use the device code flow (codex only)")
	configPath := flags.String("config", "", "config file path")
	positional, err := parseAnywhere(flags, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return errors.New("name the provider: `tpr login claude` or `tpr login codex`")
	}
	var loginFlag string
	switch strings.ToLower(positional[0]) {
	case "claude":
		loginFlag = "-claude-login"
	case "codex":
		loginFlag = "-codex-login"
		if *device {
			loginFlag = "-codex-device-login"
		}
	default:
		return fmt.Errorf("%q is not a provider `tpr login` knows. Use claude or codex; upstream login flags cover the rest", positional[0])
	}
	rewritten := []string{os.Args[0], loginFlag}
	if *noBrowser {
		rewritten = append(rewritten, "-no-browser")
	}
	if *configPath != "" {
		rewritten = append(rewritten, "-config", *configPath)
	}
	os.Args = rewritten
	return errFallThrough
}

func cmdExpose(c *context_, args []string) error {
	flags := newFlags(c, "expose")
	port := flags.Int("port", tailscale.DefaultPort, "tailnet HTTPS port")
	configPath := configFlag(flags)
	if err := flags.Parse(args); err != nil {
		return err
	}
	info, err := loadInfo(*configPath)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	client := c.tailscale()
	status := client.Status(ctx)
	if !status.LoggedIn {
		return errors.New(status.Hint)
	}
	url, err := client.Publish(ctx, *port, info)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "Published on your tailnet: %s\nOn other machines: tpr connect --hub %s --key <client key>\n", url, url)
	return nil
}

func cmdUnexpose(c *context_, args []string) error {
	flags := newFlags(c, "unexpose")
	configPath := configFlag(flags)
	if err := flags.Parse(args); err != nil {
		return err
	}
	info, err := loadInfo(*configPath)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := c.tailscale().Unpublish(ctx, info); err != nil {
		return err
	}
	fmt.Fprintln(c.out, "No longer published on your tailnet. Other serve mappings were left alone.")
	return nil
}

func cmdConnect(c *context_, args []string) error {
	flags := newFlags(c, "connect")
	hub := flags.String("hub", "", "hub address, like https://<host>:8443")
	key := flags.String("key", "", "client API key (or set TPR_API_KEY)")
	codex := flags.Bool("codex", false, "configure Codex")
	claude := flags.Bool("claude", false, "configure Claude Code")
	t3 := flags.Bool("t3", false, "configure T3 Code")
	setDefault := flags.Bool("default", false, "make the hub Codex's default provider")
	dryRun := flags.Bool("dry-run", false, "show what would change")
	undo := flags.Bool("undo", false, "put everything back")
	if err := flags.Parse(args); err != nil {
		return err
	}
	env, err := connect.DefaultEnv()
	if err != nil {
		return err
	}
	if c.runner != nil {
		env.Run = c.runner
	}
	ctx, cancel := signalContext()
	defer cancel()
	if *undo {
		return connect.Undo(ctx, env, c.out)
	}
	apiKey := *key
	if apiKey == "" {
		apiKey = os.Getenv("TPR_API_KEY")
	}
	return connect.Run(ctx, env, connect.Options{
		Hub: *hub, Key: apiKey, Codex: *codex, Claude: *claude, T3: *t3,
		Default: *setDefault, DryRun: *dryRun, Out: c.out,
	})
}

func cmdMigrate(c *context_, args []string) error {
	flags := newFlags(c, "migrate")
	from := flags.String("from", "", "old install folder (default ~/cliproxyapi)")
	dryRun := flags.Bool("dry-run", false, "show what would be imported")
	force := flags.Bool("force", false, "replace an existing Token Proxy Router config")
	if err := flags.Parse(args); err != nil {
		return err
	}
	report, err := migrate.Run(migrate.Options{From: *from, DryRun: *dryRun, Force: *force, Out: c.out})
	if err != nil {
		return err
	}
	report.Print(c.out, *dryRun, runtime.GOOS)
	return nil
}
