//go:build !windows

package service

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJobLabel(t *testing.T) {
	c, _ := config(t, "darwin")
	other := c
	other.Home = "/data/other"
	if c.JobLabel() == other.JobLabel() || !strings.HasPrefix(c.JobLabel(), Label+".") {
		t.Fatalf("labels %s %s", c.JobLabel(), other.JobLabel())
	}
	c.Home = ""
	if c.JobLabel() != Label {
		t.Fatalf("default home: %s", c.JobLabel())
	}
}

func TestWanted(t *testing.T) {
	env := func(v string) func(string) string { return func(string) string { return v } }
	if !Wanted("darwin", env("")) || Wanted("darwin", env("off")) || Wanted("linux", env("")) {
		t.Fatal("Wanted")
	}
}

func TestLaunchEnv(t *testing.T) {
	env := []string{"PATH=/opt/homebrew/bin:/usr/bin", "LANG=en_GB.UTF-8", "GH_HOST=example",
		"SSH_CONNECTION=1 2 3 4", "SSH_TTY=/dev/ttys001", "SECURITYSESSIONID=186a5", "PWD=/x", "SHLVL=2",
		"SSH_AUTH_SOCK=/tmp/agent", "TERMINATR_HOME=/h", "XPC_SERVICE_NAME=0", "TMPDIR=/var/folders/x/"}
	get := func(k string) string {
		for _, kv := range env {
			if a, b, _ := strings.Cut(kv, "="); a == k {
				return b
			}
		}
		return ""
	}
	got := strings.Join(LaunchEnv(env, get), " ")
	if got != "PATH=/opt/homebrew/bin:/usr/bin LANG=en_GB.UTF-8 GH_HOST=example TERMINATR_HOME=/h" {
		t.Fatalf("over SSH: %s", got)
	}
	// A local terminal's agent socket stays (a password manager's, say).
	local := func(k string) string {
		if strings.HasPrefix(k, "SSH_") && k != "SSH_AUTH_SOCK" {
			return ""
		}
		return get(k)
	}
	if got := LaunchEnv(env, local); !strings.Contains(strings.Join(got, " "), "SSH_AUTH_SOCK=/tmp/agent") {
		t.Fatalf("locally: %s", got)
	}
}

func TestLaunchFile(t *testing.T) {
	c, _ := config(t, "darwin")
	c.Env = []string{"TM_LAUNCH_TEST=a=b", "TM_LAUNCH_EMPTY="}
	if err := c.writeLaunchFile(); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(c.LaunchFile()); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("launch file: %v %v", fi, err)
	}
	t.Setenv("TM_LAUNCH_TEST", "old")
	if bin, err := ApplyLaunchFile(c.LaunchFile()); err != nil || bin != c.Bin {
		t.Fatalf("bin %q, %v", bin, err)
	}
	if v, ok := os.LookupEnv("TM_LAUNCH_EMPTY"); os.Getenv("TM_LAUNCH_TEST") != "a=b" || !ok || v != "" {
		t.Fatalf("applied: %q %q", os.Getenv("TM_LAUNCH_TEST"), v)
	}
	os.Unsetenv("TM_LAUNCH_EMPTY")
	if bin, err := ApplyLaunchFile(filepath.Join(t.TempDir(), "none.json")); err != nil || bin != "" {
		t.Fatalf("missing file: %q %v", bin, err)
	}
	// The launch file names the tm as started, so a login start after
	// an upgrade pins the new build behind Homebrew's link.
	c.Source = "/opt/homebrew/bin/tm"
	if err := c.writeLaunchFile(); err != nil {
		t.Fatal(err)
	}
	if bin, err := ApplyLaunchFile(c.LaunchFile()); err != nil || bin != c.Source {
		t.Fatalf("source: %q %v", bin, err)
	}
}

func TestStart(t *testing.T) {
	c, f := config(t, "darwin")
	c.Env = []string{"PATH=/usr/bin"}
	job := "gui/501/" + c.JobLabel()
	onDemand := filepath.Join(c.RunDir, c.JobLabel()+".plist")
	check := func(what string, want ...string) {
		t.Helper()
		if strings.Join(f.calls, "\n") != strings.Join(want, "\n") {
			t.Fatalf("%s: calls\n%s\nwant\n%s", what, strings.Join(f.calls, "\n"), strings.Join(want, "\n"))
		}
		f.calls = nil
	}

	// Nobody at the console: nothing written, nothing loaded.
	f.fail = map[string]bool{"launchctl print gui/501": true}
	if err := c.Start(); !errors.Is(err, ErrNoConsole) {
		t.Fatalf("no console: %v", err)
	}
	check("no console", "launchctl print gui/501")
	if _, err := os.Stat(c.LaunchFile()); err == nil {
		t.Fatal("no console: launch file written")
	}

	// Not loaded: the on-demand job, which doesn't start at load.
	f.fail = map[string]bool{"launchctl print " + job: true}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	check("first start", "launchctl print gui/501", "launchctl print "+job,
		"launchctl bootstrap gui/501 "+onDemand, "launchctl kickstart "+job)
	data, _ := os.ReadFile(onDemand)
	if !strings.Contains(string(data), "<key>RunAtLoad</key>\n\t<false/>") {
		t.Fatalf("on-demand plist:\n%s", data)
	}
	if _, err := os.Stat(c.LaunchFile()); err != nil {
		t.Fatal("no launch file")
	}

	// Loaded and unchanged: just kickstart.
	f.fail = nil
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	check("loaded", "launchctl print gui/501", "launchctl print "+job, "launchctl kickstart "+job)

	// tm was upgraded: the job runs the pin, so its plist stays and it
	// is only kickstarted (the launch file names the new build).
	c.Bin = "/opt/homebrew/Cellar/terminatr/9.9.9/bin/tm"
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	check("upgraded", "launchctl print gui/501", "launchctl print "+job, "launchctl kickstart "+job)
	if after, _ := os.ReadFile(onDemand); !bytes.Equal(after, data) {
		t.Fatalf("upgrade changed the plist:\n%s", after)
	}

	// A changed plist (another PATH) reloads the job.
	c.Path = "/opt/homebrew/bin:/usr/bin:/bin"
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	check("changed", "launchctl print gui/501", "launchctl print "+job, "launchctl bootout "+job,
		"launchctl bootstrap gui/501 "+onDemand, "launchctl kickstart "+job)

	// With the login service installed, its plist is the job.
	installed, _ := c.File()
	os.MkdirAll(filepath.Dir(installed), 0o755)
	os.WriteFile(installed, []byte("old"), 0o644)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	check("installed", "launchctl print gui/501", "launchctl print "+job, "launchctl bootout "+job,
		"launchctl bootstrap gui/501 "+installed, "launchctl kickstart "+job)
	if data, _ := os.ReadFile(installed); !strings.Contains(string(data), "<key>RunAtLoad</key>\n\t<true/>") {
		t.Fatalf("installed plist:\n%s", data)
	}

	if c.GOOS = "linux"; !errors.Is(c.Start(), ErrUnsupported) {
		t.Fatal("linux")
	}
}

func TestUnload(t *testing.T) {
	c, f := config(t, "darwin")
	job := "gui/501/" + c.JobLabel()
	onDemand := filepath.Join(c.RunDir, c.JobLabel()+".plist")
	os.MkdirAll(c.RunDir, 0o700)
	os.WriteFile(onDemand, []byte("x"), 0o644)
	if err := c.Unload(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.calls, "\n") != "launchctl bootout "+job {
		t.Fatalf("calls: %v", f.calls)
	}
	if _, err := os.Stat(onDemand); err == nil {
		t.Fatal("plist kept")
	}
	// The default home's job, and an installed login service, stay.
	f.calls = nil
	d := c
	d.Home = ""
	d.Unload()
	installed, _ := c.File()
	os.MkdirAll(filepath.Dir(installed), 0o755)
	os.WriteFile(installed, []byte("x"), 0o644)
	c.Unload()
	if len(f.calls) != 0 {
		t.Fatalf("calls: %v", f.calls)
	}
}

func TestStrays(t *testing.T) {
	c, _ := config(t, "darwin")
	// A real home isn't in a temp dir: use one under the package.
	dir, err := os.MkdirTemp(".", "strays-")
	if err != nil {
		t.Fatal(err)
	}
	dir, _ = filepath.Abs(dir)
	t.Cleanup(func() { os.RemoveAll(dir) })
	live := filepath.Join(dir, "live.plist")
	os.WriteFile(live, nil, 0o644)
	bin := filepath.Join(dir, "tm")
	os.WriteFile(bin, nil, 0o755)
	gone := "/nonexistent/dev.terminatr.server.aaaa.plist"
	temp := filepath.Join(os.TempDir(), "x", "run", "b.plist")
	// A temp plist that exists is flagged by where it is.
	os.MkdirAll(filepath.Dir(temp), 0o755)
	os.WriteFile(temp, nil, 0o644)
	defer os.RemoveAll(filepath.Dir(filepath.Dir(temp)))
	list := "PID\tStatus\tLabel\n" +
		"-\t0\t" + c.JobLabel() + "\n" + // this home's
		"-\t0\tdev.terminatr.server.aaaa\n" +
		"-\t0\tdev.terminatr.server.bbbb\n" +
		"123\t0\tdev.terminatr.server.cccc\n" + // running
		"-\t0\tdev.terminatr.server.dddd\n" + // a real home's
		"-\t0\tdev.terminatr.server.eeee\n" + // its tm is gone
		"-\t0\tcom.apple.other\n"
	prints := map[string]string{
		"dev.terminatr.server.aaaa": "path = " + gone + "\n",
		"dev.terminatr.server.bbbb": "path = " + temp + "\n",
		"dev.terminatr.server.dddd": "path = " + live + "\nprogram = " + bin + "\n",
		"dev.terminatr.server.eeee": "path = " + live + "\nprogram = /nonexistent/tm\n",
	}
	c.Output = func(name string, args ...string) (string, error) {
		if args[0] == "list" {
			return list, nil
		}
		return prints[strings.TrimPrefix(args[1], "gui/501/")], nil
	}
	got, err := c.Strays()
	if err != nil {
		t.Fatal(err)
	}
	var labels []string
	for _, s := range got {
		labels = append(labels, s.Label)
	}
	if strings.Join(labels, " ") != "dev.terminatr.server.aaaa dev.terminatr.server.bbbb dev.terminatr.server.eeee" {
		t.Fatalf("strays: %+v", got)
	}
}
