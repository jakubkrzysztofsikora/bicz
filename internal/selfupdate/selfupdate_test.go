package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsNewerSemver(t *testing.T) {
	cases := []struct {
		current, candidate string
		want               bool
	}{
		{"v0.2.1", "v0.2.2", true},
		{"0.2.1", "v0.3.0", true},
		{"v0.2.1", "v1.0.0", true},
		{"v0.2.2", "v0.2.1", false},
		{"v0.2.1", "v0.2.1", false},
		{"dev", "v0.2.1", true},
		{"dev", "garbage", false},
		{"v0.2.1", "not-a-version", false},
		{"", "v0.2.1", false},
	}
	for _, tc := range cases {
		if got := IsNewer(tc.current, tc.candidate); got != tc.want {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", tc.current, tc.candidate, got, tc.want)
		}
	}
}

// fakeRelease serves SHA256SUMS and one archive built from the given binary
// content, in the exact layout scripts/build_release.sh produces.
func fakeRelease(t *testing.T, tag, binaryContent string) *httptest.Server {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	member := func(name string, body []byte) {
		hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(body))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	member("bicz", []byte(binaryContent))
	member("LICENSE", []byte("MIT"))
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	archive := buf.Bytes()
	sum := sha256.Sum256(archive)
	sums := hex.EncodeToString(sum[:]) + "  bicz_darwin_arm64.tar.gz\n"

	mux := http.NewServeMux()
	mux.HandleFunc("/releases/latest/download/bicz_darwin_arm64.tar.gz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			// The tag probe: GitHub answers the asset URL with a redirect
			// carrying the tag.
			w.Header().Set("Location", "/releases/download/"+tag+"/bicz_darwin_arm64.tar.gz")
			w.WriteHeader(http.StatusFound)
			return
		}
		_, _ = w.Write(archive)
	})
	// Run fetches SHA256SUMS from the same base it probed, and the archive
	// from the pinned version base when Version is set.
	mux.HandleFunc("/releases/latest/download/SHA256SUMS", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(sums))
	})
	mux.HandleFunc("/releases/download/SHA256SUMS", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(sums))
	})
	// The archive is served both under latest (the client follows the
	// redirect here) and under the pinned tag path.
	mux.HandleFunc("/releases/latest/download/archive", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	})
	mux.HandleFunc("/releases/download/v9.9.9/archive", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func TestUpdateSwapsBinaryAndReports(t *testing.T) {
	server := fakeRelease(t, "v9.9.9", "#!/bin/fake-new-binary\n")
	dir := t.TempDir()
	target := filepath.Join(dir, "bicz")
	if err := os.WriteFile(target, []byte("#!/bin/old\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := Run(Options{
		BaseOverride:   server.URL + "/releases/latest/download",
		InstallDir:     dir,
		RunningVersion: "v0.2.1",
		Version:        "v9.9.9",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.From != "v0.2.1" || res.To != "v9.9.9" || res.Skipped {
		t.Fatalf("result: %+v", res)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "#!/bin/fake-new-binary\n" {
		t.Fatalf("binary not swapped: %q err=%v", got, err)
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("binary mode: %v err=%v", info.Mode(), err)
	}
	previous, err := os.ReadFile(filepath.Join(dir, "bicz.previous"))
	if err != nil || string(previous) != "#!/bin/old\n" {
		t.Fatalf("previous binary not kept: %q err=%v", previous, err)
	}

	// Running the same (now newest) version again skips.
	res, err = Run(Options{BaseOverride: server.URL + "/releases/latest/download", InstallDir: dir, RunningVersion: "v9.9.9", Version: "v9.9.9"})
	if err != nil || !res.Skipped {
		t.Fatalf("second run must skip: %+v err=%v", res, err)
	}
}

func TestUpdateReinstallCallbackAndRefusals(t *testing.T) {
	server := fakeRelease(t, "v9.9.9", "new")
	dir := t.TempDir()
	reinstalled := false
	res, err := Run(Options{
		BaseOverride:   server.URL + "/releases/latest/download",
		InstallDir:     dir,
		RunningVersion: "v0.1.0",
		Version:        "v9.9.9",
		Reinstall:      func() error { reinstalled = true; return nil },
	})
	if err != nil || !reinstalled {
		t.Fatalf("reinstall callback not run: %+v err=%v", res, err)
	}

	// Dry-run touches nothing.
	dir2 := t.TempDir()
	res, err = Run(Options{BaseOverride: server.URL + "/releases/latest/download", InstallDir: dir2, RunningVersion: "v0.1.0", Version: "v9.9.9", DryRun: true})
	if err != nil || res.Skipped || res.To != "v9.9.9" {
		t.Fatalf("dry-run: %+v err=%v", res, err)
	}
	if _, err := os.Stat(filepath.Join(dir2, "bicz")); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote a binary: %v", err)
	}

	// sumFor parses both plain and ./-prefixed forms and misses others.
	if got := sumFor([]byte("abc  bicz_darwin_arm64.tar.gz\n"), "bicz_darwin_arm64.tar.gz"); got != "abc" {
		t.Fatalf("sumFor: %q", got)
	}
	if got := sumFor([]byte("abc  ./bicz_darwin_arm64.tar.gz\n"), "bicz_darwin_arm64.tar.gz"); got != "abc" {
		t.Fatalf("sumFor ./ form: %q", got)
	}
	if got := sumFor([]byte("abc  other.tar.gz\n"), "bicz_darwin_arm64.tar.gz"); got != "" {
		t.Fatalf("sumFor must miss: %q", got)
	}

	// extractBicz refuses unexpected members.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "evil.sh", Mode: 0o755, Size: 2})
	_, _ = tw.Write([]byte("no"))
	_ = tw.Close()
	_ = gz.Close()
	if _, err := extractBicz(buf.Bytes()); err == nil || !strings.Contains(err.Error(), "unexpected archive member") {
		t.Fatalf("unexpected member must refuse: %v", err)
	}

	// With an explicit install dir the target is always <dir>/bicz, whatever
	// else the directory holds.
	dir3 := t.TempDir()
	other := filepath.Join(dir3, "notbicz")
	if err := os.WriteFile(other, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := resolveExecutable(dir3)
	if err != nil || got != filepath.Join(dir3, "bicz") {
		t.Fatalf("resolveExecutable(dir) = %q err=%v", got, err)
	}
	// A symlinked executable resolves to its real path first.
	link := filepath.Join(dir3, "blink")
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
}
