package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		latest, current string
		newer, ok       bool
	}{
		{"v0.2.0", "v0.1.9", true, true},
		{"v0.10.0", "v0.9.0", true, true},
		{"v1.0.0", "v1.0.0", false, true},
		{"v1.0.0", "v1.0.1", false, true},
		{"v1.0.0", "v1.0.0-rc1", true, true},
		{"v1.0.0-rc2", "v1.0.0-rc1", true, true},
		{"v1.0.0-rc1", "v1.0.0", false, true},
		{"v0.1.1", "v0.1.1-snapshot-abc1234", true, true},
		{"v0.2.0", "52421c6-dirty", false, false},
		{"v0.2.0", "dev", false, false},
		{"latest", "v0.1.0", false, false},
	}
	for _, c := range cases {
		newer, ok := Newer(c.latest, c.current)
		if newer != c.newer || ok != c.ok {
			t.Errorf("Newer(%q, %q) = %v, %v; want %v, %v", c.latest, c.current, newer, ok, c.newer, c.ok)
		}
	}
}

func TestDetect(t *testing.T) {
	dir := t.TempDir()
	keg := filepath.Join(dir, "Cellar", "terminatr", "0.2.0", "bin")
	os.MkdirAll(keg, 0o755)
	os.WriteFile(filepath.Join(keg, "tm"), nil, 0o755)
	os.MkdirAll(filepath.Join(dir, "bin"), 0o755)
	link := filepath.Join(dir, "bin", "tm")
	os.Symlink(filepath.Join(keg, "tm"), link)
	plain := filepath.Join(dir, "local", "tm")

	if in := Detect(link, "release"); in.Method != Homebrew || !strings.HasSuffix(in.Path, "/Cellar/terminatr/0.2.0/bin/tm") || in.Upgrade != "brew upgrade terminatr" {
		t.Errorf("brew symlink: %+v", in)
	}
	if in := Detect(plain, "release"); in.Method != Direct || in.Path != plain {
		t.Errorf("direct: %+v", in)
	}
	if in := Detect(plain, "snapshot"); in.Method != Direct {
		t.Errorf("snapshot: %+v", in)
	}
	if in := Detect(link, ""); in.Method != Dev {
		t.Errorf("source build: %+v", in)
	}
}

func TestChecksumFor(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	sums := []byte(sum + "  tm_linux_amd64.tar.gz\n" + strings.Repeat("cd", 32) + " *tm_darwin_arm64.tar.gz\n")
	if got, err := checksumFor(sums, "tm_linux_amd64.tar.gz"); err != nil || got != sum {
		t.Errorf("linux: %q %v", got, err)
	}
	if got, err := checksumFor(sums, "tm_darwin_arm64.tar.gz"); err != nil || got != strings.Repeat("cd", 32) {
		t.Errorf("binary-mode line: %q %v", got, err)
	}
	if _, err := checksumFor(sums, "tm_linux_arm64.tar.gz"); err == nil {
		t.Error("missing archive: no error")
	}
}

// Archive builds a release archive holding files (name → content).
func Archive(t testing.TB, files map[string]string) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(body))
	}
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

// fakeRelease serves one release with the given archive and checksum.
func fakeRelease(t *testing.T, tag string, archive []byte, sum string) *httptest.Server {
	name := ArchiveName("linux", "amd64")
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/latest":
			fmt.Fprintf(w, `{"tag_name":%q,"assets":[{"name":%q,"browser_download_url":"%s/dl/a"},{"name":"checksums.txt","browser_download_url":"%s/dl/sums"}]}`,
				tag, name, srv.URL, srv.URL)
		case "/dl/a":
			w.Write(archive)
		case "/dl/sums":
			fmt.Fprintf(w, "%s  %s\n", sum, name)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDownload(t *testing.T) {
	archive := Archive(t, map[string]string{"tm": "new binary", "README.md": "readme"})
	sum := fmt.Sprintf("%x", sha256.Sum256(archive))
	srv := fakeRelease(t, "v0.2.0", archive, sum)
	c := NewClient(func(k string) string {
		if k == EnvAPI {
			return srv.URL
		}
		return ""
	})
	r, err := c.Latest(context.Background())
	if err != nil || r.Tag != "v0.2.0" {
		t.Fatalf("Latest: %+v %v", r, err)
	}
	dir := t.TempDir()
	tmp, err := c.Download(context.Background(), r, "linux", "amd64", dir)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(tmp); string(b) != "new binary" || filepath.Dir(tmp) != dir {
		t.Fatalf("extracted %q into %s", b, tmp)
	}
	dst := filepath.Join(dir, "tm")
	os.WriteFile(dst, []byte("old"), 0o700)
	if err := Replace(tmp, dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "new binary" {
		t.Fatalf("replaced with %q", b)
	}

	// A wrong checksum refuses, and so does a platform without an archive.
	bad := fakeRelease(t, "v0.2.0", archive, strings.Repeat("0", 64))
	c.API = bad.URL
	r, _ = c.Latest(context.Background())
	if _, err := c.Download(context.Background(), r, "linux", "amd64", dir); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Errorf("bad checksum: %v", err)
	}
	if _, err := c.Download(context.Background(), r, "plan9", "amd64", dir); err == nil {
		t.Error("no archive for the platform: no error")
	}
}

func TestOff(t *testing.T) {
	c := NewClient(func(k string) string {
		if k == EnvAPI {
			return "off"
		}
		return ""
	})
	if _, err := c.Latest(context.Background()); err != ErrOff {
		t.Fatalf("off: %v", err)
	}
}

// TestLatestFallsBackToWeb: a rate-limited API is replaced by the
// releases/latest redirect, and the assets follow the naming convention.
func TestLatestFallsBackToWeb(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/releases/latest":
			w.Header().Set("X-Ratelimit-Remaining", "0")
			http.Error(w, `{"message":"API rate limit exceeded"}`, http.StatusForbidden)
		case "/web/releases/latest":
			http.Redirect(w, r, "/web/releases/tag/v0.3.0", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := &Client{API: srv.URL + "/api", Web: srv.URL + "/web", HTTP: srv.Client()}
	r, err := c.Latest(context.Background())
	if err != nil || r.Tag != "v0.3.0" {
		t.Fatalf("Latest: %+v %v", r, err)
	}
	a, ok := r.asset(ArchiveName("darwin", "arm64"))
	if !ok || a.Download != srv.URL+"/web/releases/download/v0.3.0/tm_darwin_arm64.tar.gz" {
		t.Fatalf("asset: %+v %v", a, ok)
	}
	// No release at all: both errors are reported.
	c.Web = srv.URL + "/none"
	if _, err := c.Latest(context.Background()); err == nil || !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("no release: %v", err)
	}
}

// TestCodesign checks a Developer ID signed tm given in TM_SIGNED_BINARY
// (with its team in TM_SIGNED_TEAM): macOS only, skipped otherwise.
func TestCodesign(t *testing.T) {
	bin := os.Getenv("TM_SIGNED_BINARY")
	if bin == "" {
		t.Skip("set TM_SIGNED_BINARY to a Developer ID signed tm")
	}
	team := os.Getenv("TM_SIGNED_TEAM")
	if got, err := Codesign(bin, team); err != nil || got != team {
		t.Fatalf("Codesign: %q %v", got, err)
	}
	if _, err := Codesign(bin, "XXXXXXXXXX"); err == nil {
		t.Fatal("another team: no error")
	}
}
