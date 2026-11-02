package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/home"
)

const testAsset = "tpr-testos-testarch"

type fakeReleases struct {
	tag         string
	binary      []byte
	checksums   string
	status      int
	noChecksums bool
	etagHits    atomic.Int32
	latestHits  atomic.Int32
	server      *httptest.Server
}

func (f *fakeReleases) start(t *testing.T) {
	t.Helper()
	mux := http.NewServeMux()
	release := func(w http.ResponseWriter, r *http.Request) {
		f.latestHits.Add(1)
		if f.status != 0 {
			w.WriteHeader(f.status)
			return
		}
		if r.Header.Get("If-None-Match") == `"tag-1"` {
			f.etagHits.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		assets := []Asset{{Name: testAsset, URL: f.server.URL + "/dl/" + testAsset}}
		if !f.noChecksums {
			assets = append(assets, Asset{Name: checksumsAsset, URL: f.server.URL + "/dl/" + checksumsAsset})
		}
		w.Header().Set("ETag", `"tag-1"`)
		_ = json.NewEncoder(w).Encode(Release{Tag: f.tag, HTMLURL: "https://example.com/notes", Body: "notes", Assets: assets})
	}
	mux.HandleFunc("/repos/"+Repo+"/releases/latest", release)
	mux.HandleFunc("/repos/"+Repo+"/releases/tags/", release)
	mux.HandleFunc("/dl/"+testAsset, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(f.binary) })
	mux.HandleFunc("/dl/"+checksumsAsset, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(f.checksums)) })
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
}

func checksumLine(data []byte, name string) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]) + "  " + name + "\n"
}

func testUpdater(t *testing.T, current string, releases *fakeReleases) *Updater {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(home.EnvHome, filepath.Join(dir, "data"))
	if err := home.Ensure(); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "tpr")
	if err := os.WriteFile(target, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	releases.start(t)
	return &Updater{
		Source:       &ReleaseSource{BaseURL: releases.server.URL, Client: releases.server.Client()},
		Current:      current,
		Target:       target,
		Asset:        testAsset,
		ProbeVersion: func(context.Context, string) error { return nil },
	}
}

func readTarget(t *testing.T, u *Updater) string {
	t.Helper()
	raw, err := os.ReadFile(u.Target)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func noTempFilesLeft(t *testing.T, u *Updater) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(u.Target))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".tpr-") {
			t.Fatalf("leftover temp file %s", entry.Name())
		}
	}
}

func TestApplySwapsAndKeepsRollbackCopy(t *testing.T) {
	binary := []byte("new binary")
	releases := &fakeReleases{tag: "v1.2.0", binary: binary, checksums: checksumLine(binary, testAsset)}
	u := testUpdater(t, "v1.1.0", releases)
	configPath := filepath.Join(home.Dir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("port: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := u.Apply(context.Background(), ApplyOptions{ConfigPath: configPath})
	if err != nil {
		t.Fatal(err)
	}
	if result.To != "v1.2.0" || result.Restarting {
		t.Fatalf("result = %+v", result)
	}
	if readTarget(t, u) != "new binary" {
		t.Fatal("target not swapped")
	}
	previous, err := os.ReadFile(PreviousPath())
	if err != nil || string(previous) != "old binary" {
		t.Fatalf("previous = %q err=%v", previous, err)
	}
	info, ok := LoadPreviousInfo()
	if !ok || info.Version != "v1.1.0" {
		t.Fatalf("info = %+v", info)
	}
	snapshots, _ := filepath.Glob(filepath.Join(home.BackupsDir(), "config-*pre-update-v1.2.0.yaml"))
	if len(snapshots) != 1 {
		t.Fatalf("snapshots = %v", snapshots)
	}
	if got := LoadProgress(); got.State != StateIdle {
		t.Fatalf("progress = %+v", got)
	}
	noTempFilesLeft(t, u)
}

func TestApplyRejectsChecksumMismatch(t *testing.T) {
	releases := &fakeReleases{tag: "v1.2.0", binary: []byte("tampered"), checksums: checksumLine([]byte("the real one"), testAsset)}
	u := testUpdater(t, "v1.1.0", releases)
	_, err := u.Apply(context.Background(), ApplyOptions{})
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v", err)
	}
	if readTarget(t, u) != "old binary" {
		t.Fatal("a bad download must not replace the binary")
	}
	if _, err := os.Stat(PreviousPath()); !os.IsNotExist(err) {
		t.Fatal("nothing should be saved as previous")
	}
	if got := LoadProgress(); got.State != StateFailed || !strings.Contains(got.Error, "checksum mismatch") {
		t.Fatalf("progress = %+v", got)
	}
	noTempFilesLeft(t, u)
}

func TestApplyRefusesWithoutChecksumsFile(t *testing.T) {
	releases := &fakeReleases{tag: "v1.2.0", binary: []byte("x"), noChecksums: true}
	u := testUpdater(t, "v1.1.0", releases)
	if _, err := u.Apply(context.Background(), ApplyOptions{}); err == nil || !strings.Contains(err.Error(), "cannot be verified") {
		t.Fatalf("err = %v", err)
	}
	if readTarget(t, u) != "old binary" {
		t.Fatal("must not swap unverified binary")
	}
}

func TestApplyRefusesWhenChecksumsOmitAsset(t *testing.T) {
	releases := &fakeReleases{tag: "v1.2.0", binary: []byte("x"), checksums: checksumLine([]byte("x"), "other-file")}
	u := testUpdater(t, "v1.1.0", releases)
	if _, err := u.Apply(context.Background(), ApplyOptions{}); err == nil || !strings.Contains(err.Error(), "does not list") {
		t.Fatalf("err = %v", err)
	}
}

func TestApplyRefusesBinaryThatDoesNotRun(t *testing.T) {
	binary := []byte("new binary")
	releases := &fakeReleases{tag: "v1.2.0", binary: binary, checksums: checksumLine(binary, testAsset)}
	u := testUpdater(t, "v1.1.0", releases)
	u.ProbeVersion = func(context.Context, string) error { return errors.New("exec format error") }
	if _, err := u.Apply(context.Background(), ApplyOptions{}); err == nil || !strings.Contains(err.Error(), "does not run") {
		t.Fatalf("err = %v", err)
	}
	if readTarget(t, u) != "old binary" {
		t.Fatal("must not swap")
	}
	noTempFilesLeft(t, u)
}

func TestDowngradeRejectedWithoutExplicitVersion(t *testing.T) {
	binary := []byte("older")
	for _, latest := range []string{"v1.0.0", "v1.1.0"} {
		t.Run(latest, func(t *testing.T) {
			releases := &fakeReleases{tag: latest, binary: binary, checksums: checksumLine(binary, testAsset)}
			u := testUpdater(t, "v1.1.0", releases)
			_, err := u.Apply(context.Background(), ApplyOptions{})
			if !errors.Is(err, ErrUpToDate) {
				t.Fatalf("err = %v", err)
			}
			if readTarget(t, u) != "old binary" {
				t.Fatal("must not swap")
			}
			if got := LoadProgress(); got.State != StateIdle {
				t.Fatalf("up to date is not a failure: %+v", got)
			}
		})
	}
}

func TestExplicitVersionAllowsDowngrade(t *testing.T) {
	binary := []byte("older")
	releases := &fakeReleases{tag: "v1.0.0", binary: binary, checksums: checksumLine(binary, testAsset)}
	u := testUpdater(t, "v1.1.0", releases)
	if _, err := u.Apply(context.Background(), ApplyOptions{Version: "v1.0.0"}); err != nil {
		t.Fatal(err)
	}
	if readTarget(t, u) != "older" {
		t.Fatal("explicit version should install")
	}
}

func TestExplicitVersionMustBeAVersion(t *testing.T) {
	releases := &fakeReleases{tag: "v1.0.0"}
	u := testUpdater(t, "v1.1.0", releases)
	if _, err := u.Apply(context.Background(), ApplyOptions{Version: "../../evil"}); err == nil || !strings.Contains(err.Error(), "not a version") {
		t.Fatalf("err = %v", err)
	}
	if releases.latestHits.Load() != 0 {
		t.Fatal("must not call the server")
	}
}

func TestDevBuildNeedsExplicitVersion(t *testing.T) {
	binary := []byte("new")
	releases := &fakeReleases{tag: "v1.2.0", binary: binary, checksums: checksumLine(binary, testAsset)}
	u := testUpdater(t, "dev", releases)
	if _, err := u.Apply(context.Background(), ApplyOptions{}); err == nil || !strings.Contains(err.Error(), "development build") {
		t.Fatalf("err = %v", err)
	}
	if _, err := u.Apply(context.Background(), ApplyOptions{Version: "v1.2.0"}); err != nil {
		t.Fatal(err)
	}
}

func TestRollbackRestoresThenHasNothingLeft(t *testing.T) {
	binary := []byte("new binary")
	releases := &fakeReleases{tag: "v1.2.0", binary: binary, checksums: checksumLine(binary, testAsset)}
	u := testUpdater(t, "v1.1.0", releases)
	if _, err := u.Rollback(); err == nil || !strings.Contains(err.Error(), "no previous version") {
		t.Fatalf("err = %v", err)
	}
	if _, err := u.Apply(context.Background(), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	u.Current = "v1.2.0"
	result, err := u.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	if result.To != "v1.1.0" || result.Restarting {
		t.Fatalf("result = %+v", result)
	}
	if readTarget(t, u) != "old binary" {
		t.Fatal("target not restored")
	}
	if _, ok := LoadPreviousInfo(); ok {
		t.Fatal("rollback copy should be used up")
	}
	info, err := os.Stat(u.Target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 && filepath.Separator == '/' {
		t.Fatal("restored binary must stay executable")
	}
	noTempFilesLeft(t, u)
}

func TestReplaceBinaryRenameAsideLikeWindows(t *testing.T) {
	dir := t.TempDir()
	dst, src := filepath.Join(dir, "tpr.exe"), filepath.Join(dir, "new")
	if err := os.WriteFile(dst, []byte("running"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("next"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := replaceBinary(src, dst, true); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(dst)
	if string(raw) != "next" {
		t.Fatalf("dst = %q", raw)
	}
	aside, _ := filepath.Glob(dst + ".old-*")
	if len(aside) != 1 {
		t.Fatalf("aside = %v", aside)
	}
	raw, _ = os.ReadFile(aside[0])
	if string(raw) != "running" {
		t.Fatalf("aside content = %q", raw)
	}
	CleanupAside(dst)
	aside, _ = filepath.Glob(dst + ".old-*")
	if len(aside) != 0 {
		t.Fatalf("cleanup left %v", aside)
	}
}

func TestReplaceBinaryRestoresOnFailureLikeWindows(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "tpr.exe")
	if err := os.WriteFile(dst, []byte("running"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := replaceBinary(filepath.Join(dir, "missing"), dst, true); err == nil {
		t.Fatal("expected an error")
	}
	raw, err := os.ReadFile(dst)
	if err != nil || string(raw) != "running" {
		t.Fatalf("dst = %q err=%v", raw, err)
	}
}

func TestPendingCheckHealthyClearsPending(t *testing.T) {
	releases := &fakeReleases{tag: "v1.2.0"}
	u := testUpdater(t, "v1.1.0", releases)
	if PendingCheck() != nil {
		t.Fatal("nothing pending yet")
	}
	if err := savePending(Pending{Target: u.Target, Previous: PreviousPath(), From: "v1.1.0", To: "v1.2.0"}); err != nil {
		t.Fatal(err)
	}
	check := PendingCheck()
	if check == nil {
		t.Fatal("expected a check")
	}
	check.OnHealthy()
	if LoadPending() != nil {
		t.Fatal("pending should clear")
	}
	if got := LoadProgress(); got.State != StateIdle || got.To != "v1.2.0" {
		t.Fatalf("progress = %+v", got)
	}
}

func TestPendingCheckFailureRollsBack(t *testing.T) {
	releases := &fakeReleases{tag: "v1.2.0"}
	u := testUpdater(t, "v1.1.0", releases)
	if err := os.WriteFile(PreviousPath(), []byte("good version"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := savePending(Pending{Target: u.Target, Previous: PreviousPath(), From: "v1.1.0", To: "v1.2.0"}); err != nil {
		t.Fatal(err)
	}
	PendingCheck().OnFailed("did not become healthy")
	if readTarget(t, u) != "good version" {
		t.Fatal("binary should be restored")
	}
	if LoadPending() != nil {
		t.Fatal("pending should clear")
	}
	got := LoadProgress()
	if got.State != StateFailed || !strings.Contains(got.Error, "rolled back to v1.1.0") {
		t.Fatalf("progress = %+v", got)
	}
}

func TestPendingCheckRollbackFailureIsReported(t *testing.T) {
	releases := &fakeReleases{tag: "v1.2.0"}
	u := testUpdater(t, "v1.1.0", releases)
	if err := savePending(Pending{Target: u.Target, Previous: filepath.Join(home.BinDir(), "missing"), From: "v1.1.0", To: "v1.2.0"}); err != nil {
		t.Fatal(err)
	}
	PendingCheck().OnFailed("crashed")
	got := LoadProgress()
	if got.State != StateFailed || !strings.Contains(got.Error, "rollback also failed") {
		t.Fatalf("progress = %+v", got)
	}
}

func TestFailedRollbackDoesNotLoop(t *testing.T) {
	releases := &fakeReleases{tag: "v1.2.0"}
	u := testUpdater(t, "v1.1.0", releases)
	if err := savePending(Pending{Target: u.Target, From: "v1.2.0", To: "v1.1.0", Rollback: true}); err != nil {
		t.Fatal(err)
	}
	PendingCheck().OnFailed("crashed")
	if LoadPending() != nil {
		t.Fatal("pending should clear")
	}
	if got := LoadProgress(); got.State != StateFailed || !strings.Contains(got.Error, "restored version failed") {
		t.Fatalf("progress = %+v", got)
	}
}

func TestCheckCachesAndToleratesRateLimit(t *testing.T) {
	releases := &fakeReleases{tag: "v1.2.0"}
	u := testUpdater(t, "v1.1.0", releases)
	cache := Check(context.Background(), u.Source)
	if cache.Latest != "v1.2.0" || cache.NotesURL == "" || cache.LastError != "" {
		t.Fatalf("cache = %+v", cache)
	}
	// A second check sends the ETag and keeps the answer.
	cache = Check(context.Background(), u.Source)
	if cache.Latest != "v1.2.0" || releases.etagHits.Load() != 1 {
		t.Fatalf("cache=%+v etag hits=%d", cache, releases.etagHits.Load())
	}
	releases.status = http.StatusForbidden
	cache = Check(context.Background(), u.Source)
	if cache.Latest != "v1.2.0" || !strings.Contains(cache.LastError, "rate limit") {
		t.Fatalf("a rate limit must keep the last answer: %+v", cache)
	}
	report := BuildReport("v1.1.0")
	if !report.Available || report.Latest != "v1.2.0" || !strings.Contains(report.Error, "rate limit") {
		t.Fatalf("report = %+v", report)
	}
}

func TestCheckNoReleasesYet(t *testing.T) {
	releases := &fakeReleases{tag: "v1.2.0", status: http.StatusNotFound}
	u := testUpdater(t, "v1.1.0", releases)
	cache := Check(context.Background(), u.Source)
	if cache.Latest != "" || cache.LastError != "" || cache.CheckedAt == "" {
		t.Fatalf("cache = %+v", cache)
	}
	if report := BuildReport("v1.1.0"); report.Available {
		t.Fatalf("report = %+v", report)
	}
}

func TestCanApplyRequiresSupervisorAndRelease(t *testing.T) {
	releases := &fakeReleases{tag: "v1.2.0"}
	testUpdater(t, "v1.1.0", releases)
	if ok, reason := canApply("v1.1.0"); ok || !strings.Contains(reason, "not running as a service") {
		t.Fatalf("ok=%v reason=%q", ok, reason)
	}
	t.Setenv("TPR_SUPERVISED", "1")
	if ok, reason := canApply("dev"); ok || !strings.Contains(reason, "development build") {
		t.Fatalf("ok=%v reason=%q", ok, reason)
	}
	if ok, reason := canApply("v1.1.0"); !ok {
		t.Fatalf("reason=%q", reason)
	}
}

func TestSnapshotConfigPrunesOld(t *testing.T) {
	testUpdater(t, "v1", &fakeReleases{})
	configPath := filepath.Join(home.Dir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := range keepSnapshots + 5 {
		name := fmt.Sprintf("config-2020010%d-000000-x.yaml", i)
		if err := os.WriteFile(filepath.Join(home.BackupsDir(), name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	path, err := SnapshotConfig(configPath, "pre-update-../../evil")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != home.BackupsDir() {
		t.Fatalf("label must not escape the folder: %s", path)
	}
	matches, _ := filepath.Glob(filepath.Join(home.BackupsDir(), "config-*.yaml"))
	if len(matches) != keepSnapshots {
		t.Fatalf("kept %d", len(matches))
	}
	if _, err := SnapshotConfig(filepath.Join(home.Dir(), "missing.yaml"), "x"); err != nil {
		t.Fatalf("a missing config is fine for a client-only machine: %v", err)
	}
}

func TestDownloadEnforcesSizeLimit(t *testing.T) {
	releases := &fakeReleases{tag: "v1.2.0", binary: bytes.Repeat([]byte("x"), 100)}
	u := testUpdater(t, "v1.1.0", releases)
	var out bytes.Buffer
	if err := u.Source.download(context.Background(), releases.server.URL+"/dl/"+testAsset, &out, 10); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("err = %v", err)
	}
}

func TestNewer(t *testing.T) {
	cases := []struct {
		candidate, current string
		want               bool
	}{
		{"v1.2.0", "v1.1.9", true},
		{"v1.10.0", "v1.9.0", true},
		{"v2.0.0", "v1.99.99", true},
		{"v1.1.0", "v1.1.0", false},
		{"v1.0.0", "v1.1.0", false},
		{"1.2.0", "v1.1.0", true},
		{"v1.2.0", "v1.2.0-rc.1", true},
		{"v1.2.0-rc.1", "v1.2.0", false},
		{"v1.2.0-rc.2", "v1.2.0-rc.1", true},
		{"v1.2.0-rc.10", "v1.2.0-rc.9", true},
		{"v1.2.0", "dev", false},
		{"", "v1.0.0", false},
		{"garbage", "v1.0.0", false},
	}
	for _, tc := range cases {
		if got := Newer(tc.candidate, tc.current); got != tc.want {
			t.Errorf("Newer(%q, %q) = %v", tc.candidate, tc.current, got)
		}
	}
}

func TestParseChecksums(t *testing.T) {
	digest := strings.Repeat("A", 64)
	sums := ParseChecksums(digest + "  tpr-linux-amd64\n" + digest + " *tpr-windows-amd64.exe\nbroken line here\nshort file\n")
	if sums["tpr-linux-amd64"] != strings.ToLower(digest) || sums["tpr-windows-amd64.exe"] != strings.ToLower(digest) || len(sums) != 2 {
		t.Fatalf("sums = %v", sums)
	}
}

func TestAssetName(t *testing.T) {
	if AssetName("linux", "arm64") != "tpr-linux-arm64" || AssetName("windows", "amd64") != "tpr-windows-amd64.exe" {
		t.Fatal("asset names changed; the install scripts and release workflow depend on them")
	}
}

func TestStuckUpdateIsReportedAsInterrupted(t *testing.T) {
	testUpdater(t, "v1.1.0", &fakeReleases{})
	raw, err := json.Marshal(Progress{State: StateDownloading, To: "v1.2.0", UpdatedAt: "2020-01-01T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(progressPath(), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	report := BuildReport("v1.1.0")
	if report.State != StateFailed || !strings.Contains(report.Error, "interrupted") {
		t.Fatalf("report = %+v", report)
	}
	saveProgress(Progress{State: StateDownloading, To: "v1.2.0"})
	if report := BuildReport("v1.1.0"); report.State != StateDownloading {
		t.Fatalf("a fresh state must stand: %+v", report)
	}
}
