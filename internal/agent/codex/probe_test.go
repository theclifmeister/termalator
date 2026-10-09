package codex

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestProbeVerdicts: what the sandbox run says becomes a reason to fall
// back, or nil, for each way a newer Codex could break the profile.
func TestProbeVerdicts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for codex")
	}
	cases := []struct {
		script, want string
	}{
		{`echo "unix=ok tcp=refused"`, ""},
		{`echo "WARNING: something"; echo "unix=ok tcp=refused"`, ""},
		{`echo "unix=ok tcp=ok"`, "the network isn't limited"},
		{`echo "unix=refused tcp=refused"`, "the profile wasn't applied"},
		{"echo 'Error loading config.toml: unknown configuration field `features.network_proxy` in -c/--config override' >&2; exit 1", "codex sandbox refused it: Error loading config.toml: unknown configuration field"},
		{`echo "error: unexpected argument '-P' found" >&2; exit 2`, "codex sandbox refused it: error: unexpected argument '-P'"},
		{`echo something else`, "unexpected probe output"},
	}
	for _, tc := range cases {
		codex := filepath.Join(t.TempDir(), "codex")
		os.WriteFile(codex, []byte("#!/bin/sh\n"+tc.script+"\n"), 0o700)
		err := probeOnce(codex, "/bin/tm", t.TempDir(), "/run/tm.sock", "tm", []string{"-c", "features.network_proxy=true"})
		if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%s: %v, want %q", tc.script, err, tc.want)
		}
	}
}

// TestProbeMain: the in-sandbox side reports each dial.
func TestProbeMain(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets")
	}
	dir, _ := os.MkdirTemp("", "tmp-")
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	tcp, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := tcp.Addr().String()
	tcp.Close() // nothing listens there now
	r, w, _ := os.Pipe()
	stdout := os.Stdout
	os.Stdout = w
	code := ProbeMain([]string{sock, addr})
	os.Stdout = stdout
	w.Close()
	out := make([]byte, 100)
	n, _ := r.Read(out)
	if code != 0 || strings.TrimSpace(string(out[:n])) != "unix=ok tcp=refused" {
		t.Errorf("ProbeMain = %d %q", code, out[:n])
	}
	if ProbeMain(nil) != 2 {
		t.Error("no args accepted")
	}
}

// TestProfileFlags: the profile's flags are found, -P's name read.
func TestProfileFlags(t *testing.T) {
	argv := []string{"codex", "-c", "a=1", "-c", `default_permissions="tm"`, "--approve-for-me", "-c", "features.network_proxy=true", "-c", "permissions={tm={}}", "-m", "x"}
	idx, name, flags := profileFlags(argv)
	if name != "tm" || len(idx) != 6 || strings.Join(flags, " ") != "-c features.network_proxy=true -c permissions={tm={}}" {
		t.Errorf("idx %v name %q flags %q", idx, name, flags)
	}
	if idx, _, _ := profileFlags([]string{"codex", "-c", "a=1"}); idx != nil {
		t.Errorf("no profile: %v", idx)
	}
}
