package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theclifmeister/termalator/internal/proto"
	"github.com/theclifmeister/termalator/internal/update"
)

type updateRig struct {
	u        *updater
	exe      string
	restarts []bool
	brews    [][]string
}

// newUpdateRig serves release tag with a tm whose `version` prints
// version, and installs an old tm at exe (under dir/sub).
func newUpdateRig(t *testing.T, tag, version, sub string, server bool) *updateRig {
	t.Helper()
	script := "#!/bin/sh\necho tm " + version + " build x\n"
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	tw.WriteHeader(&tar.Header{Name: "tm", Mode: 0o755, Size: int64(len(script)), Typeflag: tar.TypeReg})
	tw.Write([]byte(script))
	tw.Close()
	zw.Close()
	archive := buf.Bytes()
	name := update.ArchiveName("linux", "amd64")
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/latest":
			fmt.Fprintf(w, `{"tag_name":%q,"assets":[{"name":%q,"browser_download_url":"%s/a"},{"name":"checksums.txt","browser_download_url":"%s/sums"}]}`,
				tag, name, srv.URL, srv.URL)
		case "/a":
			w.Write(archive)
		case "/sums":
			fmt.Fprintf(w, "%x  %s\n", sha256.Sum256(archive), name)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	dir := filepath.Join(t.TempDir(), sub)
	os.MkdirAll(dir, 0o755)
	exe := filepath.Join(dir, "tm")
	os.WriteFile(exe, []byte("old"), 0o755)
	rig := &updateRig{exe: exe}
	rig.u = &updater{
		Exe: exe, Channel: "release", Version: "v0.1.0", GOOS: "linux", GOARCH: "amd64",
		Client: update.NewClient(func(k string) string {
			if k == update.EnvAPI {
				return srv.URL
			}
			return ""
		}),
		RunVersion: func(bin string) (string, error) {
			b, err := os.ReadFile(bin)
			return strings.TrimPrefix(strings.TrimSpace(string(b)), "#!/bin/sh\necho "), err
		},
		Brew: func(args ...string) error { rig.brews = append(rig.brews, args); return nil },
		Server: func() (proto.ServerStatus, bool) {
			return proto.ServerStatus{PID: 42, Version: "v0.1.0", Sessions: 3}, server
		},
		Restart: func(bin string, yes bool) int { rig.restarts = append(rig.restarts, yes); return ExitOK },
	}
	return rig
}

func (r *updateRig) run(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	e := &Env{Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errb, Getenv: func(string) string { return "" }}
	code := updateCmd(e, args, r.u)
	return code, out.String(), errb.String()
}

func TestUpdateCheck(t *testing.T) {
	r := newUpdateRig(t, "v0.2.0", "v0.2.0", "bin", false)
	code, out, _ := r.run(t, "", "--check")
	if code != ExitOK || !strings.Contains(out, "v0.2.0: tm update") || !strings.Contains(out, "direct") {
		t.Fatalf("check: %d %q", code, out)
	}
	code, out, _ = r.run(t, "", "--check", "--json")
	if code != ExitOK || !strings.Contains(out, `"newer": true`) || !strings.Contains(out, `"method": "direct"`) {
		t.Fatalf("check --json: %d %q", code, out)
	}
	r.u.Version = "v0.2.0"
	if code, out, _ := r.run(t, "", "--check"); code != ExitOK || !strings.Contains(out, "up to date") {
		t.Fatalf("check, current: %d %q", code, out)
	}
	if b, _ := os.ReadFile(r.exe); string(b) != "old" {
		t.Fatal("--check changed the binary")
	}
}

func TestUpdateDirect(t *testing.T) {
	r := newUpdateRig(t, "v0.2.0", "v0.2.0", "bin", true)
	// Not a terminal and no --yes: nothing changes.
	if code, _, errs := r.run(t, ""); code != ExitRefused || !strings.Contains(errs, "--yes") {
		t.Fatalf("no --yes: %d %q", code, errs)
	}
	code, out, errs := r.run(t, "", "--yes")
	if code != ExitOK {
		t.Fatalf("update: %d\n%s%s", code, out, errs)
	}
	if b, _ := os.ReadFile(r.exe); !strings.Contains(string(b), "v0.2.0") {
		t.Fatalf("binary not replaced: %q", b)
	}
	// The server keeps running, and the user is told how to switch it.
	if len(r.restarts) != 0 || !strings.Contains(out, "pid 42") || !strings.Contains(out, "tm server restart") {
		t.Fatalf("server handling: restarts %v\n%s", r.restarts, out)
	}
	if entries, _ := os.ReadDir(filepath.Dir(r.exe)); len(entries) != 1 {
		t.Errorf("leftovers next to tm: %v", entries)
	}
}

func TestUpdateRestart(t *testing.T) {
	r := newUpdateRig(t, "v0.2.0", "v0.2.0", "bin", true)
	if code, out, errs := r.run(t, "", "--yes", "--restart"); code != ExitOK || len(r.restarts) != 1 || !r.restarts[0] {
		t.Fatalf("--restart: %d %v\n%s%s", code, r.restarts, out, errs)
	}
	// On a terminal it asks, and the restart keeps its own question.
	r = newUpdateRig(t, "v0.2.0", "v0.2.0", "bin", true)
	r.u.TTY = true
	if code, _, _ := r.run(t, "y\ny\n"); code != ExitOK || len(r.restarts) != 1 || r.restarts[0] {
		t.Fatalf("asked restart: %d %v", code, r.restarts)
	}
}

func TestUpdateRefusesBadDownloads(t *testing.T) {
	// The downloaded tm says it is another version.
	r := newUpdateRig(t, "v0.2.0", "v0.1.5", "bin", false)
	if code, _, errs := r.run(t, "", "--yes"); code != ExitRefused || !strings.Contains(errs, "nothing changed") {
		t.Fatalf("wrong version: %d %q", code, errs)
	}
	// The signature check fails.
	r = newUpdateRig(t, "v0.2.0", "v0.2.0", "bin", false)
	r.u.Verify = func(string, string) (string, error) { return "", fmt.Errorf("not signed with a Developer ID") }
	if code, _, errs := r.run(t, "", "--yes"); code != ExitRefused || !strings.Contains(errs, "Developer ID") {
		t.Fatalf("signature: %d %q", code, errs)
	}
	if b, _ := os.ReadFile(r.exe); string(b) != "old" {
		t.Fatalf("binary replaced after a failed check: %q", b)
	}
	if entries, _ := os.ReadDir(filepath.Dir(r.exe)); len(entries) != 1 {
		t.Errorf("leftovers next to tm: %v", entries)
	}
}

func TestUpdateHomebrew(t *testing.T) {
	r := newUpdateRig(t, "v0.2.0", "v0.2.0", "Cellar/termalator/0.1.0/bin", false)
	code, out, _ := r.run(t, "")
	if code != ExitOK || !strings.Contains(out, "brew upgrade termalator") || len(r.brews) != 0 {
		t.Fatalf("brew, not asked: %d %v %q", code, r.brews, out)
	}
	if code, _, _ := r.run(t, "", "--yes"); code != ExitOK || len(r.brews) != 1 || strings.Join(r.brews[0], " ") != "upgrade termalator" {
		t.Fatalf("brew --yes: %d %v", code, r.brews)
	}
	if b, _ := os.ReadFile(r.exe); string(b) != "old" {
		t.Fatal("tm update wrote into the Homebrew keg")
	}
	if got := brewBin("/opt/homebrew/Cellar/termalator/0.1.0/bin/tm"); got != "/opt/homebrew/bin/tm" {
		t.Errorf("brewBin = %s", got)
	}
}

func TestUpdateSourceBuild(t *testing.T) {
	r := newUpdateRig(t, "v0.2.0", "v0.2.0", "bin", false)
	r.u.Channel = ""
	if code, _, errs := r.run(t, "", "--yes"); code != ExitRefused || !strings.Contains(errs, "git pull && make") {
		t.Fatalf("source build: %d %q", code, errs)
	}
}
