//go:build unix

package cli

import (
	"bytes"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeLaunchctl stands in for launchctl when the test binary runs as
// "launchctl" (a symlink on PATH) with TM_FAKE_LAUNCHCTL_DIR set. It logs
// every call to <dir>/calls, keeps the loaded plist's path in
// <dir>/loaded, and on kickstart starts the plist's program the way
// launchd does: a new session, launchd's small environment plus the
// plist's EnvironmentVariables. <dir>/noconsole makes the GUI domain
// missing.
func fakeLaunchctl(dir string, args []string) int {
	f, _ := os.OpenFile(filepath.Join(dir, "calls"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	fmt.Fprintln(f, strings.Join(args, " "))
	f.Close()
	loaded := filepath.Join(dir, "loaded")
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	if len(args) == 0 {
		return 64
	}
	switch args[0] {
	case "managername":
		fmt.Println("Aqua")
	case "print":
		if exists(filepath.Join(dir, "noconsole")) {
			fmt.Fprintln(os.Stderr, "Could not find domain")
			return 113
		}
		if strings.Count(args[1], "/") == 2 && !exists(loaded) {
			fmt.Fprintln(os.Stderr, "Could not find service")
			return 113
		}
	case "bootstrap":
		if exists(loaded) {
			return 5
		}
		os.WriteFile(loaded, []byte(args[2]), 0o600)
	case "bootout":
		if os.Remove(loaded) != nil {
			return 113
		}
	case "kickstart":
		plist, err := os.ReadFile(loaded)
		if err != nil {
			return 113
		}
		data, err := os.ReadFile(string(plist))
		if err != nil {
			return 1
		}
		argv, env := parsePlist(string(data))
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Env = append([]string{"HOME=" + os.Getenv("HOME"), "PATH=/usr/bin:/bin:/usr/sbin:/sbin"}, env...)
		cmd.Dir = "/"
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		cmd.Process.Release()
	}
	return 0
}

func must(b []byte, _ error) []byte { return b }

var plistString = regexp.MustCompile(`<(key|string)>([^<]*)</`)

// parsePlist reads ProgramArguments and EnvironmentVariables from a plist
// tm wrote.
func parsePlist(s string) (argv, env []string) {
	sect := func(key, end string) []string {
		_, rest, _ := strings.Cut(s, "<key>"+key+"</key>")
		rest, _, _ = strings.Cut(rest, end)
		var out []string
		for _, m := range plistString.FindAllStringSubmatch(rest, -1) {
			out = append(out, html.UnescapeString(m[2]))
		}
		return out
	}
	argv = sect("ProgramArguments", "</array>")
	kv := sect("EnvironmentVariables", "</dict>")
	for i := 0; i+1 < len(kv); i += 2 {
		env = append(env, kv[i]+"="+kv[i+1])
	}
	return argv, env
}

// TestBinaryLaunchdStart: on macOS tm starts the server through launchd's
// GUI domain, over SSH too, and the server gets the starting shell's
// environment less the SSH login's; with nobody at the console it
// refuses, and --no-launchd starts a server directly, with the warning.
func TestBinaryLaunchdStart(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("launchd is macOS only")
	}
	tm := newProc(t)
	fake := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(fake, "bin")
	os.MkdirAll(bin, 0o700)
	if err := os.Symlink(self, filepath.Join(bin, "launchctl")); err != nil {
		t.Fatal(err)
	}
	tm.env = []string{
		"TERMINATR_LAUNCHD=", "PATH=" + bin + ":" + os.Getenv("PATH"),
		"TM_FAKE_LAUNCHCTL_DIR=" + fake, "TM_PARITY=from-the-shell",
		"SSH_CONNECTION=10.0.0.2 50000 10.0.0.1 22", "SSH_AUTH_SOCK=/tmp/ssh-login/agent",
	}
	calls := func() string {
		b, _ := os.ReadFile(filepath.Join(fake, "calls"))
		os.Remove(filepath.Join(fake, "calls"))
		return string(b)
	}
	uid := os.Getuid()

	code, out, errs := tm.run("", "server", "start")
	if code != 0 || !strings.HasPrefix(out, "started") || strings.Contains(errs, "warning") {
		t.Fatalf("start: exit %d %q %q", code, out, errs)
	}
	c := calls()
	for _, want := range []string{
		fmt.Sprintf("print gui/%d\n", uid),
		fmt.Sprintf("bootstrap gui/%d ", uid),
		fmt.Sprintf("kickstart gui/%d/dev.terminatr.server.", uid),
	} {
		if !strings.Contains(c, want) {
			t.Errorf("launchctl calls lack %q:\n%s", want, c)
		}
	}
	if _, err := os.Stat(filepath.Join(tm.home, "Library", "LaunchAgents")); err == nil {
		t.Error("a start wrote a login service")
	}
	// launchd runs the pin, not the tm that started it: macOS privacy
	// settings hold the path launchd started (T129).
	loaded, _ := os.ReadFile(filepath.Join(fake, "loaded"))
	data, _ := os.ReadFile(string(loaded))
	if argv, _ := parsePlist(string(data)); len(argv) == 0 || argv[0] == tmBin ||
		!strings.HasSuffix(argv[0], string(filepath.Separator)+filepath.Join("bin", "tm")) {
		t.Errorf("the job runs %q", argv)
	} else if a, _ := os.ReadFile(tmBin); !bytes.Equal(a, must(os.ReadFile(argv[0]))) {
		t.Errorf("the pin %s doesn't hold this build", argv[0])
	}

	// The server, and so its sessions, has the shell's environment but
	// not the SSH login's.
	id := strings.TrimSpace(tm.ok("session", "start", "--", "/bin/sh", "-c",
		`echo "parity=$TM_PARITY ssh=$SSH_CONNECTION agent=$SSH_AUTH_SOCK."; sleep 30`))
	var screen string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if screen = tm.ok("session", "read", id); strings.Contains(screen, "parity=") {
			break
		}
	}
	if !strings.Contains(screen, "parity=from-the-shell ssh= agent=.") {
		t.Fatalf("session environment:\n%s", screen)
	}

	// A restart kickstarts the loaded job again, without reloading it.
	tm.ok("server", "restart", "--yes")
	if c := calls(); !strings.Contains(c, "kickstart") || strings.Contains(c, "bootstrap") {
		t.Fatalf("restart calls:\n%s", c)
	}

	// Nobody at the console: refused, nothing started.
	tm.ok("server", "stop", "--yes")
	// A stop boots the dev home's on-demand job out; it isn't left loaded.
	if c := calls(); !strings.Contains(c, fmt.Sprintf("bootout gui/%d/dev.terminatr.server.", uid)) {
		t.Fatalf("stop calls:\n%s", c)
	}
	if _, err := os.Stat(filepath.Join(fake, "loaded")); err == nil {
		t.Fatal("the job is still loaded after a stop")
	}
	os.WriteFile(filepath.Join(fake, "noconsole"), nil, 0o600)
	tm.want(1, "nobody is logged in at the Mac's console", "server", "start")
	if code, out, _ := tm.run("", "server", "status"); code == 0 {
		t.Fatalf("a server runs after the refusal: %s", out)
	}
	code, out, errs = tm.run("", "server", "start", "--no-launchd")
	if code != 0 || !strings.HasPrefix(out, "started") || !strings.Contains(errs, "over SSH without launchd") {
		t.Fatalf("--no-launchd: exit %d %q %q", code, out, errs)
	}
}
