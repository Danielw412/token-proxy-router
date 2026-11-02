package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/service"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/setup"
)

// Update states, matching the GET /update response.
const (
	StateIdle        = "idle"
	StateDownloading = "downloading"
	StateVerifying   = "verifying"
	StateApplying    = "applying"
	StateRestarting  = "restarting"
	StateFailed      = "failed"
)

const (
	// EnvHealthTimeout overrides how long a new version gets to become healthy.
	EnvHealthTimeout = "TPR_UPDATE_HEALTH_TIMEOUT"

	defaultHealthTimeout = 90 * time.Second
	keepSnapshots        = 10
	versionProbeTimeout  = 15 * time.Second
)

// Progress is the persisted state of the current or last update.
type Progress struct {
	State     string `json:"state"`
	Error     string `json:"error,omitempty"`
	From      string `json:"from,omitempty"`
	To        string `json:"to,omitempty"`
	Message   string `json:"message,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// Pending marks a swapped binary that has not yet proved itself.
type Pending struct {
	Target   string `json:"target"`
	Previous string `json:"previous"`
	From     string `json:"from"`
	To       string `json:"to"`
	// Rollback is set when this swap is itself a rollback, which has nothing left
	// to fall back to.
	Rollback bool `json:"rollback,omitempty"`
}

// PreviousInfo describes the kept rollback copy.
type PreviousInfo struct {
	Version string `json:"version"`
	SavedAt string `json:"saved_at"`
}

// Updater performs updates. The zero value is not usable; call New.
type Updater struct {
	Source  *ReleaseSource
	Current string
	// Target is the binary to replace.
	Target string
	// ProbeVersion runs the new binary's `version` command before it replaces the
	// current one. Nil means the real thing.
	ProbeVersion func(ctx context.Context, path string) error
	// RenameAside is true where a running executable cannot be overwritten.
	RenameAside bool
	// Asset is the release file name for this build.
	Asset string
}

// New returns an updater for the running binary.
func New(current string) (*Updater, error) {
	target, err := TargetPath()
	if err != nil {
		return nil, err
	}
	return &Updater{
		Source:      NewReleaseSource(),
		Current:     current,
		Target:      target,
		RenameAside: runtime.GOOS == "windows",
		Asset:       currentAsset(),
	}, nil
}

// TargetPath returns the real path of the running binary.
func TargetPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("find the running binary: %w", err)
	}
	if resolved, errResolve := filepath.EvalSymlinks(exe); errResolve == nil {
		exe = resolved
	}
	return exe, nil
}

// PreviousPath returns the rollback copy for a target's file name.
func PreviousPath() string {
	name := "tpr.previous"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(home.BinDir(), name)
}

func progressPath() string { return filepath.Join(home.DataDir(), "update-state.json") }
func pendingPath() string  { return filepath.Join(home.DataDir(), "update-pending.json") }
func previousInfoPath() string {
	return filepath.Join(home.BinDir(), "tpr.previous.json")
}

var progressMu sync.Mutex

// LoadProgress returns the persisted update state.
func LoadProgress() Progress {
	progressMu.Lock()
	defer progressMu.Unlock()
	raw, err := os.ReadFile(progressPath())
	if err != nil {
		return Progress{State: StateIdle}
	}
	var progress Progress
	if json.Unmarshal(raw, &progress) != nil || progress.State == "" {
		return Progress{State: StateIdle}
	}
	return progress
}

func saveProgress(progress Progress) {
	progress.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	raw, err := json.Marshal(progress)
	if err != nil {
		return
	}
	progressMu.Lock()
	defer progressMu.Unlock()
	_ = setup.AtomicWrite(progressPath(), raw, 0o600)
}

// LoadPending returns the unproved swap, or nil.
func LoadPending() *Pending {
	raw, err := os.ReadFile(pendingPath())
	if err != nil {
		return nil
	}
	var pending Pending
	if json.Unmarshal(raw, &pending) != nil || pending.Target == "" {
		return nil
	}
	return &pending
}

func savePending(pending Pending) error {
	raw, err := json.Marshal(pending)
	if err != nil {
		return err
	}
	return setup.AtomicWrite(pendingPath(), raw, 0o600)
}

func clearPending() { _ = os.Remove(pendingPath()) }

// LoadPreviousInfo returns what is known about the rollback copy.
func LoadPreviousInfo() (PreviousInfo, bool) {
	if _, err := os.Stat(PreviousPath()); err != nil {
		return PreviousInfo{}, false
	}
	var info PreviousInfo
	raw, err := os.ReadFile(previousInfoPath())
	if err == nil {
		_ = json.Unmarshal(raw, &info)
	}
	return info, true
}

// Plan is a resolved update: which release, and why it is allowed.
type Plan struct {
	Release Release
	Tag     string
}

// Resolve picks the release to install. Without an explicit version it refuses
// anything that is not strictly newer than what is running.
func (u *Updater) Resolve(ctx context.Context, version string) (Plan, error) {
	version = strings.TrimSpace(version)
	if version != "" {
		if !IsRelease(version) {
			return Plan{}, fmt.Errorf("%q is not a version; use the form vX.Y.Z", version)
		}
		if !strings.HasPrefix(version, "v") {
			version = "v" + version
		}
		release, err := u.Source.Tagged(ctx, version)
		if err != nil {
			return Plan{}, fmt.Errorf("find release %s: %w", version, err)
		}
		return Plan{Release: release, Tag: release.Tag}, nil
	}
	if !IsRelease(u.Current) {
		return Plan{}, fmt.Errorf("this is a development build (%s), so it will not replace itself. To install a release anyway, use `tpr update --version vX.Y.Z`", u.Current)
	}
	release, err := u.Source.Latest(ctx)
	if err != nil {
		return Plan{}, fmt.Errorf("check for updates: %w", err)
	}
	if !Newer(release.Tag, u.Current) {
		return Plan{}, fmt.Errorf("%w: %s is the latest and you have %s", ErrUpToDate, release.Tag, u.Current)
	}
	return Plan{Release: release, Tag: release.Tag}, nil
}

// ErrUpToDate means there is nothing newer to install.
var ErrUpToDate = errors.New("already up to date")

// Download fetches and verifies a release binary into the target's folder. It
// returns the verified temp file, and removes it on any failure.
func (u *Updater) Download(ctx context.Context, plan Plan, onVerify func()) (string, error) {
	asset, ok := plan.Release.asset(u.Asset)
	if !ok {
		return "", fmt.Errorf("release %s has no build for this machine (%s)", plan.Tag, u.Asset)
	}
	checksums, ok := plan.Release.asset(checksumsAsset)
	if !ok {
		return "", fmt.Errorf("release %s has no %s, so it cannot be verified", plan.Tag, checksumsAsset)
	}
	var sums bytes.Buffer
	if err := u.Source.download(ctx, checksums.URL, &sums, maxMetadataSize); err != nil {
		return "", fmt.Errorf("get checksums: %w", err)
	}
	want, ok := ParseChecksums(sums.String())[u.Asset]
	if !ok {
		return "", fmt.Errorf("%s does not list %s, so it cannot be verified", checksumsAsset, u.Asset)
	}
	tmp, err := os.CreateTemp(filepath.Dir(u.Target), ".tpr-new-*")
	if err != nil {
		return "", fmt.Errorf("the folder %s is not writable: %w", filepath.Dir(u.Target), err)
	}
	tmpName := tmp.Name()
	fail := func(errFail error) (string, error) {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return "", errFail
	}
	hasher := sha256.New()
	if err := u.Source.download(ctx, asset.URL, io.MultiWriter(tmp, hasher), maxBinarySize); err != nil {
		return fail(err)
	}
	if onVerify != nil {
		onVerify()
	}
	got := hex.EncodeToString(hasher.Sum(nil))
	if got != want {
		return fail(fmt.Errorf("checksum mismatch for %s: the download is damaged or was tampered with, so nothing was changed", u.Asset))
	}
	if err := tmp.Chmod(0o755); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return "", err
	}
	return tmpName, nil
}

func (u *Updater) probe(ctx context.Context, path string) error {
	if u.ProbeVersion != nil {
		return u.ProbeVersion(ctx, path)
	}
	ctx, cancel := context.WithTimeout(ctx, versionProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "version")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// Swap keeps the current binary as the rollback copy and moves the new one into
// place in one step. On a failure the current binary stays where it was.
func (u *Updater) Swap(newPath, fromVersion string) error {
	if err := copyFile(u.Target, PreviousPath()); err != nil {
		return fmt.Errorf("keep the current version for rollback: %w", err)
	}
	raw, err := json.Marshal(PreviousInfo{Version: fromVersion, SavedAt: time.Now().UTC().Format(time.RFC3339)})
	if err == nil {
		_ = setup.AtomicWrite(previousInfoPath(), raw, 0o600)
	}
	return replaceBinary(newPath, u.Target, u.RenameAside)
}

// replaceBinary moves src over dst. A running file on Windows can be renamed but
// not overwritten, so it goes aside first and is cleaned up on a later run.
func replaceBinary(src, dst string, renameAside bool) error {
	if !renameAside {
		return os.Rename(src, dst)
	}
	aside := dst + ".old-" + time.Now().UTC().Format("20060102150405.000000000")
	if err := os.Rename(dst, aside); err != nil {
		return fmt.Errorf("move the running binary aside: %w", err)
	}
	if err := os.Rename(src, dst); err != nil {
		_ = os.Rename(aside, dst)
		return err
	}
	return nil
}

// InstallBinary copies src to dst in one step, even when dst is running.
func InstallBinary(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".tpr-install-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(tmpName) }()
	if err := copyFile(src, tmpName); err != nil {
		return err
	}
	_, statErr := os.Stat(dst)
	return replaceBinary(tmpName, dst, runtime.GOOS == "windows" && statErr == nil)
}

// CleanupAside removes binaries left behind by earlier Windows swaps. Ones still
// in use stay until a later run.
func CleanupAside(target string) {
	matches, err := filepath.Glob(target + ".old-*")
	if err != nil {
		return
	}
	for _, match := range matches {
		_ = os.Remove(match)
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".tpr-copy-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, dst); err == nil {
		return nil
	}
	// Windows refuses to replace a file that is in use.
	if errRemove := os.Remove(dst); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
		return errRemove
	}
	return os.Rename(tmpName, dst)
}

// SnapshotConfig copies the config into backups/ and prunes old copies.
func SnapshotConfig(configPath, label string) (string, error) {
	raw, err := os.ReadFile(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read config for snapshot: %w", err)
	}
	stamp := time.Now().UTC().Format("20060102-150405")
	name := fmt.Sprintf("config-%s-%s.yaml", stamp, sanitizeLabel(label))
	path := filepath.Join(home.BackupsDir(), name)
	if err := setup.AtomicWrite(path, raw, 0o600); err != nil {
		return "", fmt.Errorf("write config snapshot: %w", err)
	}
	pruneSnapshots()
	return path, nil
}

func sanitizeLabel(label string) string {
	var b strings.Builder
	for _, r := range label {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

func pruneSnapshots() {
	matches, err := filepath.Glob(filepath.Join(home.BackupsDir(), "config-*.yaml"))
	if err != nil || len(matches) <= keepSnapshots {
		return
	}
	sort.Strings(matches)
	for _, stale := range matches[:len(matches)-keepSnapshots] {
		_ = os.Remove(stale)
	}
}

// ApplyOptions tune Apply.
type ApplyOptions struct {
	// ConfigPath is snapshotted before anything changes.
	ConfigPath string
	// Version installs an exact release, which also allows a downgrade.
	Version string
}

// Result reports what Apply did.
type Result struct {
	From string
	To   string
	// Restarting is true when the supervisor will restart and health-check the
	// new version. Otherwise the swap alone is the whole update.
	Restarting bool
}

// Apply downloads, verifies and installs a release, then hands over to the
// supervisor if one is running. Progress is persisted so that the panel can follow.
func (u *Updater) Apply(ctx context.Context, opts ApplyOptions) (Result, error) {
	CleanupAside(u.Target)
	result := Result{From: u.Current}
	release, errLock := acquireLock()
	if errLock != nil {
		return result, errLock
	}
	defer release()
	if pending := LoadPending(); pending != nil && service.SupervisorRunning() {
		return result, errors.New("another update is still being checked; wait for it to finish")
	}
	fail := func(err error) (Result, error) {
		saveProgress(Progress{State: StateFailed, Error: err.Error(), From: u.Current, To: result.To})
		return result, err
	}
	saveProgress(Progress{State: StateDownloading, From: u.Current})
	plan, err := u.Resolve(ctx, opts.Version)
	if err != nil {
		if errors.Is(err, ErrUpToDate) {
			saveProgress(Progress{State: StateIdle, From: u.Current})
			return result, err
		}
		return fail(err)
	}
	result.To = plan.Tag
	saveProgress(Progress{State: StateDownloading, From: u.Current, To: plan.Tag})
	newPath, err := u.Download(ctx, plan, func() {
		saveProgress(Progress{State: StateVerifying, From: u.Current, To: plan.Tag})
	})
	if err != nil {
		return fail(err)
	}
	defer func() { _ = os.Remove(newPath) }()
	saveProgress(Progress{State: StateApplying, From: u.Current, To: plan.Tag})
	if err := u.probe(ctx, newPath); err != nil {
		return fail(fmt.Errorf("the new version does not run on this machine, so nothing was changed: %w", err))
	}
	if opts.ConfigPath != "" {
		if _, err := SnapshotConfig(opts.ConfigPath, "pre-update-"+plan.Tag); err != nil {
			return fail(err)
		}
	}
	if err := u.Swap(newPath, u.Current); err != nil {
		return fail(fmt.Errorf("install the new version: %w", err))
	}
	if !service.SupervisorRunning() {
		// A marker from an interrupted update must not trigger a later rollback.
		clearPending()
		saveProgress(Progress{State: StateIdle, From: u.Current, To: plan.Tag, Message: "installed"})
		return result, nil
	}
	if err := savePending(Pending{Target: u.Target, Previous: PreviousPath(), From: u.Current, To: plan.Tag}); err != nil {
		return fail(fmt.Errorf("record the update: %w", err))
	}
	saveProgress(Progress{State: StateRestarting, From: u.Current, To: plan.Tag})
	if err := service.RequestRestart(); err != nil {
		clearPending()
		return fail(fmt.Errorf("ask the supervisor to restart: %w", err))
	}
	result.Restarting = true
	return result, nil
}

// Rollback puts the previous version back. The two versions trade places, so the
// version being left stays available as the fallback: if the older one no longer
// starts, for instance because it cannot read the current config, the health check
// returns to the newer one instead of leaving the service down.
func (u *Updater) Rollback() (Result, error) {
	result := Result{From: u.Current}
	release, errLock := acquireLock()
	if errLock != nil {
		return result, errLock
	}
	defer release()
	// The supervisor is checking an update and may roll it back itself. Swapping the
	// binaries under it could reinstall the broken one and lose the working one.
	if pending := LoadPending(); pending != nil && service.SupervisorRunning() {
		return result, errors.New("an update is still being checked; wait for it to finish, then roll back if you still need to")
	}
	info, ok := LoadPreviousInfo()
	if !ok {
		return result, errors.New("there is no previous version to go back to")
	}
	result.To = info.Version
	if err := u.exchangeWithPrevious(u.Current); err != nil {
		saveProgress(Progress{State: StateFailed, Error: err.Error(), From: u.Current, To: info.Version})
		return result, fmt.Errorf("restore the previous version: %w", err)
	}
	if !service.SupervisorRunning() {
		saveProgress(Progress{State: StateIdle, From: u.Current, To: info.Version, Message: "rolled back"})
		return result, nil
	}
	if err := savePending(Pending{Target: u.Target, Previous: PreviousPath(), From: u.Current, To: info.Version, Rollback: true}); err != nil {
		return result, fmt.Errorf("record the rollback: %w", err)
	}
	saveProgress(Progress{State: StateRestarting, From: u.Current, To: info.Version})
	if err := service.RequestRestart(); err != nil {
		clearPending()
		return result, fmt.Errorf("ask the supervisor to restart: %w", err)
	}
	result.Restarting = true
	return result, nil
}

// exchangeWithPrevious swaps the target and the rollback copy.
func (u *Updater) exchangeWithPrevious(currentVersion string) error {
	restore, err := os.CreateTemp(filepath.Dir(u.Target), ".tpr-restore-*")
	if err != nil {
		return fmt.Errorf("the folder %s is not writable: %w", filepath.Dir(u.Target), err)
	}
	restoreName := restore.Name()
	_ = restore.Close()
	defer func() { _ = os.Remove(restoreName) }()
	if err := copyFile(PreviousPath(), restoreName); err != nil {
		return err
	}
	return u.Swap(restoreName, currentVersion)
}

func healthTimeout() time.Duration {
	if raw := strings.TrimSpace(os.Getenv(EnvHealthTimeout)); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
			return parsed
		}
	}
	return defaultHealthTimeout
}

// PendingCheck is the supervisor hook. It returns the health check for a newly
// swapped binary, and rolls back when that check fails.
func PendingCheck() *service.PendingCheck {
	pending := LoadPending()
	if pending == nil {
		return nil
	}
	return &service.PendingCheck{
		Timeout: healthTimeout(),
		OnHealthy: func() {
			// The result goes first: waiters treat the marker's removal as "finished".
			saveProgress(Progress{State: StateIdle, From: pending.From, To: pending.To, Message: "updated"})
			clearPending()
		},
		OnFailed: func(reason string) { autoRollback(*pending, reason) },
	}
}

func autoRollback(pending Pending, reason string) {
	// The result goes first: waiters treat the marker's removal as "finished".
	defer clearPending()
	if pending.Previous == "" {
		saveProgress(Progress{State: StateFailed, Error: "the new version failed and there is nothing to return to: " + reason, From: pending.From, To: pending.To})
		return
	}
	message := fmt.Sprintf("%s failed its health check and was rolled back to %s: %s", pending.To, pending.From, reason)
	if pending.Rollback {
		message = fmt.Sprintf("%s failed its health check, so the service stayed on %s: %s", pending.To, pending.From, reason)
	}
	tmp, err := os.CreateTemp(filepath.Dir(pending.Target), ".tpr-restore-*")
	if err == nil {
		tmpName := tmp.Name()
		_ = tmp.Close()
		defer func() { _ = os.Remove(tmpName) }()
		if err = copyFile(pending.Previous, tmpName); err == nil {
			err = replaceBinary(tmpName, pending.Target, runtime.GOOS == "windows")
		}
	}
	if err != nil {
		saveProgress(Progress{State: StateFailed, Error: fmt.Sprintf("%s failed its health check and the automatic rollback also failed: %v. The previous version is at %s", pending.To, err, pending.Previous), From: pending.From, To: pending.To})
		return
	}
	// The copy is now identical to what runs, so it is no longer something to go
	// back to.
	_ = os.Remove(pending.Previous)
	_ = os.Remove(previousInfoPath())
	saveProgress(Progress{State: StateFailed, Error: message, From: pending.From, To: pending.To})
}
