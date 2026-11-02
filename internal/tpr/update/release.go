// Package update checks GitHub releases and swaps the binary: checksummed
// download, atomic swap, restart through the supervisor, health check, automatic
// rollback. Machines with no service use the same code, minus the restart.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	// Repo is where releases are published.
	Repo = "Danielw412/token-proxy-router"
	// EnvBaseURL points the updater at another release server. It exists for
	// end-to-end tests. Checksums are still verified against what that server says,
	// so only set it for a server you control.
	EnvBaseURL = "TPR_UPDATE_BASE_URL"

	defaultAPIBase  = "https://api.github.com"
	checksumsAsset  = "checksums.txt"
	checkTimeout    = 15 * time.Second
	downloadTimeout = 15 * time.Minute
	maxBinarySize   = 400 << 20
	maxMetadataSize = 8 << 20
	userAgent       = "TokenProxyRouter-updater"
)

// Version metadata stamped into release builds by the Makefile and release workflow.
var (
	UpstreamVersion = "v8.0.52"
	PanelVersion    = "v1.27.2"
)

// ErrRateLimited means GitHub refused the check for now.
var ErrRateLimited = errors.New("GitHub rate limit reached; try again later")

// ErrNoRelease means no release exists yet, or none with that version.
var ErrNoRelease = errors.New("release not found")

// Asset is one file attached to a release.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// Release is the part of a GitHub release the updater uses.
type Release struct {
	Tag     string  `json:"tag_name"`
	HTMLURL string  `json:"html_url"`
	Body    string  `json:"body"`
	Assets  []Asset `json:"assets"`
}

func (r Release) asset(name string) (Asset, bool) {
	for _, asset := range r.Assets {
		if asset.Name == name {
			return asset, true
		}
	}
	return Asset{}, false
}

// AssetName returns the release file for an OS and architecture.
func AssetName(goos, goarch string) string {
	name := "tpr-" + goos + "-" + goarch
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

// ReleaseSource talks to the release server.
type ReleaseSource struct {
	BaseURL string
	Client  *http.Client
}

// NewReleaseSource returns the source for GitHub, or for EnvBaseURL when set.
func NewReleaseSource() *ReleaseSource {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv(EnvBaseURL)), "/")
	if base == "" {
		base = defaultAPIBase
	}
	return &ReleaseSource{BaseURL: base, Client: &http.Client{}}
}

// fetchResult carries the conditional-request outcome of a metadata call.
type fetchResult struct {
	release     Release
	etag        string
	notModified bool
}

func (s *ReleaseSource) fetch(ctx context.Context, path, etag string) (fetchResult, error) {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.BaseURL+"/repos/"+Repo+"/releases/"+path, nil)
	if err != nil {
		return fetchResult{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", userAgent)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return fetchResult{}, fmt.Errorf("could not reach the release server: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotModified:
		return fetchResult{notModified: true, etag: etag}, nil
	case http.StatusNotFound:
		return fetchResult{}, ErrNoRelease
	case http.StatusForbidden, http.StatusTooManyRequests:
		return fetchResult{}, ErrRateLimited
	default:
		return fetchResult{}, fmt.Errorf("release server answered %d", resp.StatusCode)
	}
	var release Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxMetadataSize)).Decode(&release); err != nil {
		return fetchResult{}, fmt.Errorf("could not read release data: %w", err)
	}
	if release.Tag == "" {
		return fetchResult{}, errors.New("release data has no version")
	}
	return fetchResult{release: release, etag: resp.Header.Get("ETag")}, nil
}

// Latest returns the newest published release.
func (s *ReleaseSource) Latest(ctx context.Context) (Release, error) {
	result, err := s.fetch(ctx, "latest", "")
	return result.release, err
}

// Tagged returns the release with an exact tag.
func (s *ReleaseSource) Tagged(ctx context.Context, tag string) (Release, error) {
	result, err := s.fetch(ctx, "tags/"+tag, "")
	return result.release, err
}

// download fetches a release file to w, up to limit bytes.
func (s *ReleaseSource) download(ctx context.Context, url string, w io.Writer, limit int64) error {
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := s.Client.Do(req)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download answered %d", resp.StatusCode)
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	if n > limit {
		return fmt.Errorf("download is larger than the %d byte limit", limit)
	}
	return nil
}

// ParseChecksums reads `sha256sum` output into file name → lowercase hex digest.
func ParseChecksums(raw string) map[string]string {
	sums := map[string]string{}
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		digest := strings.ToLower(fields[0])
		if len(digest) != 64 {
			continue
		}
		sums[strings.TrimPrefix(fields[1], "*")] = digest
	}
	return sums
}

var semverPattern = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$`)

type semver struct {
	parts [3]int
	pre   string
}

func parseSemver(raw string) (semver, bool) {
	match := semverPattern.FindStringSubmatch(strings.TrimSpace(raw))
	if match == nil {
		return semver{}, false
	}
	var parsed semver
	for i := range parsed.parts {
		value, err := strconv.Atoi(match[i+1])
		if err != nil {
			return semver{}, false
		}
		parsed.parts[i] = value
	}
	parsed.pre = match[4]
	return parsed, true
}

// IsRelease reports whether a version string is a stamped release version, rather
// than something like "dev".
func IsRelease(version string) bool {
	_, ok := parseSemver(version)
	return ok
}

// Newer reports whether candidate is a later version than current. Versions that
// cannot be parsed are never newer.
func Newer(candidate, current string) bool {
	next, okNext := parseSemver(candidate)
	now, okNow := parseSemver(current)
	if !okNext || !okNow {
		return false
	}
	for i := range next.parts {
		if next.parts[i] != now.parts[i] {
			return next.parts[i] > now.parts[i]
		}
	}
	if next.pre == now.pre {
		return false
	}
	if next.pre == "" {
		return true
	}
	if now.pre == "" {
		return false
	}
	return comparePre(next.pre, now.pre) > 0
}

// comparePre orders pre-release identifiers per the semver rules.
func comparePre(a, b string) int {
	left, right := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(left) && i < len(right); i++ {
		if left[i] == right[i] {
			continue
		}
		leftNum, errLeft := strconv.Atoi(left[i])
		rightNum, errRight := strconv.Atoi(right[i])
		switch {
		case errLeft == nil && errRight == nil:
			if leftNum < rightNum {
				return -1
			}
			return 1
		case errLeft == nil:
			return -1
		case errRight == nil:
			return 1
		}
		return strings.Compare(left[i], right[i])
	}
	return len(left) - len(right)
}

// currentAsset names the release file for this build.
func currentAsset() string { return AssetName(runtime.GOOS, runtime.GOARCH) }
