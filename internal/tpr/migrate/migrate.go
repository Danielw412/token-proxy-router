// Package migrate imports an existing CLIProxyAPI install into the TPR data
// folder. It copies and never moves: the old install keeps working until the user
// stops it, and if the new one disappoints it can simply be started again.
package migrate

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/router"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/setup"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/update"
	"gopkg.in/yaml.v3"
)

const pluginName = "custom-quota-router"

// Options are the `tpr migrate` flags.
type Options struct {
	// From is the old install folder, or its config file. Empty means ~/cliproxyapi.
	From string
	// UserHome is the user's home folder. Empty means the current one.
	UserHome string
	DryRun   bool
	Force    bool
	Out      io.Writer
	// ImportPlugin imports router state. Nil means the real router package.
	ImportPlugin func(statePath string, settings map[string]any) error
}

// Report says what a migration did, without any secret values.
type Report struct {
	SourceConfig string
	SourceAuth   string
	ConfigPath   string
	APIKeys      int
	HasMgmtKey   bool
	Port         int
	Host         string
	Copied       []string
	SkippedEmpty []string
	SkippedSame  []string
	KeptExisting []string
	PluginFound  bool
	PluginState  string
	Warnings     []string
}

func expand(path, userHome string) string {
	if path == "~" {
		return userHome
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		return filepath.Join(userHome, path[2:])
	}
	return path
}

func resolveSource(opts Options, userHome string) (configPath, baseDir string, err error) {
	from := opts.From
	if from == "" {
		from = filepath.Join(userHome, "cliproxyapi")
	}
	from = expand(from, userHome)
	info, errStat := os.Stat(from)
	if errStat != nil {
		return "", "", fmt.Errorf("no old install at %s. Point at it with `tpr migrate --from <folder>`", from)
	}
	if !info.IsDir() {
		return from, filepath.Dir(from), nil
	}
	configPath = filepath.Join(from, "config.yaml")
	if _, errConfig := os.Stat(configPath); errConfig != nil {
		return "", "", fmt.Errorf("%s has no config.yaml. Point at the folder that does with `tpr migrate --from <folder>`", from)
	}
	return configPath, from, nil
}

// Run performs the migration. With DryRun it only reports and writes nothing.
func Run(opts Options) (Report, error) {
	userHome := opts.UserHome
	if userHome == "" {
		found, err := os.UserHomeDir()
		if err != nil {
			return Report{}, fmt.Errorf("find the home folder: %w", err)
		}
		userHome = found
	}
	configPath, baseDir, err := resolveSource(opts, userHome)
	if err != nil {
		return Report{}, err
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return Report{}, fmt.Errorf("read the old config: %w", err)
	}
	cfg, err := config.ParseConfigBytes(raw)
	if err != nil {
		return Report{}, fmt.Errorf("the old config at %s is not valid: %w", configPath, err)
	}

	report := Report{
		SourceConfig: configPath,
		ConfigPath:   home.ConfigPath(),
		APIKeys:      len(cfg.APIKeys),
		HasMgmtKey:   cfg.RemoteManagement.SecretKey != "",
		Port:         cfg.Port,
		Host:         cfg.Host,
	}
	if report.Port == 0 {
		report.Port = setup.DefaultPort
	}

	sourceAuth := cfg.AuthDir
	if sourceAuth == "" {
		sourceAuth = filepath.Join(userHome, ".cli-proxy-api")
	}
	sourceAuth = expand(sourceAuth, userHome)
	if !filepath.IsAbs(sourceAuth) {
		sourceAuth = filepath.Join(baseDir, sourceAuth)
	}
	report.SourceAuth = sourceAuth

	if _, errExisting := os.Stat(home.ConfigPath()); errExisting == nil && !opts.Force {
		return report, fmt.Errorf("Token Proxy Router already has a config at %s, so nothing was changed. To replace it (a copy goes to backups/), use `tpr migrate --force`", home.ConfigPath()) //nolint:staticcheck // product name
	}

	converted, settings, statePath, err := convertConfig(raw, baseDir, userHome, &report)
	if err != nil {
		return report, err
	}
	if !setup.IsLoopbackHost(report.Host) {
		report.Warnings = append(report.Warnings, fmt.Sprintf("The old config listens on host %q, not loopback, and that was kept. Identity sign-in needs loopback: set server.host to 127.0.0.1 if you do not need LAN access.", report.Host))
	}
	if err := copyAuthFiles(sourceAuth, home.AuthDir(), opts, &report); err != nil {
		return report, err
	}
	if opts.DryRun {
		return report, nil
	}

	if err := home.Ensure(); err != nil {
		return report, fmt.Errorf("create the data folder: %w", err)
	}
	if opts.Force {
		if _, err := update.SnapshotConfig(home.ConfigPath(), "pre-migrate"); err != nil {
			return report, err
		}
	}
	if err := setup.AtomicWrite(home.ConfigPath(), converted, 0o600); err != nil {
		return report, fmt.Errorf("write the new config: %w", err)
	}
	// Hash a plaintext management key now, the same way a server start would.
	if err := config.PrepareConfigFileForUpload(home.ConfigPath()); err != nil {
		_ = os.Remove(home.ConfigPath())
		return report, fmt.Errorf("prepare the new config: %w", err)
	}
	if err := copyAuthFilesForReal(sourceAuth, home.AuthDir(), report.Copied); err != nil {
		return report, err
	}
	if report.PluginFound {
		importPlugin := opts.ImportPlugin
		if importPlugin == nil {
			importPlugin = router.ImportPluginState
		}
		if err := importPlugin(statePath, settings); err != nil {
			return report, fmt.Errorf("import router state: %w", err)
		}
	}
	if _, err := setup.MarkCompleted(); err != nil {
		return report, fmt.Errorf("record setup state: %w", err)
	}
	return report, nil
}

// convertConfig rewrites the old config for the new data folder. It keeps comments
// and every other value, including client keys and the hashed management key.
func convertConfig(raw []byte, baseDir, userHome string, report *Report) ([]byte, map[string]any, string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, nil, "", fmt.Errorf("read the old config: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, nil, "", errors.New("the old config is empty")
	}
	root := doc.Content[0]
	settings, statePath, err := extractPlugin(root, baseDir, userHome, report)
	if err != nil {
		return nil, nil, "", err
	}
	removeKey(root, "plugins")
	rewriteAuthDir(root, setup.AuthDirKeyPath(raw), home.AuthDir())
	absolutizeTLS(root, baseDir, userHome)

	var out strings.Builder
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(&doc); err != nil {
		return nil, nil, "", fmt.Errorf("write the new config: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, nil, "", err
	}
	converted := []byte(out.String())
	if _, err := config.ParseConfigBytes(converted); err != nil {
		return nil, nil, "", fmt.Errorf("the rewritten config is not valid: %w", err)
	}
	return converted, settings, statePath, nil
}

func lookup(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func removeKey(node *yaml.Node, key string) {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			node.Content = append(node.Content[:i], node.Content[i+2:]...)
			return
		}
	}
}

func scalar(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

func setScalar(node *yaml.Node, key, value string) {
	if existing := lookup(node, key); existing != nil {
		existing.Kind, existing.Tag, existing.Value, existing.Style = yaml.ScalarNode, "!!str", value, 0
		existing.Content, existing.Alias = nil, nil
		return
	}
	node.Content = append(node.Content, scalar(key), scalar(value))
}

func rewriteAuthDir(root *yaml.Node, path []string, value string) {
	current := root
	for _, key := range path[:len(path)-1] {
		next := lookup(current, key)
		if next == nil {
			next = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			current.Content = append(current.Content, scalar(key), next)
		}
		current = next
	}
	setScalar(current, path[len(path)-1], value)
}

// absolutizeTLS keeps certificate paths working now that the config has moved.
func absolutizeTLS(root *yaml.Node, baseDir, userHome string) {
	for _, tls := range []*yaml.Node{lookup(root, "tls"), lookup(lookup(root, "server"), "tls")} {
		for _, key := range []string{"cert", "key"} {
			value := lookup(tls, key)
			if value == nil || value.Value == "" {
				continue
			}
			path := expand(value.Value, userHome)
			if !filepath.IsAbs(path) {
				path = filepath.Join(baseDir, path)
			}
			value.Value = path
		}
	}
}

// extractPlugin finds the old router plugin's settings and state file.
func extractPlugin(root *yaml.Node, baseDir, userHome string, report *Report) (map[string]any, string, error) {
	configs := lookup(lookup(root, "plugins"), "configs")
	if configs == nil || configs.Kind != yaml.MappingNode {
		return nil, "", nil
	}
	ids := make([]string, 0, len(configs.Content)/2)
	for i := 0; i+1 < len(configs.Content); i += 2 {
		ids = append(ids, configs.Content[i].Value)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if id != pluginName && !strings.Contains(id, "quota-router") && !strings.Contains(id, "quota_router") {
			continue
		}
		var settings map[string]any
		if err := lookup(configs, id).Decode(&settings); err != nil {
			return nil, "", fmt.Errorf("read the settings of plugin %s: %w", id, err)
		}
		report.PluginFound = true
		statePath := pluginStatePath(settings, id, baseDir, userHome)
		report.PluginState = statePath
		return settings, statePath, nil
	}
	return nil, "", nil
}

func pluginStatePath(settings map[string]any, id, baseDir, userHome string) string {
	if raw, ok := settings["state_file"].(string); ok && strings.TrimSpace(raw) != "" {
		path := expand(strings.TrimSpace(raw), userHome)
		if !filepath.IsAbs(path) {
			path = filepath.Join(baseDir, path)
		}
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	for _, candidate := range []string{
		filepath.Join(baseDir, "plugins", id, "state.json"),
		filepath.Join(baseDir, "plugins", "state", id+".json"),
		filepath.Join(baseDir, id, "state.json"),
		filepath.Join(baseDir, id+"-state.json"),
	} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

// copyAuthFiles works out which files to copy, recording them in the report.
func copyAuthFiles(source, target string, opts Options, report *Report) error {
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return filepath.SkipAll
			}
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if relative == "logs" {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() == 0 {
			report.SkippedEmpty = append(report.SkippedEmpty, relative)
			return nil
		}
		existing, errExisting := os.ReadFile(filepath.Join(target, relative))
		if errExisting == nil {
			current, errRead := os.ReadFile(path)
			if errRead != nil {
				return errRead
			}
			if string(existing) == string(current) {
				report.SkippedSame = append(report.SkippedSame, relative)
				return nil
			}
			if !opts.Force {
				report.KeptExisting = append(report.KeptExisting, relative)
				return nil
			}
		}
		report.Copied = append(report.Copied, relative)
		return nil
	})
	if err != nil {
		return fmt.Errorf("read the old auth folder: %w", err)
	}
	return nil
}

func copyAuthFilesForReal(source, target string, files []string) error {
	for _, relative := range files {
		raw, err := os.ReadFile(filepath.Join(source, relative))
		if err != nil {
			return fmt.Errorf("copy %s: %w", relative, err)
		}
		if err := setup.AtomicWrite(filepath.Join(target, relative), raw, 0o600); err != nil {
			return fmt.Errorf("copy %s: %w", relative, err)
		}
	}
	return nil
}

// Print writes the report and the cutover steps.
func (r Report) Print(w io.Writer, dryRun bool, goos string) {
	verb := "Imported"
	if dryRun {
		verb = "Would import"
		fmt.Fprintln(w, "Dry run: nothing was written.")
	}
	fmt.Fprintf(w, "%s from %s:\n", verb, r.SourceConfig)
	fmt.Fprintf(w, "  config: port %d, %d client API key(s), management key %s\n", r.Port, r.APIKeys, presence(r.HasMgmtKey))
	fmt.Fprintf(w, "  auth files: %d from %s\n", len(r.Copied), r.SourceAuth)
	if len(r.SkippedSame) > 0 {
		fmt.Fprintf(w, "  already there: %d\n", len(r.SkippedSame))
	}
	for _, name := range r.SkippedEmpty {
		fmt.Fprintf(w, "  skipped empty auth file: %s (it holds no account, so log in again if you need it)\n", name)
	}
	for _, name := range r.KeptExisting {
		fmt.Fprintf(w, "  kept the existing auth file: %s (use --force to overwrite it)\n", name)
	}
	if r.PluginFound {
		if r.PluginState != "" {
			fmt.Fprintf(w, "  router: settings and state from %s\n", r.PluginState)
		} else {
			fmt.Fprintln(w, "  router: settings only; no state file was found")
		}
	}
	for _, warning := range r.Warnings {
		fmt.Fprintf(w, "  warning: %s\n", warning)
	}
	if !r.HasMgmtKey {
		fmt.Fprintln(w, "  warning: the old config has no management key, so the panel stays locked until you set one.")
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "New config: %s\n", r.ConfigPath)
	fmt.Fprintln(w, "The old install was not changed. Your existing management key and client keys keep working.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "IMPORTANT: run only one of the two at a time. They hold the same accounts, and")
	fmt.Fprintln(w, "providers rotate refresh tokens, so running both logs accounts out of each other.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "To switch over:")
	if goos == "windows" {
		fmt.Fprintln(w, "  1. Stop the old server (close its window, or end its Scheduled Task).")
		fmt.Fprintln(w, "  2. tpr install")
	} else {
		fmt.Fprintln(w, "  systemctl --user disable --now cliproxyapi.service && tpr install")
		fmt.Fprintln(w, "Replace the first part if you run the old server another way.")
	}
	fmt.Fprintln(w, "To switch back: stop TPR with `tpr stop`, then start the old server again. Any login TPR has refreshed by then must be logged in again on the old server.")
}

func presence(present bool) string {
	if present {
		return "kept"
	}
	return "not set"
}
