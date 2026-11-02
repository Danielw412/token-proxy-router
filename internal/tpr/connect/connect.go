// Package connect points Codex, Claude Code and T3 Code on this machine at a hub.
// Every change is a non-destructive merge, every original file is backed up, and
// --undo puts things back: byte for byte when the file was not edited since, and
// by removing only what TPR added when it was.
package connect

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/setup"
)

const (
	envFileName     = "env"
	profileMarker   = "# added by tpr connect"
	validateTimeout = 15 * time.Second
	journalVersion  = 1
)

// Tool names, also used as journal entry kinds.
const (
	kindCodex   = "codex"
	kindClaude  = "claude"
	kindT3      = "t3"
	kindSecret  = "secret"
	kindEnvFile = "envfile"
	kindProfile = "profile"
	kindSetx    = "setx"
)

// Env is everything about the machine that connect depends on.
type Env struct {
	// UserHome is the user's home folder.
	UserHome string
	// T3Home is T3 Code's home folder.
	T3Home string
	// GOOS picks OS-specific behaviour.
	GOOS string
	// Shell is the user's login shell path.
	Shell string
	// Run executes setx and reg on Windows.
	Run setup.Runner
	// LookPath finds a tool on PATH.
	LookPath func(string) (string, error)
	// HTTP validates the hub.
	HTTP *http.Client
}

// DefaultEnv reads the environment of this machine.
func DefaultEnv() (Env, error) {
	userHome, err := os.UserHomeDir()
	if err != nil {
		return Env{}, fmt.Errorf("find the home folder: %w", err)
	}
	t3Home := strings.TrimSpace(os.Getenv("T3CODE_HOME"))
	if t3Home == "" {
		t3Home = filepath.Join(userHome, ".t3")
	}
	return Env{
		UserHome: userHome,
		T3Home:   t3Home,
		GOOS:     runtime.GOOS,
		Shell:    os.Getenv("SHELL"),
		Run:      setup.ExecRunner,
		LookPath: exec.LookPath,
		HTTP:     &http.Client{},
	}, nil
}

// Options are the `tpr connect` flags.
type Options struct {
	Hub     string
	Key     string
	Codex   bool
	Claude  bool
	T3      bool
	Default bool
	DryRun  bool
	Out     io.Writer
}

func (e Env) codexConfig() string { return filepath.Join(e.UserHome, ".codex", "config.toml") }
func (e Env) claudeSettings() string {
	return filepath.Join(e.UserHome, ".claude", "settings.json")
}
func (e Env) t3Userdata() string { return filepath.Join(e.T3Home, "userdata") }
func (e Env) t3Settings() string { return filepath.Join(e.t3Userdata(), "settings.json") }

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (e Env) hasTool(folder, binary string) bool {
	if exists(folder) {
		return true
	}
	if e.LookPath == nil {
		return false
	}
	_, err := e.LookPath(binary)
	return err == nil
}

// Detected reports which tools are present.
func (e Env) Detected() (codex, claude, t3 bool) {
	return e.hasTool(filepath.Join(e.UserHome, ".codex"), "codex"),
		e.hasTool(filepath.Join(e.UserHome, ".claude"), "claude"),
		exists(e.T3Home)
}

// NormalizeHub cleans a hub URL and refuses anything unsafe to put in a command
// or a config file.
func NormalizeHub(hub string) (string, error) {
	hub = strings.TrimRight(strings.TrimSpace(hub), "/")
	hub = strings.TrimSuffix(hub, "/v1")
	hub = strings.TrimRight(hub, "/")
	if hub == "" {
		return "", errors.New("the hub address is required, like --hub https://<host>:8443")
	}
	parsed, err := url.Parse(hub)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", fmt.Errorf("%q is not a hub address. It should look like https://<host>:8443", hub)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("the hub address cannot include a username, query or fragment")
	}
	if !safeToken.MatchString(hub) {
		return "", errors.New("the hub address contains characters that are not safe in a command")
	}
	return hub, nil
}

// safeToken matches what is safe to place in every config format and shell.
var safeToken = regexp.MustCompile(`^[A-Za-z0-9._~:/@%+=,\[\]-]+$`)

// ValidateKey refuses a key that could not be written safely.
func ValidateKey(key string) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("the API key is required: pass --key, or set TPR_API_KEY")
	}
	if !safeToken.MatchString(key) {
		return errors.New("this API key contains spaces or symbols that are not safe in a command. Use a key without them")
	}
	return nil
}

// ValidateHub checks that the hub is reachable and accepts the key.
func ValidateHub(ctx context.Context, client *http.Client, hub, key string) error {
	ctx, cancel := context.WithTimeout(ctx, validateTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, hub+"/v1/models", nil)
	if err != nil {
		return fmt.Errorf("bad hub address: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach the hub at %s. Is it running, and is this machine on the same tailnet? On the hub, run `tpr doctor`. (%s)", hub, describeNetError(err))
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusOK:
		return nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return errors.New("the hub rejected this API key. Copy a key from the hub's panel, or run `tpr creds` on the hub")
	default:
		return fmt.Errorf("the hub answered %d to /v1/models. Is %s really a Token Proxy Router hub? On the hub, run `tpr doctor`", resp.StatusCode, hub)
	}
}

func describeNetError(err error) string {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return opErr.Err.Error()
	}
	return err.Error()
}

func sha(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Journal records how to undo what connect changed.
type Journal struct {
	Version int            `json:"version"`
	Hub     string         `json:"hub,omitempty"`
	Entries []JournalEntry `json:"entries"`
}

// JournalEntry is one changed file, or the Windows environment variable.
type JournalEntry struct {
	Kind        string   `json:"kind"`
	Path        string   `json:"path,omitempty"`
	Existed     bool     `json:"existed"`
	Backup      string   `json:"backup,omitempty"`
	Mode        uint32   `json:"mode,omitempty"`
	AfterSHA    string   `json:"after_sha,omitempty"`
	CreatedDirs []string `json:"created_dirs,omitempty"`
	// PriorValue and PriorType hold a user environment variable that connect
	// replaced. The journal is owner-only, like the other backups beside it.
	PriorValue string `json:"prior_value,omitempty"`
	PriorType  string `json:"prior_type,omitempty"`
}

func connectDir() string  { return filepath.Join(home.Dir(), "connect") }
func journalPath() string { return filepath.Join(connectDir(), "journal.json") }
func backupsDir() string  { return filepath.Join(connectDir(), "backups") }

func loadJournal() (Journal, error) {
	raw, err := os.ReadFile(journalPath())
	if errors.Is(err, os.ErrNotExist) {
		return Journal{Version: journalVersion}, nil
	}
	if err != nil {
		return Journal{}, err
	}
	var journal Journal
	if err := json.Unmarshal(raw, &journal); err != nil {
		return Journal{}, fmt.Errorf("the connect journal is unreadable: %w", err)
	}
	return journal, nil
}

func saveJournal(journal Journal) error {
	raw, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return err
	}
	return setup.AtomicWrite(journalPath(), raw, 0o600)
}

// session applies a set of file changes and records them in the journal.
type session struct {
	journal Journal
	out     io.Writer
	dryRun  bool
}

func (s *session) find(path string) *JournalEntry {
	for i := range s.journal.Entries {
		if s.journal.Entries[i].Path == path {
			return &s.journal.Entries[i]
		}
	}
	return nil
}

func missingParents(path string) []string {
	var created []string
	for dir := filepath.Dir(path); dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		if exists(dir) {
			break
		}
		created = append(created, dir)
	}
	return created
}

// apply writes content to path. The first change to a file captures its backup,
// so that undo always returns to the state from before TPR touched it.
func (s *session) apply(kind, path string, content []byte, mode os.FileMode, label string) error {
	original, errRead := os.ReadFile(path)
	if errRead != nil && !errors.Is(errRead, os.ErrNotExist) {
		return fmt.Errorf("read %s: %w", path, errRead)
	}
	existed := errRead == nil
	if existed && bytes.Equal(original, content) {
		fmt.Fprintf(s.out, "  already set: %s\n", label)
		return nil
	}
	if s.dryRun {
		fmt.Fprintf(s.out, "  would change: %s\n", label)
		return nil
	}
	entry := s.find(path)
	if entry == nil {
		next := JournalEntry{Kind: kind, Path: path, Existed: existed, CreatedDirs: missingParents(path)}
		if existed {
			info, err := os.Stat(path)
			if err != nil {
				return err
			}
			next.Mode = uint32(info.Mode().Perm())
			next.Backup = filepath.Join(backupsDir(), fmt.Sprintf("%03d-%s", len(s.journal.Entries), filepath.Base(path)))
			if err := setup.AtomicWrite(next.Backup, original, 0o600); err != nil {
				return fmt.Errorf("back up %s: %w", path, err)
			}
		}
		s.journal.Entries = append(s.journal.Entries, next)
		entry = &s.journal.Entries[len(s.journal.Entries)-1]
	}
	if err := setup.AtomicWrite(path, content, mode); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	entry.AfterSHA = sha(content)
	// Save after each file, so a failure partway can still be undone.
	if err := saveJournal(s.journal); err != nil {
		return fmt.Errorf("save the undo journal: %w", err)
	}
	fmt.Fprintf(s.out, "  changed: %s\n", label)
	return nil
}

func readOrEmpty(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return raw, err
}

func modeOr(path string, fallback os.FileMode) os.FileMode {
	if info, err := os.Stat(path); err == nil {
		return info.Mode().Perm()
	}
	return fallback
}

// Run validates the hub and configures the chosen tools.
func Run(ctx context.Context, env Env, opts Options) error {
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	hub, err := NormalizeHub(opts.Hub)
	if err != nil {
		return err
	}
	if err := ValidateKey(opts.Key); err != nil {
		return err
	}
	if strings.HasPrefix(hub, "http://") && !isLocalHub(hub) {
		fmt.Fprintln(out, "Warning: this hub uses plain HTTP, so the API key travels unencrypted. Prefer the https:// tailnet address from `tpr expose`.")
	}
	if err := ValidateHub(ctx, env.HTTP, hub, opts.Key); err != nil {
		return err
	}
	fmt.Fprintf(out, "Hub OK: %s accepted the key.\n", hub)

	useCodex, useClaude, useT3 := opts.Codex, opts.Claude, opts.T3
	if !useCodex && !useClaude && !useT3 {
		useCodex, useClaude, useT3 = env.Detected()
		if !useCodex && !useClaude && !useT3 {
			return errors.New("no Codex, Claude Code or T3 Code found on this machine. Install one, or name it with --codex, --claude or --t3")
		}
	}

	journal, err := loadJournal()
	if err != nil {
		return err
	}
	if !opts.DryRun {
		if err := home.Ensure(); err != nil {
			return fmt.Errorf("create data folder: %w", err)
		}
	}
	journal.Hub = hub
	s := &session{journal: journal, out: out, dryRun: opts.DryRun}

	if opts.DryRun {
		fmt.Fprintln(out, "Dry run: nothing will be written.")
	}
	if useCodex {
		if err := s.connectCodex(env, hub, opts); err != nil {
			return fmt.Errorf("Codex: %w", err) //nolint:staticcheck // product name
		}
	}
	if useClaude {
		if err := s.connectClaude(env, hub, opts.Key); err != nil {
			return fmt.Errorf("Claude Code: %w", err) //nolint:staticcheck // product name
		}
		fmt.Fprintln(out, "  note: Claude Code on this machine now always goes through the hub, for every project. `tpr connect --undo` reverts that.")
	}
	if useT3 {
		if err := s.connectT3(env, hub, opts.Key); err != nil {
			return fmt.Errorf("T3 Code: %w", err) //nolint:staticcheck // product name
		}
		fmt.Fprintln(out, "  note: restart T3 Code to pick up the new providers.")
	}
	if opts.DryRun {
		return nil
	}
	if err := saveJournal(s.journal); err != nil {
		return fmt.Errorf("save the undo journal: %w", err)
	}
	fmt.Fprintln(out, "Done. To put everything back: tpr connect --undo")
	return nil
}

func isLocalHub(hub string) bool {
	parsed, err := url.Parse(hub)
	if err != nil {
		return false
	}
	return setup.IsLoopbackHost(parsed.Hostname())
}

func (s *session) connectCodex(env Env, hub string, opts Options) error {
	path := env.codexConfig()
	original, err := readOrEmpty(path)
	if err != nil {
		return err
	}
	merged, err := MergeCodex(original, hub, opts.Default)
	if err != nil {
		return err
	}
	if err := s.apply(kindCodex, path, merged, modeOr(path, 0o600), "Codex config "+path); err != nil {
		return err
	}
	if !opts.Default && !s.dryRun {
		fmt.Fprintln(s.out, "  note: Codex keeps its default provider. Use the hub with `codex --profile tpr`, or rerun with --default.")
	}
	return s.persistKey(env, opts.Key)
}

// persistKey makes TPR_API_KEY available to new shells: the user environment on
// Windows, a sourced env file on everything else.
func (s *session) persistKey(env Env, key string) error {
	if env.GOOS == "windows" {
		return s.persistKeyWindows(env, key)
	}
	envPath := filepath.Join(home.Dir(), envFileName)
	content := "# Written by `tpr connect`. Removed by `tpr connect --undo`.\nexport " + codexEnvKey + "='" + key + "'\n"
	if err := s.apply(kindEnvFile, envPath, []byte(content), 0o600, "API key file "+envPath); err != nil {
		return err
	}
	line := includeLine(env.UserHome, envPath)
	for _, profile := range env.profiles() {
		original, err := readOrEmpty(profile)
		if err != nil {
			return err
		}
		if strings.Contains(string(original), line) {
			fmt.Fprintf(s.out, "  already set: %s\n", profile)
			continue
		}
		updated := append([]byte{}, original...)
		if len(updated) > 0 && !bytes.HasSuffix(updated, []byte("\n")) {
			updated = append(updated, '\n')
		}
		updated = append(updated, line+"\n"...)
		if err := s.apply(kindProfile, profile, updated, modeOr(profile, 0o644), "shell profile "+profile); err != nil {
			return err
		}
	}
	return nil
}

func includeLine(userHome, envPath string) string {
	shown := "'" + envPath + "'"
	if relative, err := filepath.Rel(userHome, envPath); err == nil && !strings.HasPrefix(relative, "..") {
		shown = `"$HOME/` + filepath.ToSlash(relative) + `"`
	}
	return "[ -f " + shown + " ] && . " + shown + " " + profileMarker
}

// profiles returns the shell startup files to update: the one for the user's
// shell, plus ~/.profile when it exists, since GUI logins read that one.
func (e Env) profiles() []string {
	rc := ".profile"
	switch filepath.Base(e.Shell) {
	case "zsh":
		rc = ".zshrc"
	case "bash":
		rc = ".bashrc"
	}
	paths := []string{filepath.Join(e.UserHome, rc)}
	profile := filepath.Join(e.UserHome, ".profile")
	if rc != ".profile" && exists(profile) {
		paths = append(paths, profile)
	}
	return paths
}

func (s *session) persistKeyWindows(env Env, key string) error {
	if s.dryRun {
		fmt.Fprintln(s.out, "  would change: user environment variable "+codexEnvKey)
		return nil
	}
	if env.Run == nil {
		return errors.New("no way to set the user environment")
	}
	// Journal first, so a crash after setx can't lose the value being replaced. The
	// first connect's value is the one to go back to, whatever later reruns replace.
	entry := s.find(codexEnvKey)
	if entry == nil {
		priorType, priorValue, existed := readUserEnv(env, codexEnvKey)
		s.journal.Entries = append(s.journal.Entries, JournalEntry{
			Kind: kindSetx, Path: codexEnvKey, Existed: existed, PriorType: priorType, PriorValue: priorValue,
		})
		entry = &s.journal.Entries[len(s.journal.Entries)-1]
	}
	entry.AfterSHA = sha([]byte(key))
	if err := saveJournal(s.journal); err != nil {
		return fmt.Errorf("save the undo journal: %w", err)
	}
	if _, stderr, err := env.Run(context.Background(), "setx", codexEnvKey, key); err != nil {
		return fmt.Errorf("could not set %s: %s", codexEnvKey, firstLine(stderr, err))
	}
	fmt.Fprintln(s.out, "  changed: user environment variable "+codexEnvKey+" (open a new terminal to use it)")
	return nil
}

// readUserEnv reads a variable from the user's saved environment, which is not the
// same as this process's environment.
func readUserEnv(env Env, name string) (valueType, value string, ok bool) {
	stdout, _, err := env.Run(context.Background(), "reg", "query", `HKCU\Environment`, "/v", name)
	if err != nil {
		return "", "", false
	}
	return parseRegQuery(string(stdout), name)
}

// parseRegQuery reads `reg query` output: a line of name, type and value separated
// by runs of spaces. The value may itself contain spaces.
func parseRegQuery(output, name string) (valueType, value string, ok bool) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimRight(strings.TrimLeft(line, " \t"), "\r")
		rest, found := strings.CutPrefix(line, name)
		if !found || !strings.HasPrefix(rest, " ") {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 || !strings.HasPrefix(fields[0], "REG_") {
			continue
		}
		valueType = fields[0]
		_, after, _ := strings.Cut(rest, valueType)
		return valueType, strings.TrimLeft(after, " \t"), true
	}
	return "", "", false
}

// undoUserEnv puts the variable back as it was, unless the user has changed it
// since, in which case their value wins.
func undoUserEnv(ctx context.Context, env Env, entry JournalEntry, out io.Writer) error {
	_, current, exists := readUserEnv(env, codexEnvKey)
	if !exists {
		fmt.Fprintln(out, "  skipped: user environment variable "+codexEnvKey+" is already gone")
		return nil
	}
	if entry.AfterSHA != "" && sha([]byte(current)) != entry.AfterSHA {
		fmt.Fprintln(out, "  skipped: user environment variable "+codexEnvKey+" was changed after connect, so it was left as is")
		return nil
	}
	if entry.Existed {
		if _, stderr, err := env.Run(ctx, "reg", "add", `HKCU\Environment`, "/v", codexEnvKey, "/t", entry.PriorType, "/d", entry.PriorValue, "/f"); err != nil {
			return fmt.Errorf("could not restore it: %s", firstLine(stderr, err))
		}
		fmt.Fprintln(out, "  restored: user environment variable "+codexEnvKey)
		return nil
	}
	if _, stderr, err := env.Run(ctx, "reg", "delete", `HKCU\Environment`, "/v", codexEnvKey, "/f"); err != nil {
		return fmt.Errorf("could not remove it: %s", firstLine(stderr, err))
	}
	fmt.Fprintln(out, "  removed: user environment variable "+codexEnvKey)
	return nil
}

func (s *session) connectClaude(env Env, hub, key string) error {
	path := env.claudeSettings()
	original, err := readOrEmpty(path)
	if err != nil {
		return err
	}
	merged, err := MergeClaude(original, hub, key)
	if err != nil {
		return err
	}
	// The file now holds a secret, so it is owner-only whatever it was before.
	return s.apply(kindClaude, path, merged, 0o600, "Claude Code settings "+path)
}

func (s *session) connectT3(env Env, hub, key string) error {
	path := env.t3Settings()
	original, err := readOrEmpty(path)
	if err != nil {
		return err
	}
	merged, err := MergeT3(original, hub)
	if err != nil {
		return err
	}
	// Secrets go first, so the settings never reference a secret that is not there.
	for _, secret := range []struct{ instance, name string }{
		{t3CodexInstance, codexEnvKey},
		{t3ClaudeInstance, claudeToken},
	} {
		target := T3SecretPath(env.t3Userdata(), secret.instance, secret.name)
		if err := s.apply(kindSecret, target, []byte(key), 0o600, "T3 Code secret for "+secret.instance+" "+secret.name); err != nil {
			return err
		}
	}
	return s.apply(kindT3, path, merged, modeOr(path, 0o600), "T3 Code settings "+path)
}

// Undo reverses everything in the journal.
func Undo(ctx context.Context, env Env, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	journal, err := loadJournal()
	if err != nil {
		return err
	}
	if len(journal.Entries) == 0 {
		fmt.Fprintln(out, "Nothing to undo: `tpr connect` has not changed anything here.")
		return nil
	}
	var failures []error
	for i := len(journal.Entries) - 1; i >= 0; i-- {
		entry := journal.Entries[i]
		if err := undoEntry(ctx, env, entry, out); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", entry.Path, err))
		}
	}
	if len(failures) > 0 {
		// Keep the journal and backups, so the user can retry.
		return errors.Join(failures...)
	}
	if err := os.RemoveAll(connectDir()); err != nil {
		return err
	}
	fmt.Fprintln(out, "Undone. Restart any tool that was running.")
	return nil
}

func undoEntry(ctx context.Context, env Env, entry JournalEntry, out io.Writer) error {
	if entry.Kind == kindSetx {
		if env.Run == nil {
			return errors.New("no way to change the user environment")
		}
		return undoUserEnv(ctx, env, entry, out)
	}
	current, err := os.ReadFile(entry.Path)
	if errors.Is(err, os.ErrNotExist) {
		if !entry.Existed {
			removeCreatedDirs(entry)
			return nil
		}
		// It existed before connect and is gone now: the user removed it on purpose.
		fmt.Fprintf(out, "  skipped: %s no longer exists\n", entry.Path)
		return nil
	}
	if err != nil {
		return err
	}
	original := []byte(nil)
	if entry.Existed {
		if original, err = os.ReadFile(entry.Backup); err != nil {
			return fmt.Errorf("read the backup: %w", err)
		}
	}
	if sha(current) == entry.AfterSHA {
		if !entry.Existed {
			if err := os.Remove(entry.Path); err != nil {
				return err
			}
			removeCreatedDirs(entry)
		} else if err := setup.AtomicWrite(entry.Path, original, os.FileMode(entry.Mode)); err != nil {
			return err
		}
		fmt.Fprintf(out, "  restored: %s\n", entry.Path)
		return nil
	}
	// The file was edited after connect ran. Taking only TPR's parts back out keeps
	// those edits.
	updated, err := removeOurParts(entry, current, original)
	if err != nil {
		return err
	}
	if err := setup.AtomicWrite(entry.Path, updated, modeOr(entry.Path, 0o600)); err != nil {
		return err
	}
	fmt.Fprintf(out, "  removed TPR's settings from %s (it changed since connect ran, so your edits were kept)\n", entry.Path)
	return nil
}

func removeOurParts(entry JournalEntry, current, original []byte) ([]byte, error) {
	switch entry.Kind {
	case kindCodex:
		return UnmergeCodex(current, original)
	case kindClaude:
		return UnmergeClaude(current, original)
	case kindT3:
		return UnmergeT3(current)
	case kindProfile:
		return removeProfileLine(current), nil
	default:
		// A secret or the env file only ever holds what connect wrote, so a change
		// means the user replaced it. Theirs to keep.
		return current, nil
	}
}

func removeProfileLine(current []byte) []byte {
	lines := strings.SplitAfter(string(current), "\n")
	var kept strings.Builder
	for _, line := range lines {
		if !strings.Contains(line, profileMarker) {
			kept.WriteString(line)
		}
	}
	return []byte(kept.String())
}

func removeCreatedDirs(entry JournalEntry) {
	for _, dir := range entry.CreatedDirs {
		// os.Remove only removes empty folders, so anything else that moved in stays.
		if err := os.Remove(dir); err != nil {
			return
		}
	}
}

func firstLine(stderr []byte, err error) string {
	for _, line := range strings.Split(string(stderr), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	if err != nil {
		return err.Error()
	}
	return "unknown error"
}
