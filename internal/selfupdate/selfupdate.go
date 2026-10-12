// Package selfupdate implements `bicz update`: download the latest release of
// bicz from its GitHub repository, verify its SHA256 against the release's own
// SHA256SUMS, swap the running binary atomically (keeping the previous one for
// rollback), and re-run the adapter install for every harness that already has
// a bicz config so hook commands and hook timeouts follow the new binary.
//
// The whole flow is user-scope and idempotent. What it deliberately does NOT
// do: no shell, no script download-and-execute, no config edits beyond the
// installer's own diff (identity, ledger, and operator keys survive every
// reinstall), no Windows (the archive layout differs; update via the ps1
// installer).
package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Repo is the GitHub repository releases are fetched from.
const Repo = "jakubkrzysztofsikora/bicz"

// The HTTP client: one timeout for everything, redirects allowed (GitHub
// release downloads redirect to a CDN).
var httpClient = &http.Client{Timeout: 120 * time.Second}

// Options are the resolved inputs of one update.
type Options struct {
	// InstallDir is where the running binary lives and where the new one is
	// written (default: the directory of os.Executable()).
	InstallDir string
	// RepoOverride replaces the default repository ("owner/repo"); used by
	// tests to point at a fake server.
	RepoOverride string
	// BaseOverride replaces the release download base URL entirely; used by
	// tests (must be http on loopback or https).
	BaseOverride string
	// Version pins a release tag instead of the latest one.
	Version string
	// DryRun reports what would happen and changes nothing.
	DryRun bool
	// Reinstall re-runs the adapter install for every installed harness.
	Reinstall func() error
	// RunningVersion overrides the reported running version (tests). Empty
	// means "ask the binary" via VersionHook.
	RunningVersion string
}

// VersionHook is set by the binary to report its stamped version.
var VersionHook = func() string { return "dev" }

// runningVersion returns opts.RunningVersion, or the VersionHook value.
func (o Options) runningVersion() string {
	if o.RunningVersion != "" {
		return o.RunningVersion
	}
	return VersionHook()
}

// Result reports what happened.
type Result struct {
	From    string // the running version ("dev" when unstamped)
	To      string // the release the update installed
	SHA256  string // sha256 of the installed archive, matches SHA256SUMS
	Binary  string // the binary path written
	Skipped bool   // true when already up to date
}

// LatestRelease is the answer of the GitHub latest-release endpoint that
// update cares about.
type LatestRelease struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
}

// DefaultBase is the release download base for the default repository.
func (o Options) base() string {
	if o.BaseOverride != "" {
		return strings.TrimRight(o.BaseOverride, "/")
	}
	repo := o.RepoOverride
	if repo == "" {
		repo = Repo
	}
	if o.Version != "" {
		return fmt.Sprintf("https://github.com/%s/releases/download/%s", repo, o.Version)
	}
	return fmt.Sprintf("https://github.com/%s/releases/latest/download", repo)
}

// assetName is the release archive for this platform. Windows is not
// supported here (zip layout, in-use binary swap): the ps1 installer handles
// it.
func assetName() (string, error) {
	switch runtime.GOOS {
	case "darwin", "linux":
	case "windows":
		return "", errors.New("bicz update is not supported on windows; run scripts/install.ps1 again")
	default:
		return "", fmt.Errorf("bicz update does not handle %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	arch := runtime.GOARCH
	if arch != "amd64" && arch != "arm64" {
		return "", fmt.Errorf("bicz update does not handle %s/%s", runtime.GOOS, arch)
	}
	return fmt.Sprintf("bicz_%s_%s.tar.gz", runtime.GOOS, arch), nil
}

// IsNewer reports whether candidate (a release tag like v0.2.2) is newer than
// current (a stamped version or "dev"). Equal or older is false; a current
// "dev" build is always older, so dev builds can move to a release; a
// candidate that is not v-prefixed semver is ignored (false).
func IsNewer(current, candidate string) bool {
	cur, ok1 := parseSemver(strings.TrimPrefix(strings.TrimSpace(current), "v"))
	cand, ok2 := parseSemver(strings.TrimPrefix(strings.TrimSpace(candidate), "v"))
	if !ok2 {
		return false
	}
	if !ok1 {
		// An unparseable current version ("dev") can always move to any real
		// release; a real current version cannot move to a garbage candidate.
		return strings.TrimSpace(strings.TrimPrefix(current, "v")) == "dev"
	}
	if cand[0] != cur[0] {
		return cand[0] > cur[0]
	}
	if cand[1] != cur[1] {
		return cand[1] > cur[1]
	}
	return cand[2] > cur[2]
}

// parseSemver parses "0.2.1" into its three numbers; anything else fails.
func parseSemver(s string) ([3]int, bool) {
	var out [3]int
	parts := strings.SplitN(s, ".", 3)
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		if p == "" {
			return out, false
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return out, false
			}
		}
		n := 0
		fmt.Sscanf(p, "%d", &n)
		out[i] = n
	}
	return out, true
}

// resolveExecutable finds the binary to replace: an explicit install dir, or
// the directory of the running executable. The directory of os.Executable is
// preferred over PATH lookup: an update must write next to where the running
// binary actually lives, not wherever PATH happens to point.
func resolveExecutable(installDir string) (string, error) {
	if installDir != "" {
		return filepath.Join(installDir, "bicz"), nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("selfupdate: locate the running binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if filepath.Base(exe) != "bicz" {
		return "", fmt.Errorf("selfupdate: the running binary %q is not named bicz", exe)
	}
	return exe, nil
}

// Run performs the whole update. It downloads SHA256SUMS first, refuses
// anything the sums do not cover, verifies the archive checksum, validates the
// archive member list exactly like scripts/install.sh, swaps the binary
// atomically, and then re-runs the adapter install.
func Run(opts Options) (Result, error) {
	var res Result
	asset, err := assetName()
	if err != nil {
		return res, err
	}
	target, err := resolveExecutable(opts.InstallDir)
	if err != nil {
		return res, err
	}
	res.From = opts.runningVersion()
	res.Binary = target

	// The latest tag comes from the redirect target of the latest download
	// URL: /releases/latest/download/<asset> 302s to
	// /releases/download/<tag>/<asset>, and every release ships SHA256SUMS.
	// Reading the redirect needs no API token.
	if opts.Version == "" && opts.BaseOverride == "" {
		latest, err := latestTag(opts.base(), asset)
		if err != nil {
			return res, err
		}
		res.To = latest
	} else {
		res.To = opts.Version
	}

	if res.To != "" && res.From != "dev" && !IsNewer(res.From, res.To) {
		res.Skipped = true
		return res, nil
	}
	if opts.DryRun {
		return res, nil
	}

	// Once the tag is known, everything comes from the tag's own directory:
	// one redirect fewer, and the sums can never be mixed between releases.
	base := opts.base()
	if res.To != "" && opts.BaseOverride == "" {
		base = fmt.Sprintf("https://github.com/%s/releases/download/%s", repoName(opts), res.To)
	}
	sums, err := fetch(base + "/SHA256SUMS")
	if err != nil {
		return res, fmt.Errorf("selfupdate: fetch SHA256SUMS: %w", err)
	}
	archive, err := fetch(base + "/" + asset)
	if err != nil {
		return res, fmt.Errorf("selfupdate: fetch %s: %w", asset, err)
	}
	sum := sha256.Sum256(archive)
	got := hex.EncodeToString(sum[:])
	want := sumFor(sums, asset)
	if want == "" {
		return res, fmt.Errorf("selfupdate: SHA256SUMS has no entry for %s; refusing", asset)
	}
	if want != got {
		return res, fmt.Errorf("selfupdate: checksum mismatch for %s: SHA256SUMS says %s, downloaded %s; nothing installed", asset, want, got)
	}
	res.SHA256 = got

	binary, err := extractBicz(archive)
	if err != nil {
		return res, err
	}

	// Atomic swap: the previous binary is kept for rollback, exactly like the
	// install script. A failed copy leaves the old binary in place.
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return res, fmt.Errorf("selfupdate: create install dir: %w", err)
	}
	if info, err := os.Stat(target); err == nil && !info.Mode().IsRegular() {
		return res, fmt.Errorf("selfupdate: %s exists and is not a regular file; refusing", target)
	}
	previous := filepath.Join(filepath.Dir(target), "bicz.previous")
	if old, err := os.ReadFile(target); err == nil {
		if err := os.WriteFile(previous, old, 0o755); err != nil {
			return res, fmt.Errorf("selfupdate: keep previous binary: %w", err)
		}
	}
	staged := target + ".new"
	if err := os.WriteFile(staged, binary, 0o755); err != nil {
		return res, fmt.Errorf("selfupdate: stage new binary: %w", err)
	}
	if err := os.Rename(staged, target); err != nil {
		return res, fmt.Errorf("selfupdate: swap binary: %w", err)
	}

	if opts.Reinstall != nil {
		if err := opts.Reinstall(); err != nil {
			return res, fmt.Errorf("selfupdate: binary updated to %s but the adapter reinstall failed (%w); run `bicz install --harness <each>` by hand", res.To, err)
		}
	}
	return res, nil
}

// repoName returns the repository slug for the base URL construction.
func repoName(opts Options) string {
	if opts.RepoOverride != "" {
		return opts.RepoOverride
	}
	return Repo
}

// latestTag follows the /releases/latest/download redirect once and reads the
// tag out of the resolved URL path.
func latestTag(base, asset string) (string, error) {
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // do not follow; inspect it
		},
	}
	url := base + "/" + asset
	resp, err := client.Head(url)
	if err != nil {
		return "", fmt.Errorf("selfupdate: probe %s: %w", url, err)
	}
	defer resp.Body.Close()
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusFound || loc == "" {
		return "", fmt.Errorf("selfupdate: %s did not redirect to a release (status %d)", url, resp.StatusCode)
	}
	// .../releases/download/<tag>/<asset>
	parts := strings.Split(strings.TrimRight(loc, "/"), "/")
	if len(parts) < 2 {
		return "", fmt.Errorf("selfupdate: redirect %q is not a release URL", loc)
	}
	tag := parts[len(parts)-2]
	if !strings.HasPrefix(tag, "v") {
		return "", fmt.Errorf("selfupdate: redirect names %q, which is not a release tag", tag)
	}
	return tag, nil
}

// fetch downloads one URL and returns its body.
func fetch(url string) ([]byte, error) {
	resp, err := httpClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}

// sumFor extracts the checksum of name from SHA256SUMS content.
func sumFor(sums []byte, name string) string {
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 2 {
			f := strings.TrimPrefix(fields[1], "*")
			if f == name || f == "./"+name {
				return strings.ToLower(fields[0])
			}
		}
	}
	return ""
}

// extractBicz validates and extracts the bicz member of the release archive:
// exactly one regular file named bicz (optionally ./bicz) plus an optional
// regular LICENSE, no other members, no directories, no links. This mirrors
// scripts/install.sh's guard against the common malicious-archive paths; it is
// a guard, not a containment boundary. The real protection is the checksum
// against SHA256SUMS served from the same origin.
func extractBicz(archive []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("selfupdate: release archive is not gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var binary []byte
	members := 0
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("selfupdate: read archive: %w", err)
		}
		members++
		name := strings.TrimPrefix(header.Name, "./")
		isRegular := header.Typeflag == tar.TypeReg
		switch name {
		case "bicz":
			if !isRegular {
				return nil, fmt.Errorf("selfupdate: archive member %q is not a regular file; refusing", header.Name)
			}
			if binary != nil {
				return nil, errors.New("selfupdate: archive holds two bicz members; refusing")
			}
			body, err := io.ReadAll(io.LimitReader(tr, 256<<20))
			if err != nil {
				return nil, fmt.Errorf("selfupdate: read bicz member: %w", err)
			}
			binary = body
		case "LICENSE":
			if !isRegular {
				return nil, fmt.Errorf("selfupdate: archive member %q is not a regular file; refusing", header.Name)
			}
			if _, err := io.Copy(io.Discard, io.LimitReader(tr, 1<<20)); err != nil {
				return nil, fmt.Errorf("selfupdate: read LICENSE member: %w", err)
			}
		default:
			return nil, fmt.Errorf("selfupdate: unexpected archive member %q; refusing", header.Name)
		}
	}
	if members == 0 {
		return nil, errors.New("selfupdate: release archive is empty; refusing")
	}
	if binary == nil {
		return nil, errors.New("selfupdate: archive holds no bicz binary; refusing")
	}
	return binary, nil
}
